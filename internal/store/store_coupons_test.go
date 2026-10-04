package store

import (
	"testing"
	"time"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// Regression tests for the coupon scope bug: coupons.product_ids is
// NOT NULL DEFAULT '{}', but pgx encodes a nil []string as SQL NULL, so a
// coupon created without picking products used to fail with
//   ERROR: null value in column "product_ids" ... (SQLSTATE 23502)
// and the API surfaced COUPON_CREATE_FAILED.

func TestNormalizeProductIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want int
	}{
		{"nil becomes empty (NOT NULL safe)", nil, 0},
		{"empty slice stays empty", []string{}, 0},
		{"blank entries are dropped", []string{"", "   ", "	"}, 0},
		{"real ids survive, blanks removed", []string{"", "prod-1", "  ", "prod-2"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeProductIDs(tc.in)
			if len(got) != tc.want {
				t.Fatalf("len(normalizeProductIDs(%q)) = %d, want %d", tc.in, len(got), tc.want)
			}
			// Must never come back nil: pgx would turn that back into SQL NULL.
			if got == nil {
				t.Fatal("normalizeProductIDs returned nil, which pgx encodes as SQL NULL")
			}
		})
	}
}

func TestCouponDiscountAcceptance(t *testing.T) {
	active := true
	base := model.Coupon{
		Active:         active,
		Type:           "percent",
		Value:          10,
		MaxUsesPerUser: 1,
		ProductIDs:     []string{}, // site-wide: the shape the bug fix now always stores
	}

	t.Run("site-wide coupon applies to any product", func(t *testing.T) {
		got, err := couponDiscount(base, "prod-anything", 10_000, 0)
		if err != nil {
			t.Fatalf("site-wide coupon rejected: %v", err)
		}
		if got != 1_000 {
			t.Fatalf("discount = %d, want 1000", got)
		}
	})

	t.Run("empty scope and nil scope behave the same", func(t *testing.T) {
		unscoped := base
		unscoped.ProductIDs = nil
		got, err := couponDiscount(unscoped, "prod-anything", 10_000, 0)
		if err != nil {
			t.Fatalf("nil-scope coupon rejected: %v", err)
		}
		if got != 1_000 {
			t.Fatalf("discount = %d, want 1000", got)
		}
	})

	t.Run("scoped coupon rejects other products", func(t *testing.T) {
		scoped := base
		scoped.ProductIDs = []string{"prod-1"}
		if _, err := couponDiscount(scoped, "prod-2", 10_000, 0); err == nil {
			t.Fatal("scoped coupon was applied to a product outside its scope")
		}
		got, err := couponDiscount(scoped, "prod-1", 10_000, 0)
		if err != nil {
			t.Fatalf("scoped coupon rejected its own product: %v", err)
		}
		if got != 1_000 {
			t.Fatalf("discount = %d, want 1000", got)
		}
	})

	t.Run("fixed amount never exceeds the subtotal", func(t *testing.T) {
		fixed := base
		fixed.Type = "fixed"
		fixed.Value = 50_000
		got, err := couponDiscount(fixed, "prod-1", 10_000, 0)
		if err != nil {
			t.Fatalf("fixed coupon rejected: %v", err)
		}
		if got != 10_000 {
			t.Fatalf("discount = %d, want the 10000 subtotal cap", got)
		}
	})

	t.Run("minimum spend and expiry are enforced", func(t *testing.T) {
		withMin := base
		withMin.MinAmountCents = 20_000
		if _, err := couponDiscount(withMin, "prod-1", 10_000, 0); err == nil {
			t.Fatal("coupon applied below its minimum spend")
		}
		past := time.Now().Add(-time.Hour)
		expired := base
		expired.ExpiresAt = &past
		if _, err := couponDiscount(expired, "prod-1", 10_000, 0); err == nil {
			t.Fatal("expired coupon was accepted")
		}
	})
}
