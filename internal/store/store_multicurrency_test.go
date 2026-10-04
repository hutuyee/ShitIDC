package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 多币种独立定价：同一个商品在每个币种上可以有自己的价格，
// 下单必须取「该币种」的价格，而不是拿基础货币的价格去换算。

// seedMultiCurrencyProduct 建一个同时有 CNY 与 USD 价格的商品。
// 两个币种的价格刻意做成不成比例，这样"用错币种"一定会被断言抓到。
func seedMultiCurrencyProduct(t *testing.T, s *Store) string {
	t.Helper()
	ctx := context.Background()
	p, err := s.CreateProduct(ctx, "multi-currency", "", "manual", "", "", "monthly", "CNY", 10000)
	if err != nil {
		t.Fatal(err)
	}
	// 美元价故意不等于任何汇率折算结果：20.00 USD 而不是 10.00。
	if err := s.SetProductPrices(ctx, p.PublicID, "USD", []PriceInput{{BillingCycle: "monthly", AmountCents: 2000}}); err != nil {
		t.Fatal(err)
	}
	return p.PublicID
}

func TestOrderUsesProductCurrencyPrice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	productID := seedMultiCurrencyProduct(t, s)

	// 显式用 USD 下单：必须取 USD 的价格 20.00，而不是 CNY 的 100.00。
	order, err := s.CreateOrderInCurrency(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "USD")
	if err != nil {
		t.Fatalf("usd order: %v", err)
	}
	if order.Currency != "USD" {
		t.Fatalf("order currency = %q, want USD", order.Currency)
	}
	if order.TotalCents != 2000 {
		t.Fatalf("order total = %d, want the USD price 2000 (not the CNY 10000)", order.TotalCents)
	}
	// 账单币种必须跟着订单走，否则收银台会按错的币种结算。
	var invoiceCurrency string
	var invoiceTotal int64
	if err := s.DB.QueryRow(ctx, `SELECT i.currency,i.total_cents FROM invoices i JOIN orders o ON o.id=i.order_id WHERE o.public_id=$1`, order.PublicID).Scan(&invoiceCurrency, &invoiceTotal); err != nil {
		t.Fatal(err)
	}
	if invoiceCurrency != "USD" || invoiceTotal != 2000 {
		t.Fatalf("invoice = %s/%d, want USD/2000", invoiceCurrency, invoiceTotal)
	}
}

func TestOrderRejectsCurrencyWithoutPrice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	productID := seedMultiCurrencyProduct(t, s)

	// EUR 没有价格：必须报错，而不是悄悄回退到 CNY 卖出去。
	_, err := s.CreateOrderInCurrency(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "EUR")
	if err == nil {
		t.Fatal("ordering in a currency the product has no price for must fail")
	}
	if !strings.Contains(err.Error(), "EUR") {
		t.Fatalf("error %q should name the missing currency", err.Error())
	}
}

func TestOrderCurrencyDefaultsToProductPrice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	productID := seedMultiCurrencyProduct(t, s)

	// 不指定币种：按商品在该周期上的可售币种解析（排序后取第一个，即 CNY）。
	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatalf("default currency order: %v", err)
	}
	if order.Currency != "CNY" {
		t.Fatalf("default currency = %q, want CNY", order.Currency)
	}
	if order.TotalCents != 10000 {
		t.Fatalf("default total = %d, want 10000", order.TotalCents)
	}
}

func TestWalletMustMatchOrderCurrency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// 只给人民币余额。
	userID := seedUser(t, s, 1_000_00)
	productID := seedMultiCurrencyProduct(t, s)

	order, err := s.CreateOrderInCurrency(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "USD")
	if err != nil {
		t.Fatalf("usd order: %v", err)
	}
	// 用余额支付一笔美元订单：没有美元钱包，必须失败而不是扣人民币。
	if _, err := s.PayOrderWithWallet(ctx, userID, order.PublicID, uuid.NewString()); err == nil {
		t.Fatal("paying a USD order from a CNY-only wallet must fail")
	}
	var cnyBalance int64
	if err := s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency='CNY'`, userID).Scan(&cnyBalance); err != nil {
		t.Fatal(err)
	}
	if cnyBalance != 1_000_00 {
		t.Fatalf("CNY balance changed to %d — a USD order must not touch the CNY wallet", cnyBalance)
	}
}

func TestListProductPriceCurrencies(t *testing.T) {
	s := newTestStore(t)
	productID := seedMultiCurrencyProduct(t, s)
	prices, err := s.ListProductPriceCurrencies(context.Background(), productID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 {
		t.Fatalf("got %d price rows, want 2 (CNY + USD)", len(prices))
	}
	seen := map[string]int64{}
	for _, pr := range prices {
		seen[pr.Currency] = pr.AmountCents
	}
	if seen["CNY"] != 10000 || seen["USD"] != 2000 {
		t.Fatalf("prices = %+v, want CNY 10000 and USD 2000", seen)
	}
}

// 一次请求里给多个币种各配一套价：建商品时必须都落库，且互不覆盖。
func TestCreateProductWithMultipleCurrencies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	prod, err := s.CreateProductWithPrices(ctx, "multi-cur-create", "", "manual", "", "", "CNY", "",
		ProductBillingInput{PayType: PayTypeRecurring, AllowQty: true},
		[]PriceInput{
			{BillingCycle: "monthly", AmountCents: 10000, Currency: "CNY"},
			{BillingCycle: "monthly", AmountCents: 2000, Currency: "USD"},
			{BillingCycle: "yearly", AmountCents: 100000, Currency: "CNY"},
		})
	if err != nil {
		t.Fatalf("create with multiple currencies: %v", err)
	}
	prices, err := s.ListProductPriceCurrencies(ctx, prod.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ cycle, cur string }
	got := map[key]int64{}
	for _, pr := range prices {
		got[key{pr.BillingCycle, pr.Currency}] = pr.AmountCents
	}
	if len(got) != 3 {
		t.Fatalf("got %d price rows, want 3: %+v", len(got), got)
	}
	if got[key{"monthly", "CNY"}] != 10000 {
		t.Fatalf("CNY monthly = %d, want 10000", got[key{"monthly", "CNY"}])
	}
	if got[key{"monthly", "USD"}] != 2000 {
		t.Fatalf("USD monthly = %d, want 2000", got[key{"monthly", "USD"}])
	}
	if got[key{"yearly", "CNY"}] != 100000 {
		t.Fatalf("CNY yearly = %d, want 100000", got[key{"yearly", "CNY"}])
	}
}

// 更新一个币种的价格，不能影响另一个币种——这是最初写错的地方。
func TestSetPricesForOneCurrencyKeepsOthers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedMultiCurrencyProduct(t, s)

	// 只改 CNY 的价格，USD 必须原样保留。
	if err := s.SetProductPrices(ctx, productID, "CNY", []PriceInput{{BillingCycle: "monthly", AmountCents: 15000}}); err != nil {
		t.Fatal(err)
	}
	prices, err := s.ListProductPriceCurrencies(ctx, productID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, pr := range prices {
		seen[pr.Currency] = pr.AmountCents
	}
	if seen["CNY"] != 15000 {
		t.Fatalf("CNY price = %d, want the updated 15000", seen["CNY"])
	}
	if seen["USD"] != 2000 {
		t.Fatalf("USD price = %d, want it untouched at 2000", seen["USD"])
	}
}
