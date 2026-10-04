package store

import (
	"context"
	"testing"
)

// 先钉死优先级与取整，再测数据库侧。

func TestApplyGroupPricePrecedence(t *testing.T) {
	// 只有标价。
	q := ApplyGroupPrice(10000, 0, -1)
	if q.UnitCents != 10000 || q.Source != PriceSourceList || q.DiscountCents != 0 {
		t.Fatalf("list-only quote = %+v", q)
	}
	// 标价 + 组折扣 20%。
	q = ApplyGroupPrice(10000, 20, -1)
	if q.UnitCents != 8000 || q.Source != PriceSourceGroupDiscount || q.DiscountCents != 2000 {
		t.Fatalf("20%% discount quote = %+v", q)
	}
	// 专属固定价**不再叠加**组折扣：标 50 元就该收 50 元。
	q = ApplyGroupPrice(10000, 50, 5000)
	if q.UnitCents != 5000 {
		t.Fatalf("override price = %d, want 5000 without stacking the group discount", q.UnitCents)
	}
	if q.Source != PriceSourceGroupProduct {
		t.Fatalf("source = %q, want group_product", q.Source)
	}
	if q.ListCents != 10000 || q.DiscountCents != 5000 {
		t.Fatalf("list/discount = %d/%d, want 10000/5000", q.ListCents, q.DiscountCents)
	}
}

func TestApplyGroupPriceRounding(t *testing.T) {
	// 1 分钱的 10% 折扣要四舍五入到 0 分，不能出现负价或 0.1 分的账。
	q := ApplyGroupPrice(1, 10, -1)
	if q.UnitCents != 1 {
		t.Fatalf("unit = %d, want 1 (a sub-cent discount rounds to nothing)", q.UnitCents)
	}
	// 3333 分打 33%：1000.89 分 -> 四舍五入 1001 分。
	q = ApplyGroupPrice(3333, 33, -1)
	if q.UnitCents != 3333-1100 {
		t.Fatalf("unit = %d, want %d", q.UnitCents, 3333-1100)
	}
	// 100% 折扣就是免费。
	q = ApplyGroupPrice(5000, 100, -1)
	if q.UnitCents != 0 {
		t.Fatalf("100%% discount = %d, want 0", q.UnitCents)
	}
	// 超过 100% 的脏数据被夹到 100%，绝不能算出负价。
	q = ApplyGroupPrice(5000, 150, -1)
	if q.UnitCents != 0 {
		t.Fatalf("over-100%% discount = %d, want 0 (clamped, never negative)", q.UnitCents)
	}
}

func TestApplyGroupPriceOverrideAboveList(t *testing.T) {
	// 专属价高于标价是合法配置（比如给某组涨价），但展示上不该出现「负优惠」。
	q := ApplyGroupPrice(1000, 0, 1500)
	if q.UnitCents != 1500 {
		t.Fatalf("unit = %d, want the override 1500", q.UnitCents)
	}
	if q.DiscountCents != 0 {
		t.Fatalf("discount = %d, want 0 rather than a negative number", q.DiscountCents)
	}
}

// ---- 数据库侧 ----

// seedGroup 建一个客户组并返回公开 ID。
func seedGroup(t *testing.T, s *Store, name string, discount int) string {
	t.Helper()
	g, err := s.CreateUserGroup(context.Background(), name, discount)
	if err != nil {
		t.Fatalf("create group %s: %v", name, err)
	}
	id, _ := g["id"].(string)
	if id == "" {
		t.Fatalf("CreateUserGroup returned no id: %+v", g)
	}
	return id
}

// assignGroup 把用户挂到客户组上。SetUserGroup 用的是用户公开 ID。
func assignGroup(t *testing.T, s *Store, userID int64, groupPublicID string) {
	t.Helper()
	var publicID string
	if err := s.DB.QueryRow(context.Background(),
		`SELECT public_id::text FROM users WHERE id=$1`, userID).Scan(&publicID); err != nil {
		t.Fatalf("read user public id: %v", err)
	}
	if err := s.SetUserGroup(context.Background(), publicID, groupPublicID); err != nil {
		t.Fatalf("assign group: %v", err)
	}
}

func TestGroupProductPriceIsUsedInsteadOfDiscount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedProduct(t, s, 10000)
	// 组本身有 50% 折扣。
	groupID := seedGroup(t, s, "VIP", 50)
	// 但这个商品给该组定死 3000 分。
	if err := s.SetUserProductPrice(ctx, groupID, productID, "monthly", "CNY", 3000); err != nil {
		t.Fatalf("set group price: %v", err)
	}
	userID := seedUser(t, s, 1_000_00)
	assignGroup(t, s, userID, groupID)

	quote, err := s.QuoteProductPrice(ctx, userID, productID, "monthly", "CNY")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if quote.UnitCents != 3000 {
		t.Fatalf("unit = %d, want the fixed 3000 (not 10000 minus 50%%)", quote.UnitCents)
	}
	if quote.Source != PriceSourceGroupProduct {
		t.Fatalf("source = %q, want group_product", quote.Source)
	}
	// 真正下单的时候也必须收 3000。
	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if order.TotalCents != 3000 {
		t.Fatalf("order total = %d, want the group fixed price 3000", order.TotalCents)
	}
}

func TestGroupDiscountAppliesWithoutOverride(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedProduct(t, s, 10000)
	groupID := seedGroup(t, s, "代理", 30)
	userID := seedUser(t, s, 1_000_00)
	assignGroup(t, s, userID, groupID)
	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	// 10000 打 7 折 = 7000。
	if order.TotalCents != 7000 {
		t.Fatalf("order total = %d, want 7000 after the 30%% group discount", order.TotalCents)
	}
	if order.DiscountCents != 3000 {
		t.Fatalf("discount = %d, want 3000 recorded on the order", order.DiscountCents)
	}
}

func TestGroupPriceDoesNotDiscountConfigAddons(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedProduct(t, s, 10000)
	groupID := seedGroup(t, s, "五折组", 50)
	userID := seedUser(t, s, 1_000_00)
	assignGroup(t, s, userID, groupID)

	// 配置项里额外买 4000 分的内存。
	opt, err := s.CreateConfigOption(ctx, productID, ConfigOptionInput{
		// option_type 与魔方一致：1=下拉 2=单选 3=开关 4=数量。
		Name: "内存", OptionType: 1, Required: true, SortWeight: 1,
		Values: []ConfigValueInput{{Label: "2G", PriceCents: 4000, SetupCents: 0, IsDefault: true}},
	})
	if err != nil {
		t.Fatalf("create option: %v", err)
	}
	order, err := s.CreateOrderWithConfig(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{
		Choices: []ConfigChoice{{OptionID: opt.PublicID, ValueID: opt.Values[0].PublicID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	// 商品 10000 打五折 = 5000；配置项 4000 不打折。合计 9000。
	if order.TotalCents != 9000 {
		t.Fatalf("order total = %d, want 9000 (5000 discounted product + 4000 full-price addon)", order.TotalCents)
	}
}

func TestGroupPriceIsPerCycleAndCurrency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedMultiCurrencyProduct(t, s)
	groupID := seedGroup(t, s, "分周期组", 0)
	userID := seedUser(t, s, 1_000_00)
	assignGroup(t, s, userID, groupID)
	// 只给「人民币月付」定专属价。
	if err := s.SetUserProductPrice(ctx, groupID, productID, "monthly", "CNY", 7777); err != nil {
		t.Fatalf("set: %v", err)
	}
	// 人民币月付命中。
	q, err := s.QuoteProductPrice(ctx, userID, productID, "monthly", "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if q.UnitCents != 7777 {
		t.Fatalf("CNY monthly = %d, want 7777", q.UnitCents)
	}
	// 美元月付没有专属价，回落到标价 2000。
	q2, err := s.QuoteProductPrice(ctx, userID, productID, "monthly", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if q2.UnitCents != 2000 {
		t.Fatalf("USD monthly = %d, want the list price 2000", q2.UnitCents)
	}
	if q2.Source != PriceSourceList {
		t.Fatalf("USD source = %q, want list", q2.Source)
	}
}

func TestDeleteGroupProductPriceFallsBackToList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedProduct(t, s, 10000)
	groupID := seedGroup(t, s, "临时代理", 25)
	userID := seedUser(t, s, 1_000_00)
	assignGroup(t, s, userID, groupID)
	if err := s.SetUserProductPrice(ctx, groupID, productID, "monthly", "CNY", 1234); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUserProductPrice(ctx, groupID, productID, "monthly", "CNY"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 删掉专属价后回落到「标价 + 组折扣」：10000 - 25% = 7500。
	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if order.TotalCents != 7500 {
		t.Fatalf("total = %d, want 7500 after falling back to list + group discount", order.TotalCents)
	}
	// 删一个不存在的必须报 NotFound，而不是静默成功。
	if err := s.DeleteUserProductPrice(ctx, groupID, productID, "monthly", "CNY"); err != ErrNotFound {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}
