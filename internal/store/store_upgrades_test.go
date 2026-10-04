package store

import (
	"testing"
	"time"
)

// 升降级的换算直接决定收多少钱，因此逐条钉死。

func TestCycleDays(t *testing.T) {
	cases := map[string]int{
		"hourly": 1, "daily": 1, "monthly": 30, "quarterly": 90,
		"semiannually": 180, "yearly": 365, "onetime": 3650, "": 30, "nonsense": 30,
	}
	for cycle, want := range cases {
		if got := CycleDays(cycle); got != want {
			t.Fatalf("CycleDays(%q) = %d, want %d", cycle, got, want)
		}
	}
}

func TestRemainingValueProrates(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) *time.Time { tt := now.AddDate(0, 0, n); return &tt }

	t.Run("half of a monthly cycle", func(t *testing.T) {
		b := serviceUpgradeBase{cycle: "monthly", unitPrice: 3000, expiresAt: day(15)}
		value, remaining, inCycle := b.remainingValue(now)
		if inCycle != 30 {
			t.Fatalf("days in cycle = %d, want 30", inCycle)
		}
		if remaining != 15 {
			t.Fatalf("days remaining = %d, want 15", remaining)
		}
		if value != 1500 {
			t.Fatalf("remaining value = %d, want half of 3000", value)
		}
	})

	t.Run("config surcharge is prorated too", func(t *testing.T) {
		b := serviceUpgradeBase{cycle: "monthly", unitPrice: 2000, configCents: 1000, expiresAt: day(30)}
		value, remaining, _ := b.remainingValue(now)
		if remaining != 30 {
			t.Fatalf("days remaining = %d, want 30", remaining)
		}
		if value != 3000 {
			t.Fatalf("remaining value = %d, want the full 3000", value)
		}
	})

	t.Run("expired service has no remaining value", func(t *testing.T) {
		b := serviceUpgradeBase{cycle: "monthly", unitPrice: 3000, expiresAt: day(-1)}
		value, remaining, _ := b.remainingValue(now)
		if value != 0 || remaining != 0 {
			t.Fatalf("expired service: value=%d remaining=%d, want 0/0", value, remaining)
		}
	})

	t.Run("service without an expiry has no remaining value", func(t *testing.T) {
		b := serviceUpgradeBase{cycle: "monthly", unitPrice: 3000}
		value, remaining, _ := b.remainingValue(now)
		if value != 0 || remaining != 0 {
			t.Fatalf("no expiry: value=%d remaining=%d, want 0/0", value, remaining)
		}
	})

	t.Run("remaining days never exceed the cycle length", func(t *testing.T) {
		// 数据异常（到期时间被设到很远的未来）时不能算出超过全额的价值。
		b := serviceUpgradeBase{cycle: "monthly", unitPrice: 3000, expiresAt: day(365)}
		value, remaining, inCycle := b.remainingValue(now)
		if remaining != inCycle {
			t.Fatalf("days remaining = %d, want it clamped to %d", remaining, inCycle)
		}
		if value != 3000 {
			t.Fatalf("value = %d, want it clamped to the full 3000", value)
		}
	})

	t.Run("yearly cycle prorates over 365 days", func(t *testing.T) {
		b := serviceUpgradeBase{cycle: "yearly", unitPrice: 36500, expiresAt: day(73)}
		value, remaining, inCycle := b.remainingValue(now)
		if inCycle != 365 || remaining != 73 {
			t.Fatalf("cycle=%d remaining=%d, want 365/73", inCycle, remaining)
		}
		if value != 7300 {
			t.Fatalf("value = %d, want 7300", value)
		}
	})
}

// TestUpgradeDiffSign 说明差价的正负如何决定“要不要付款”。
func TestUpgradeDiffSign(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exp := now.AddDate(0, 0, 15)
	base := serviceUpgradeBase{cycle: "monthly", unitPrice: 3000, expiresAt: &exp}
	value, _, _ := base.remainingValue(now)

	upgrade := int64(5000) - value
	if upgrade <= 0 {
		t.Fatalf("moving to a pricier plan must be payable, got diff %d", upgrade)
	}
	downgrade := int64(1000) - value
	if downgrade > 0 {
		t.Fatalf("moving to a cheaper plan must not be payable, got diff %d", downgrade)
	}
	equal := value - value
	if equal != 0 {
		t.Fatalf("equal plans must have zero diff")
	}
}
