package store

import (
	"context"
	"testing"
)

// 优惠券的 product_ids 是 uuid[]，而 Go 侧传的是 []string。
// 历史上这里连撞过两次：23502（NOT NULL 写 NULL）与 42804（text 不给 uuid[]）。
// 两个方向都必须覆盖到。

func TestCreateCouponWithAndWithoutProductScope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p1 := seedProduct(t, s, 10000)
	p2, err := s.CreateProduct(ctx, "second", "", "manual", "", "", "monthly", "CNY", 5000)
	if err != nil {
		t.Fatal(err)
	}

	// 1) 带商品范围 —— 曾经报 42804。
	scoped, err := s.CreateCoupon(ctx, "SCOPED10", "percent", 10, nil, 1, 0, []string{p1, p2.PublicID}, nil, nil, true)
	if err != nil {
		t.Fatalf("create scoped coupon: %v", err)
	}
	if len(scoped.ProductIDs) != 2 {
		t.Fatalf("scoped coupon stored %d product ids, want 2: %v", len(scoped.ProductIDs), scoped.ProductIDs)
	}
	seen := map[string]bool{}
	for _, id := range scoped.ProductIDs {
		seen[id] = true
	}
	if !seen[p1] || !seen[p2.PublicID] {
		t.Fatalf("stored ids do not match the submitted ones: %v", scoped.ProductIDs)
	}

	// 2) 不带范围（空数组）—— 曾经报 23502；也必须落成空数组而不是 NULL。
	unscoped, err := s.CreateCoupon(ctx, "ALL10", "fixed", 500, nil, 1, 0, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("create unscoped coupon: %v", err)
	}
	if len(unscoped.ProductIDs) != 0 {
		t.Fatalf("unscoped coupon has scope: %v", unscoped.ProductIDs)
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM coupons WHERE public_id=$1 AND product_ids='{}'::uuid[]`, unscoped.PublicID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("an unscoped coupon must store an empty uuid array, not NULL")
	}

	// 3) 显式传空切片（而不是 nil）也要正常。
	if _, err := s.CreateCoupon(ctx, "EMPTY10", "percent", 5, nil, 1, 0, []string{}, nil, nil, true); err != nil {
		t.Fatalf("create coupon with empty slice: %v", err)
	}
	// 4) 全是空白的 id 会被过滤掉，同样落成空数组。
	blank, err := s.CreateCoupon(ctx, "BLANK10", "percent", 5, nil, 1, 0, []string{"", "   "}, nil, nil, true)
	if err != nil {
		t.Fatalf("create coupon with blank ids: %v", err)
	}
	if len(blank.ProductIDs) != 0 {
		t.Fatalf("blank ids should be filtered out: %v", blank.ProductIDs)
	}

	// 5) 取回来时范围必须一致（读写两条路径都要正确）。
	loaded, err := s.loadCoupon(ctx, "SCOPED10")
	if err != nil {
		t.Fatalf("loadCoupon: %v", err)
	}
	if len(loaded.ProductIDs) != 2 {
		t.Fatalf("reloaded scope = %v, want 2 ids", loaded.ProductIDs)
	}

	// 6) 重复码必须被拒。
	if _, err := s.CreateCoupon(ctx, "SCOPED10", "percent", 10, nil, 1, 0, nil, nil, nil, true); err == nil {
		t.Fatal("duplicate coupon code must be rejected")
	}
}

// 范围过滤在结算时必须真的生效：不在范围内的商品不能用券。
func TestCouponScopeFiltersProducts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	inScope := seedProduct(t, s, 10000)
	outOfScope, err := s.CreateProduct(ctx, "outside", "", "manual", "", "", "monthly", "CNY", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCoupon(ctx, "ONLYIN", "percent", 50, nil, 1, 0, []string{inScope}, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	userID := seedUser(t, s, 1_000_00)

	// 范围内的商品：券生效，10000 -> 5000。
	inOrder, err := s.CreateOrder(ctx, userID, inScope, "monthly", 1, "ONLYIN")
	if err != nil {
		t.Fatalf("order in scope: %v", err)
	}
	if inOrder.TotalCents != 5000 {
		t.Fatalf("in-scope total = %d, want 5000 after a 50%% coupon", inOrder.TotalCents)
	}
	// 范围外的商品：必须报错，而不是默默给折扣。
	if _, err := s.CreateOrder(ctx, userID, outOfScope.PublicID, "monthly", 1, "ONLYIN"); err == nil {
		t.Fatal("a coupon must not apply to a product outside its scope")
	}
}
