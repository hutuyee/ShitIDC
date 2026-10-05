package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 购物车多商品结算（对应魔方 shd_cart_session）。
//
// 核心决定：**购物车不存价格**。每次读取都用与下单完全相同的计价逻辑重算
// （`QuoteProductPrice`），所以组折扣、专属价、配置加价、库存变化都会立刻反映。
// 存价格的话就会出现「加购时 100 元、结算时按 100 元卖但市价已经 120」的问题。

// CartItem 是购物车里的一项（含实时算出的价格）。
type CartItem struct {
	PublicID     string            `json:"id"`
	ProductID    string            `json:"product_id"`
	ProductName  string            `json:"product_name"`
	BillingCycle string            `json:"billing_cycle"`
	Currency     string            `json:"currency"`
	Quantity     int               `json:"quantity"`
	Choices      []ConfigChoice    `json:"config"`
	CustomFields map[string]string `json:"custom_fields,omitempty"`
	// UnitCents 是当前单价（含组折扣/专属价/配置加价）。
	UnitCents int64 `json:"unit_cents"`
	// ListCents 是标价，用于展示划线价。
	ListCents     int64  `json:"list_cents"`
	ConfigCents   int64  `json:"config_cents"`
	SetupCents    int64  `json:"setup_cents"`
	SubtotalCents int64  `json:"subtotal_cents"`
	PriceSource   string `json:"price_source"`
	// Available 是当前可售状态（下架/库存不足时为 false），前端给灰显。
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// SetupCents 只在整车里计一次（与单笔下单一致）。
	CreatedAt string `json:"created_at"`
}

// Cart 是一个用户的购物车视图。
type Cart struct {
	Items []CartItem `json:"items"`
	// Currency 为空的购物车没有币种约束；一旦有商品就固定下来。
	Currency string `json:"currency"`
	// ItemsTotalCents 是各明细小计之和（配置加价已按件计算）。
	ItemsTotalCents int64 `json:"items_total_cents"`
	// SetupTotalCents 是初装费合计（一次性）。
	SetupTotalCents int64 `json:"setup_total_cents"`
	// TotalCents = ItemsTotalCents + SetupTotalCents。
	TotalCents int64 `json:"total_cents"`
	// Count 是明细条数（不是件数）。
	Count int `json:"count"`
	// Payable 表示整车是否可结算（所有明细都 available）。
	Payable bool `json:"payable"`
	// SetupCents 已并入 TotalCents，这里保留字段名给前端展示。
	ConfigTotalCents int64 `json:"config_total_cents"`
}

// ErrCartCurrencyMismatch 表示往购物车里加了不同币种的商品。
var ErrCartCurrencyMismatch = errors.New("购物车只支持一种币种，请先清空再加购")

// AddCartItem 把商品加入购物车（同商品同周期同币种覆盖数量与配置）。
func (s *Store) AddCartItem(ctx context.Context, userID int64, productPublicID, billingCycle string, quantity int, cfgIn OrderConfigInput) (CartItem, error) {
	return s.AddCartItemWithCurrency(ctx, userID, productPublicID, billingCycle, "", quantity, cfgIn)
}

// AddCartItemWithCurrency 与 AddCartItem 相同，但由调用方指定币种：多币种商品从产品页
// 加购时保持用户选中的那一种；currency 为空时回退为「该商品该周期下排序第一个可用币种」。
func (s *Store) AddCartItemWithCurrency(ctx context.Context, userID int64, productPublicID, billingCycle, currency string, quantity int, cfgIn OrderConfigInput) (CartItem, error) {
	if quantity < 1 || quantity > 100 {
		return CartItem{}, fmt.Errorf("数量必须在 1 到 100 之间")
	}
	// 币种必须与车里已有的一致：多币种钱包是隔离的，一次付款只能扣一种。
	var existingCurrency string
	err := s.DB.QueryRow(ctx, `SELECT currency FROM cart_items WHERE user_id=$1 LIMIT 1`, userID).Scan(&existingCurrency)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CartItem{}, err
	}
	// 校验配置项合法并算出加价。用 `resolveConfigTx` 而不是另写一套：
	// 加购与下单必须用完全相同的计价逻辑，否则结算价会和加购价对不上。
	// 借一个短事务当 querior 用（不要用连接池，那会在并发下自锁）。
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return CartItem{}, err
	}
	defer tx.Rollback(ctx)
	cfg, selections, fields, err := s.resolveConfigTx(ctx, tx, productPublicID, cfgIn)
	if err != nil {
		return CartItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CartItem{}, err
	}
	var productID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND active=true AND deleted_at IS NULL`, productPublicID).Scan(&productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CartItem{}, ErrNotFound
		}
		return CartItem{}, err
	}
	if strings.TrimSpace(currency) == "" {
		if err := s.DB.QueryRow(ctx, `SELECT currency FROM product_prices WHERE product_id=$1 AND billing_cycle=$2 AND active=true ORDER BY currency LIMIT 1`,
			productID, strings.ToLower(strings.TrimSpace(billingCycle))).Scan(&currency); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return CartItem{}, fmt.Errorf("该商品在此周期下没有价格")
			}
			return CartItem{}, err
		}
	} else if err := s.DB.QueryRow(ctx, `SELECT currency FROM product_prices WHERE product_id=$1 AND billing_cycle=$2 AND active=true AND upper(currency)=upper($3) LIMIT 1`,
		productID, strings.ToLower(strings.TrimSpace(billingCycle)), strings.TrimSpace(currency)).Scan(&currency); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CartItem{}, fmt.Errorf("该商品在此周期与币种下没有价格")
		}
		return CartItem{}, err
	}
	if existingCurrency != "" && !strings.EqualFold(existingCurrency, currency) {
		return CartItem{}, ErrCartCurrencyMismatch
	}
	var v CartItem
	var created string
	err = s.DB.QueryRow(ctx, `INSERT INTO cart_items(user_id,product_id,billing_cycle,currency,quantity,config_selections,custom_fields)
VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb)
ON CONFLICT (user_id,product_id,billing_cycle,currency) DO UPDATE SET
  quantity=excluded.quantity,
  config_selections=excluded.config_selections,
  custom_fields=excluded.custom_fields,
  updated_at=now()
RETURNING public_id::text,product_id,billing_cycle,currency,quantity,created_at::text`,
		userID, productID, strings.ToLower(strings.TrimSpace(billingCycle)), currency, quantity, selections, fields).
		Scan(&v.PublicID, &productID, &v.BillingCycle, &v.Currency, &v.Quantity, &created)
	if err != nil {
		return CartItem{}, err
	}
	v.Choices = cfgIn.Choices
	v.CustomFields = cfgIn.CustomFields
	v.UnitCents = cfg.ConfigCents
	return v, nil
}

// GetCart 读出一个用户的购物车，并用当前定价重算每一项。
//
// 「重算」是刻意的：购物车不存价格，所以组折扣调整、专属价变更、商品下架、
// 库存变化都会在下一次读取时立刻体现，而不是等到付款才暴露。
func (s *Store) GetCart(ctx context.Context, userID int64) (Cart, error) {
	cart := Cart{Items: []CartItem{}}
	rows, err := s.DB.Query(ctx, `SELECT c.public_id::text,c.product_id,p.public_id::text,p.name,p.active,
  c.billing_cycle,c.currency,c.quantity,c.config_selections,c.custom_fields,c.created_at::text
FROM cart_items c JOIN products p ON p.id=c.product_id
WHERE c.user_id=$1 ORDER BY c.created_at, c.id`, userID)
	if err != nil {
		return cart, err
	}
	defer rows.Close()
	type rawItem struct {
		publicID, productID, productName, cycle, currency, createdAt string
		active                                                       bool
		quantity                                                     int
		selections, fields                                           []byte
	}
	raws := []rawItem{}
	for rows.Next() {
		var r rawItem
		if err := rows.Scan(&r.publicID, new(int64), &r.productID, &r.productName, &r.active,
			&r.cycle, &r.currency, &r.quantity, &r.selections, &r.fields, &r.createdAt); err != nil {
			return cart, err
		}
		raws = append(raws, r)
	}
	if err := rows.Err(); err != nil {
		return cart, err
	}
	if len(raws) == 0 {
		cart.Payable = true
		return cart, nil
	}
	cart.Currency = raws[0].currency
	// 先假定整车可售，遇到不可售的明细再置为 false。
	cart.Payable = true

	for _, r := range raws {
		item := CartItem{
			PublicID: r.publicID, ProductID: r.productID, ProductName: r.productName,
			BillingCycle: r.cycle, Currency: r.currency, Quantity: r.quantity,
			CreatedAt: r.createdAt, Available: true,
		}
		_ = json.Unmarshal(r.selections, &item.Choices)
		_ = json.Unmarshal(r.fields, &item.CustomFields)
		if !r.active {
			item.Available = false
			item.Reason = "商品已下架"
		}
		// 用与下单相同的计价：组折扣 / 专属价 / 配置加价。
		quote, err := s.QuoteProductPrice(ctx, userID, r.productID, r.cycle, r.currency)
		if err != nil {
			// 没有价格（下架或删档）：标记不可售，而不是让整车报错。
			item.Available = false
			if item.Reason == "" {
				item.Reason = "该商品在此周期与币种下已无可售价格"
			}
			cart.Items = append(cart.Items, item)
			cart.Payable = false
			continue
		}
		// 配置加价要用当前选项重算（商品可能改了配置项）。
		cfgPricing, perr := s.priceConfigSelection(ctx, r.productID, item.Choices)
		if perr != nil {
			item.Available = false
			item.Reason = perr.Error()
			cart.Items = append(cart.Items, item)
			cart.Payable = false
			continue
		}
		item.ListCents = quote.ListCents
		item.PriceSource = quote.Source
		item.ConfigCents = cfgPricing.ConfigCents
		item.SetupCents = cfgPricing.SetupCents
		item.UnitCents = quote.UnitCents + cfgPricing.ConfigCents
		item.SubtotalCents = item.UnitCents * int64(item.Quantity)
		cart.ItemsTotalCents += item.SubtotalCents
		cart.ConfigTotalCents += cfgPricing.ConfigCents * int64(item.Quantity)
		// 初装费一次性，不随数量放大，也不随件数重复计——与单笔下单一致，整车里只计一次。
		if cfgPricing.SetupCents > cart.SetupTotalCents {
			cart.SetupTotalCents = cfgPricing.SetupCents
		}
		if !item.Available {
			cart.Payable = false
		}
		cart.Items = append(cart.Items, item)
	}
	cart.Count = len(cart.Items)
	cart.TotalCents = cart.ItemsTotalCents + cart.SetupTotalCents
	// 有任何一项不可售，整车不可结算。
	for _, it := range cart.Items {
		if !it.Available {
			cart.Payable = false
		}
	}
	return cart, nil
}

// priceConfigSelection 用当前配置项定义重算一次选择的价格。
func (s *Store) priceConfigSelection(ctx context.Context, productPublicID string, choices []ConfigChoice) (ConfigPricing, error) {
	if len(choices) == 0 {
		return ConfigPricing{}, nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ConfigPricing{}, err
	}
	defer tx.Rollback(ctx)
	options, err := listProductConfigOptionsQ(ctx, tx, productPublicID)
	if err != nil {
		return ConfigPricing{}, err
	}
	links, err := listProductConfigLinksQ(ctx, tx, productPublicID)
	if err != nil {
		return ConfigPricing{}, err
	}
	return ResolveConfigSelectionWithLinks(options, links, choices)
}

// RemoveCartItem 从购物车移除一项。
func (s *Store) RemoveCartItem(ctx context.Context, userID int64, itemPublicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM cart_items WHERE user_id=$1 AND public_id=$2`, userID, itemPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearCart 清空购物车，返回清掉的条数。
func (s *Store) ClearCart(ctx context.Context, userID int64) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM cart_items WHERE user_id=$1`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- 结算 ----

// CheckoutResult 是一次购物车结算的结果。
type CheckoutResult struct {
	GroupID    string   `json:"checkout_id"`
	Currency   string   `json:"currency"`
	TotalCents int64    `json:"total_cents"`
	OrderIDs   []string `json:"order_ids"`
	// ServiceIDs 只有在下单即开通（免费/试用）时才有值。
	ServiceIDs []string `json:"service_ids"`
}

// CheckoutCart 把购物车整批转成订单。
//
// 原子性：整个过程在一个可串行化事务里。要么所有明细都成单，要么一条都不成——
// 部分成功会让用户付了钱却少一台机器，这是最不能接受的失败模式。
//
// 价格在事务内**重新计算**并写进 checkout_groups.total_cents 作为「约定金额」；
// 付款时会再比对一次，防止两次读取之间价格被改动。
func (s *Store) CheckoutCart(ctx context.Context, userID int64, couponCode string) (CheckoutResult, error) {
	return s.CheckoutCartWithVoucher(ctx, userID, couponCode, "")
}

// CheckoutCartWithVoucher 与 CheckoutCart 相同，但额外核销一张代金券：
// 作用于购物车中第一条商品 / 周期匹配的明细（代金券插件对齐）。
func (s *Store) CheckoutCartWithVoucher(ctx context.Context, userID int64, couponCode, voucherCode string) (CheckoutResult, error) {
	var out CheckoutResult
	err := retrySerializable(ctx, orderRetryAttempts, func() error {
		res, err := s.checkoutCartOnce(ctx, userID, couponCode, voucherCode)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Store) checkoutCartOnce(ctx context.Context, userID int64, couponCode, voucherCode string) (CheckoutResult, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return CheckoutResult{}, err
	}
	defer tx.Rollback(ctx)

	// 锁住购物车行，避免结算过程中被并发修改。
	rows, err := tx.Query(ctx, `SELECT c.public_id::text,p.public_id::text,c.billing_cycle,c.currency,c.quantity,
  c.config_selections,c.custom_fields
FROM cart_items c JOIN products p ON p.id=c.product_id
WHERE c.user_id=$1 ORDER BY c.created_at, c.id FOR UPDATE OF c`, userID)
	if err != nil {
		return CheckoutResult{}, err
	}
	type line struct {
		productID, cycle, currency, selections, fields string
		quantity                                       int
	}
	lines := []line{}
	for rows.Next() {
		var l line
		var itemID string
		var sel, fld []byte
		if err := rows.Scan(&itemID, &l.productID, &l.cycle, &l.currency, &l.quantity, &sel, &fld); err != nil {
			rows.Close()
			return CheckoutResult{}, err
		}
		l.selections = string(sel)
		l.fields = string(fld)
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return CheckoutResult{}, err
	}
	if len(lines) == 0 {
		return CheckoutResult{}, fmt.Errorf("购物车是空的")
	}
	currency := lines[0].currency
	// 代金券：先挑出第一条商品 / 周期匹配的明细；金额与领取状态在核销时再校验。
	voucherLine := -1
	voucherCode = strings.TrimSpace(voucherCode)
	if voucherCode != "" {
		for i, l := range lines {
			ok, verr := s.VoucherLineMatches(ctx, voucherCode, l.productID, l.cycle)
			if verr != nil {
				return CheckoutResult{}, verr
			}
			if ok {
				voucherLine = i
				break
			}
		}
		if voucherLine < 0 {
			return CheckoutResult{}, fmt.Errorf("该代金券不适用于购物车中的商品或计费周期")
		}
	}
	// 捆绑限制：同一结算批次即视为「同时购买」，在整车维度校验。
	cartProductIDs := make([]string, 0, len(lines))
	for _, l := range lines {
		cartProductIDs = append(cartProductIDs, l.productID)
	}
	if err := checkProductBundleLimits(ctx, tx, cartProductIDs); err != nil {
		return CheckoutResult{}, err
	}

	// 先建批次，拿到 group id 供各订单引用。
	var groupID int64
	var groupPublic string
	if err := tx.QueryRow(ctx, `INSERT INTO checkout_groups(user_id,currency,total_cents,order_count)
VALUES($1,$2,0,0) RETURNING id,public_id::text`, userID, currency).Scan(&groupID, &groupPublic); err != nil {
		return CheckoutResult{}, err
	}

	var total int64
	orderIDs := []string{}
	// 结算只负责把购物车变成一批「待付款订单」。开通要等付款之后，
	// 所以这里没有服务 ID；免费/试用商品会在付款环节（0 元）生成服务。
	serviceIDs := []string{}
	couponUsed := false

	for i, l := range lines {
		cfgIn := OrderConfigInput{}
		if i == voucherLine {
			cfgIn.VoucherCode = voucherCode
		}
		if l.selections != "" && l.selections != "[]" {
			var choices []ConfigChoice
			if err := json.Unmarshal([]byte(l.selections), &choices); err != nil {
				return CheckoutResult{}, fmt.Errorf("购物车里的配置项数据已损坏")
			}
			cfgIn.Choices = choices
		}
		// 优惠码只作用于第一单：券是「一笔订单一张」，合并结算时不能重复核销。
		coupon := ""
		if !couponUsed {
			coupon = couponCode
		}
		// createOrderOnce 内部会重新校验库存、配置项与价格，并自己算总价。
		o, err := s.createOrderInTx(ctx, tx, userID, l.productID, l.cycle, l.quantity, coupon, cfgIn, currency, groupID, false)
		if err != nil {
			return CheckoutResult{}, err
		}
		if coupon != "" && o.DiscountCents > 0 {
			couponUsed = true
		}
		total += o.TotalCents
		orderIDs = append(orderIDs, o.PublicID)
	}

	if _, err := tx.Exec(ctx, `UPDATE checkout_groups SET total_cents=$2,order_count=$3 WHERE id=$1`,
		groupID, total, len(orderIDs)); err != nil {
		return CheckoutResult{}, err
	}
	// 结算成功后清空购物车：不清的话用户会反复结算出重复订单。
	if _, err := tx.Exec(ctx, `DELETE FROM cart_items WHERE user_id=$1`, userID); err != nil {
		return CheckoutResult{}, err
	}
	// 批次内所有订单的到期时间统一：一批货应该在同一个时间点到期。
	if _, err := tx.Exec(ctx, `UPDATE invoices SET due_at=(SELECT min(i.due_at) FROM invoices i JOIN orders o ON o.id=i.order_id WHERE o.checkout_group_id=$1) WHERE order_id IN (SELECT id FROM orders WHERE checkout_group_id=$1)`, groupID); err != nil {
		return CheckoutResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CheckoutResult{}, err
	}
	return CheckoutResult{
		GroupID: groupPublic, Currency: currency, TotalCents: total,
		OrderIDs: orderIDs, ServiceIDs: serviceIDs,
	}, nil
}

// ---- 合并付款 ----

// CheckoutPayResult 是一次合并付款的结果。
type CheckoutPayResult struct {
	GroupID     string   `json:"checkout_id"`
	TotalCents  int64    `json:"total_cents"`
	OrderIDs    []string `json:"order_ids"`
	ServiceIDs  []string `json:"service_ids"`
	AlreadyPaid bool     `json:"already_paid"`
}

// PayCheckoutGroup 用余额一次性支付整批订单。
//
// 为什么必须整批而不是逐单循环：逐单付款在中间失败时会留下「付了 3 单、第 4 单失败」
// 的半成品状态，用户既拿不到完整的一批货、又要自己申请退款。这里用一个可串行化事务
// 加**一次**钱包扣减，要么全部成功要么全部回滚。
//
// 金额一致性：扣款额取自各订单当前金额之和，并与下单时写进 checkout_groups.total_cents
// 的「约定金额」比对。不一致说明两次读取之间价格被改动过，此时宁可停下来让用户确认。
func (s *Store) PayCheckoutGroup(ctx context.Context, userID int64, groupPublicID string, idempotencyKey string) (CheckoutPayResult, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return CheckoutPayResult{}, fmt.Errorf("缺少幂等键")
	}
	var out CheckoutPayResult
	err := retrySerializable(ctx, orderRetryAttempts, func() error {
		res, err := s.payCheckoutGroupOnce(ctx, userID, groupPublicID, idempotencyKey)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Store) payCheckoutGroupOnce(ctx context.Context, userID int64, groupPublicID, idempotencyKey string) (CheckoutPayResult, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return CheckoutPayResult{}, err
	}
	defer tx.Rollback(ctx)

	var groupID int64
	var currency, status string
	var agreedTotal int64
	err = tx.QueryRow(ctx, `SELECT id,currency,status,total_cents FROM checkout_groups
WHERE public_id=$1 AND user_id=$2 FOR UPDATE`, groupPublicID, userID).
		Scan(&groupID, &currency, &status, &agreedTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckoutPayResult{}, ErrNotFound
	}
	if err != nil {
		return CheckoutPayResult{}, err
	}
	res := CheckoutPayResult{GroupID: groupPublicID}
	if status == "paid" {
		// 幂等：重复调用返回既有结果，不重复扣款。
		res.AlreadyPaid = true
		res.TotalCents = agreedTotal
		if err := collectGroupOrders(ctx, tx, groupID, &res); err != nil {
			return CheckoutPayResult{}, err
		}
		return res, nil
	}
	if status != "unpaid" {
		return CheckoutPayResult{}, ErrInvalidState
	}

	// 锁住该批次的订单并汇总金额。
	rows, err := tx.Query(ctx, `SELECT id,public_id::text,total_cents,status,kind FROM orders
WHERE checkout_group_id=$1 AND user_id=$2 ORDER BY id FOR UPDATE`, groupID, userID)
	if err != nil {
		return CheckoutPayResult{}, err
	}
	type grpOrder struct {
		id     int64
		public string
		total  int64
		status string
		kind   string
	}
	orders := []grpOrder{}
	for rows.Next() {
		var o grpOrder
		if err := rows.Scan(&o.id, &o.public, &o.total, &o.status, &o.kind); err != nil {
			rows.Close()
			return CheckoutPayResult{}, err
		}
		orders = append(orders, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return CheckoutPayResult{}, err
	}
	if len(orders) == 0 {
		return CheckoutPayResult{}, fmt.Errorf("该批次没有订单")
	}
	var sum int64
	for _, o := range orders {
		if o.status == "unpaid" {
			sum += o.total
		}
	}
	// 价格变动保护：与下单时的约定金额对比。不一致说明两次读取之间价格被改动过，
	// 此时宁可停下来让用户确认，也不能按旧价静默扣款。
	if sum != agreedTotal {
		if _, err := tx.Exec(ctx, `UPDATE checkout_groups SET total_cents=$2 WHERE id=$1`, groupID, sum); err != nil {
			return CheckoutPayResult{}, err
		}
		return CheckoutPayResult{}, fmt.Errorf("订单金额已变化（约定 %d 分，当前 %d 分），请确认后重新支付", agreedTotal, sum)
	}

	// 整批一次扣款。余额不足时这里失败，下面任何订单都不会被标记已付。
	if err := debitWalletTx(ctx, tx, userID, currency, sum, "checkout", groupPublicID, idempotencyKey); err != nil {
		return CheckoutPayResult{}, err
	}

	// 逐单在**同一个事务**里走与单品付款完全相同的收尾逻辑。
	//
	// 这是整个合并结算的关键：不能再调用会自己开事务的 payOrderWithWalletOnce，
	// 那样失败的订单不会跟着一起回滚，会留下「付了 3 单、第 4 单失败」的半成品状态。
	for _, o := range orders {
		if o.status != "unpaid" {
			continue
		}
		settled, err := s.settlePaidOrderInTx(ctx, tx, userID, o.id, o.public, o.kind, currency, o.total, idempotencyKey+"-"+o.public)
		if err != nil {
			return CheckoutPayResult{}, err
		}
		res.ServiceIDs = append(res.ServiceIDs, settled.ServiceIDs...)
	}
	if _, err := tx.Exec(ctx, `UPDATE checkout_groups SET status='paid',paid_at=now() WHERE id=$1`, groupID); err != nil {
		return CheckoutPayResult{}, err
	}
	if err := collectGroupOrders(ctx, tx, groupID, &res); err != nil {
		return CheckoutPayResult{}, err
	}
	res.TotalCents = sum
	if err := tx.Commit(ctx); err != nil {
		return CheckoutPayResult{}, err
	}
	return res, nil
}

// collectGroupOrders 把批次里的订单号与服务号填进结果。
func collectGroupOrders(ctx context.Context, tx pgx.Tx, groupID int64, res *CheckoutPayResult) error {
	res.OrderIDs = []string{}
	rows, err := tx.Query(ctx, `SELECT public_id::text FROM orders WHERE checkout_group_id=$1 ORDER BY id`, groupID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		res.OrderIDs = append(res.OrderIDs, id)
	}
	rows.Close()
	return rows.Err()
}
