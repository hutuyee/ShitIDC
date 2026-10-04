package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 购物车最关键的性质：价格实时重算、结算原子、合并付款只扣一次钱。

func TestCartRecalculatesPrices(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedProduct(t, s, 10000)
	userID := seedUser(t, s, 1_000_00)

	if _, err := s.AddCartItem(ctx, userID, productID, "monthly", 2, OrderConfigInput{}); err != nil {
		t.Fatalf("add: %v", err)
	}
	cart, err := s.GetCart(ctx, userID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cart.Count != 1 || len(cart.Items) != 1 {
		t.Fatalf("cart = %+v", cart)
	}
	if cart.Items[0].UnitCents != 10000 {
		t.Fatalf("unit = %d, want 10000", cart.Items[0].UnitCents)
	}
	if cart.TotalCents != 20000 {
		t.Fatalf("total = %d, want 20000 (2 x 10000)", cart.TotalCents)
	}
	if !cart.Payable {
		t.Fatal("a cart of in-stock products must be payable")
	}

	// 给用户挂一个 5 折组：购物车必须立刻反映新价格（因为不存价格）。
	groupID := seedGroup(t, s, "五折", 50)
	assignGroup(t, s, userID, groupID)
	cart2, err := s.GetCart(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if cart2.Items[0].UnitCents != 5000 {
		t.Fatalf("unit after group discount = %d, want 5000 (prices must be recomputed)", cart2.Items[0].UnitCents)
	}
	if cart2.TotalCents != 10000 {
		t.Fatalf("total after discount = %d, want 10000", cart2.TotalCents)
	}
}

func TestCartRejectsMixedCurrencies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	// 一个只有 CNY 价的商品。
	cnyProduct := seedProduct(t, s, 10000)
	if _, err := s.AddCartItem(ctx, userID, cnyProduct, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatalf("add cny: %v", err)
	}
	// 造一个只有 USD 价的商品。
	usd, err := s.CreateProduct(ctx, "usd-only", "", "manual", "", "", "monthly", "USD", 2000)
	if err != nil {
		t.Fatal(err)
	}
	// 多币种钱包是隔离的，一次付款只能扣一种，所以必须拒绝。
	_, err = s.AddCartItem(ctx, userID, usd.PublicID, "monthly", 1, OrderConfigInput{})
	if !errors.Is(err, ErrCartCurrencyMismatch) {
		t.Fatalf("mixed currency add = %v, want ErrCartCurrencyMismatch", err)
	}
}

func TestCartMarksUnavailableProducts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedProduct(t, s, 10000)
	userID := seedUser(t, s, 1_000_00)
	if _, err := s.AddCartItem(ctx, userID, productID, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	// 商品下架后购物车必须标为不可售，而不是等到结算才报错。
	if _, err := s.DB.Exec(ctx, `UPDATE products SET active=FALSE WHERE public_id=$1`, productID); err != nil {
		t.Fatal(err)
	}
	cart, err := s.GetCart(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if cart.Payable {
		t.Fatal("a cart containing a delisted product must not be payable")
	}
	if len(cart.Items) != 1 || cart.Items[0].Available {
		t.Fatalf("item should be marked unavailable: %+v", cart.Items)
	}
	if cart.Items[0].Reason == "" {
		t.Fatal("an unavailable item must explain why")
	}
	// 结算必须被拒。
	if _, err := s.CheckoutCart(ctx, userID, ""); err == nil {
		t.Fatal("checking out an unavailable cart must fail")
	}
}

// 合并结算：一次生成多笔订单，并共享一个批次。
func TestCheckoutCartCreatesOneOrderPerItem(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	p1 := seedProduct(t, s, 10000)
	p2, err := s.CreateProduct(ctx, "second", "", "manual", "", "", "monthly", "CNY", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p1, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p2.PublicID, "monthly", 2, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}

	res, err := s.CheckoutCart(ctx, userID, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if len(res.OrderIDs) != 2 {
		t.Fatalf("created %d orders, want 2", len(res.OrderIDs))
	}
	// 10000 + 2*5000 = 20000。
	if res.TotalCents != 20000 {
		t.Fatalf("total = %d, want 20000", res.TotalCents)
	}
	// 两笔订单必须挂在同一个批次上。
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM orders WHERE checkout_group_id=(SELECT id FROM checkout_groups WHERE public_id=$1)`, res.GroupID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("group holds %d orders, want 2", count)
	}
	// 结算后购物车必须被清空，否则会反复结算出重复订单。
	cart, err := s.GetCart(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if cart.Count != 0 {
		t.Fatalf("cart still has %d items after checkout", cart.Count)
	}
}

// 合并付款：一次扣款付清整批，且余额只被扣一次。
func TestPayCheckoutGroupDebitsOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00) // 1000 元
	p1 := seedProduct(t, s, 10000)
	p2, err := s.CreateProduct(ctx, "second", "", "manual", "", "", "monthly", "CNY", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p1, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p2.PublicID, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	res, err := s.CheckoutCart(ctx, userID, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	paid, err := s.PayCheckoutGroup(ctx, userID, res.GroupID, "idem-1")
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if paid.TotalCents != 15000 {
		t.Fatalf("paid = %d, want 15000", paid.TotalCents)
	}
	if len(paid.ServiceIDs) != 2 {
		t.Fatalf("created %d services, want 2", len(paid.ServiceIDs))
	}
	// 余额必须正好少了 15000，而不是每个订单各扣一次。
	var balance int64
	if err := s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency='CNY'`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 1_000_00-15000 {
		t.Fatalf("balance = %d, want %d (exactly one debit of 15000)", balance, 1_000_00-15000)
	}
	// 流水只能有一条 checkout 扣款。
	var debits int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM wallet_transactions wt JOIN wallet_accounts wa ON wa.id=wt.account_id WHERE wa.user_id=$1 AND wt.reference_type='checkout'`, userID).Scan(&debits); err != nil {
		t.Fatal(err)
	}
	if debits != 1 {
		t.Fatalf("found %d checkout debits, want exactly 1", debits)
	}
	// 两笔订单都必须已付款。
	var unpaid int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM orders WHERE checkout_group_id=(SELECT id FROM checkout_groups WHERE public_id=$1) AND status='unpaid'`, res.GroupID).Scan(&unpaid); err != nil {
		t.Fatal(err)
	}
	if unpaid != 0 {
		t.Fatalf("%d orders are still unpaid after paying the group", unpaid)
	}
	// 幂等：再付一次不重复扣款。
	paid2, err := s.PayCheckoutGroup(ctx, userID, res.GroupID, "idem-1")
	if err != nil {
		t.Fatalf("second pay: %v", err)
	}
	if !paid2.AlreadyPaid {
		t.Fatal("a second payment of the same group must be reported as already paid")
	}
	if err := s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency='CNY'`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 1_000_00-15000 {
		t.Fatalf("balance changed to %d on the idempotent replay", balance)
	}
}

// 整批原子性：余额不够时，一笔订单都不能被标记已付。
func TestPayCheckoutGroupIsAllOrNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// 只给 100 元，但购物车要 150 元。
	userID := seedUser(t, s, 100_00)
	p1 := seedProduct(t, s, 10000)
	p2, err := s.CreateProduct(ctx, "second", "", "manual", "", "", "monthly", "CNY", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p1, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p2.PublicID, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	res, err := s.CheckoutCart(ctx, userID, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PayCheckoutGroup(ctx, userID, res.GroupID, "idem-broke")
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("pay = %v, want ErrInsufficientBalance", err)
	}
	// 关键：不能出现「付了一半」。
	var paidCount int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM orders WHERE checkout_group_id=(SELECT id FROM checkout_groups WHERE public_id=$1) AND status<>'unpaid'`, res.GroupID).Scan(&paidCount); err != nil {
		t.Fatal(err)
	}
	if paidCount != 0 {
		t.Fatalf("%d orders were settled despite the payment failing", paidCount)
	}
	// 余额一分不动。
	var balance int64
	if err := s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency='CNY'`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 100_00 {
		t.Fatalf("balance = %d, want it untouched at 10000", balance)
	}
	if _, err := s.DB.Exec(ctx, `SELECT 1 WHERE false`); err != nil {
		t.Fatal(err)
	}
	_ = strings.TrimSpace("")
}

func TestCartRemoveAndClear(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	p1 := seedProduct(t, s, 1000)
	p2, err := s.CreateProduct(ctx, "second", "", "manual", "", "", "monthly", "CNY", 2000)
	if err != nil {
		t.Fatal(err)
	}
	item1, err := s.AddCartItem(ctx, userID, p1, "monthly", 1, OrderConfigInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p2.PublicID, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCartItem(ctx, userID, item1.PublicID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	cart, _ := s.GetCart(ctx, userID)
	if cart.Count != 1 {
		t.Fatalf("count = %d, want 1 after removing one item", cart.Count)
	}
	// 移除不存在的项要报 NotFound。
	if err := s.RemoveCartItem(ctx, userID, item1.PublicID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove = %v, want ErrNotFound", err)
	}
	n, err := s.ClearCart(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cleared %d items, want 1", n)
	}
	cart2, _ := s.GetCart(ctx, userID)
	if cart2.Count != 0 {
		t.Fatal("cart should be empty after clearing")
	}
	// 同一商品重复加购是覆盖而不是新增。
	if _, err := s.AddCartItem(ctx, userID, p1, "monthly", 1, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCartItem(ctx, userID, p1, "monthly", 5, OrderConfigInput{}); err != nil {
		t.Fatal(err)
	}
	cart3, _ := s.GetCart(ctx, userID)
	if cart3.Count != 1 || cart3.Items[0].Quantity != 5 {
		t.Fatalf("re-adding the same product must overwrite: %+v", cart3.Items)
	}
}
