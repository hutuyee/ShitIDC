package store

import "testing"

// 计费模型的校验规则：这些约束决定了免费/试用/一次性商品能不能被错误配置。

func TestValidBillingCycle(t *testing.T) {
	for _, c := range []string{"hourly", "daily", "monthly", "quarterly", "semiannually", "yearly", "onetime"} {
		if !ValidBillingCycle(c) {
			t.Fatalf("cycle %q should be valid", c)
		}
	}
	for _, c := range []string{"", "weekly", "biweekly", "MONTHLY ", "forever"} {
		if ValidBillingCycle(c) {
			t.Fatalf("cycle %q should be rejected", c)
		}
	}
}

func TestValidatePrices(t *testing.T) {
	cases := []struct {
		name    string
		payType string
		prices  []PriceInput
		wantErr bool
	}{
		{name: "recurring with several cycles", payType: PayTypeRecurring, prices: []PriceInput{{BillingCycle: "monthly", AmountCents: 1000}, {BillingCycle: "yearly", AmountCents: 10000}}},
		{name: "no price at all is rejected", payType: PayTypeRecurring, prices: nil, wantErr: true},
		{name: "unknown cycle is rejected", payType: PayTypeRecurring, prices: []PriceInput{{BillingCycle: "weekly", AmountCents: 100}}, wantErr: true},
		{name: "duplicate cycle is rejected", payType: PayTypeRecurring, prices: []PriceInput{{BillingCycle: "monthly", AmountCents: 1}, {BillingCycle: "monthly", AmountCents: 2}}, wantErr: true},
		{name: "negative amount is rejected", payType: PayTypeRecurring, prices: []PriceInput{{BillingCycle: "monthly", AmountCents: -1}}, wantErr: true},
		{name: "onetime requires an onetime tier", payType: PayTypeOneTime, prices: []PriceInput{{BillingCycle: "monthly", AmountCents: 100}}, wantErr: true},
		{name: "onetime with its tier is fine", payType: PayTypeOneTime, prices: []PriceInput{{BillingCycle: "onetime", AmountCents: 100}}},
		{name: "free must be priced at zero", payType: PayTypeFree, prices: []PriceInput{{BillingCycle: "monthly", AmountCents: 100}}, wantErr: true},
		{name: "free at zero is fine", payType: PayTypeFree, prices: []PriceInput{{BillingCycle: "monthly", AmountCents: 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePrices(tc.payType, tc.prices)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNormalizePayType(t *testing.T) {
	// 空字符串表示“不改动”，不算错误。
	if got, err := normalizePayType(""); err != nil || got != "" {
		t.Fatalf("empty pay type should pass through unchanged, got %q err %v", got, err)
	}
	if got, err := normalizePayType(" FREE "); err != nil || got != PayTypeFree {
		t.Fatalf("pay type should be trimmed and lower-cased, got %q err %v", got, err)
	}
	if _, err := normalizePayType("monthly"); err == nil {
		t.Fatal("an unknown pay type must be rejected")
	}
}
