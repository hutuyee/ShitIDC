package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hutuyee/ShitIDC/internal/model"
)

var ErrNotFound = errors.New("not found")
var ErrInsufficientBalance = errors.New("insufficient balance")
var ErrInvalidState = errors.New("invalid state")

type Store struct {
	DB *pgxpool.Pool
	// MasterKey 用于不可逆指纹（实名证件号、手机号去重）。
	// 为空时依赖它的功能会明确报错，而不是退化成「存明文」。
	MasterKey []byte
}

func New(db *pgxpool.Pool) *Store { return &Store{DB: db} }

// WithMasterKey 设置主密钥并返回自身，便于链式构造。
func (s *Store) WithMasterKey(master []byte) *Store {
	s.MasterKey = master
	return s
}

func (s *Store) CreateUser(ctx context.Context, email, passwordHash string, emailVerified bool) (model.User, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	var u model.User
	if err := tx.QueryRow(ctx, `INSERT INTO users(email,email_verified) VALUES(lower($1),$2) RETURNING id,public_id::text,email,status,email_verified,created_at`, email, emailVerified).Scan(&u.ID, &u.PublicID, &u.Email, &u.Status, &u.EmailVerified, &u.CreatedAt); err != nil {
		return model.User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_security(user_id,password_hash) VALUES($1,$2)`, u.ID, passwordHash); err != nil {
		return model.User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,currency) VALUES($1,'CNY')`, u.ID); err != nil {
		return model.User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) SELECT $1,id FROM roles WHERE name='customer'`, u.ID); err != nil {
		return model.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.User{}, err
	}
	return u, nil
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (model.User, string, error) {
	var u model.User
	var hash string
	err := s.DB.QueryRow(ctx, `SELECT u.id,u.public_id::text,u.email,u.status,u.email_verified,u.created_at,us.password_hash FROM users u JOIN user_security us ON us.user_id=u.id WHERE u.email=lower($1) AND u.deleted_at IS NULL`, email).Scan(&u.ID, &u.PublicID, &u.Email, &u.Status, &u.EmailVerified, &u.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, "", ErrNotFound
	}
	return u, hash, err
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (model.User, error) {
	var u model.User
	err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,email,status,email_verified,created_at FROM users WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&u.ID, &u.PublicID, &u.Email, &u.Status, &u.EmailVerified, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) GetUserByPublicID(ctx context.Context, publicID string) (model.User, error) {
	var u model.User
	err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,email,status,email_verified,created_at FROM users WHERE public_id=$1 AND deleted_at IS NULL`, publicID).Scan(&u.ID, &u.PublicID, &u.Email, &u.Status, &u.EmailVerified, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) EnsureAdmin(ctx context.Context, email, passwordHash string) error {
	if email == "" || passwordHash == "" {
		return nil
	}
	var userID int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM users WHERE email=lower($1)`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		u, err := s.CreateUser(ctx, email, passwordHash, true)
		if err != nil {
			return err
		}
		userID = u.ID
	} else if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) SELECT $1,id FROM roles WHERE name='admin' ON CONFLICT DO NOTHING`, userID)
	return err
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash, csrf string, ip net.IP, ua string, expires time.Time) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO user_sessions(user_id,token_hash,csrf_token,ip,user_agent,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, userID, tokenHash, csrf, ip, ua, expires)
	return err
}

func (s *Store) SessionUser(ctx context.Context, tokenHash string) (model.User, string, error) {
	var u model.User
	var csrf string
	err := s.DB.QueryRow(ctx, `SELECT u.id,u.public_id::text,u.email,u.status,u.email_verified,u.created_at,s.csrf_token FROM user_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND u.status='active' AND u.deleted_at IS NULL`, tokenHash).Scan(&u.ID, &u.PublicID, &u.Email, &u.Status, &u.EmailVerified, &u.CreatedAt, &csrf)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, "", ErrNotFound
	}
	if err == nil {
		_, _ = s.DB.Exec(ctx, `UPDATE user_sessions SET last_seen_at=now() WHERE token_hash=$1`, tokenHash)
	}
	return u, csrf, err
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.DB.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, tokenHash)
	return err
}

func (s *Store) Permissions(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT p.name FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id JOIN permissions p ON p.id=rp.permission_id WHERE ur.user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (s *Store) ListProducts(ctx context.Context) ([]model.Product, error) {
	rows, err := s.DB.Query(ctx, `SELECT p.id,p.public_id::text,p.name,p.description,coalesce(pr.public_id::text,''),coalesce(pr.name,''),p.provider_type,p.active,pp.amount_cents,pp.currency,pp.billing_cycle,coalesce(g.public_id::text,''),coalesce(g.name,''),p.created_at FROM products p JOIN product_prices pp ON pp.product_id=p.id AND pp.active=true LEFT JOIN providers pr ON pr.id=p.provider_id LEFT JOIN product_groups g ON g.id=p.group_id WHERE p.active=true AND p.deleted_at IS NULL ORDER BY coalesce(g.sort_weight,0) DESC, p.sort_weight DESC, p.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Product{}
	for rows.Next() {
		var v model.Product
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.Description, &v.ProviderID, &v.ProviderName, &v.ProviderType, &v.Active, &v.PriceCents, &v.Currency, &v.BillingCycle, &v.GroupID, &v.GroupName, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetProductPrice(ctx context.Context, productID, billingCycle string) (model.Product, error) {
	var v model.Product
	err := s.DB.QueryRow(ctx, `SELECT p.id,p.public_id::text,p.name,p.description,coalesce(pr.public_id::text,''),coalesce(pr.name,''),p.provider_type,p.active,pp.amount_cents,pp.currency,pp.billing_cycle,p.created_at FROM products p JOIN product_prices pp ON pp.product_id=p.id LEFT JOIN providers pr ON pr.id=p.provider_id WHERE p.public_id=$1 AND pp.billing_cycle=$2 AND pp.active=true AND p.active=true AND p.deleted_at IS NULL`, productID, billingCycle).Scan(&v.ID, &v.PublicID, &v.Name, &v.Description, &v.ProviderID, &v.ProviderName, &v.ProviderType, &v.Active, &v.PriceCents, &v.Currency, &v.BillingCycle, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Product{}, ErrNotFound
	}
	return v, err
}

func (s *Store) CreateProduct(ctx context.Context, name, description, providerType, providerPublicID, providerRef, billingCycle, currency string, amount int64) (model.Product, error) {
	return s.CreateProductInGroup(ctx, name, description, providerType, providerPublicID, providerRef, billingCycle, currency, amount, "")
}

// CreateProductInGroup is CreateProduct with an optional storefront group.
func (s *Store) CreateProductInGroup(ctx context.Context, name, description, providerType, providerPublicID, providerRef, billingCycle, currency string, amount int64, groupPublicID string) (model.Product, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return model.Product{}, err
	}
	defer tx.Rollback(ctx)
	var providerID *int64
	var providerName string
	if providerPublicID != "" {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id,name FROM providers WHERE public_id=$1 AND active=true`, providerPublicID).Scan(&id, &providerName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return model.Product{}, ErrNotFound
			}
			return model.Product{}, err
		}
		providerID = &id
	}
	var groupID *int64
	if groupPublicID != "" {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM product_groups WHERE public_id=$1`, groupPublicID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return model.Product{}, ErrNotFound
			}
			return model.Product{}, err
		}
		groupID = &id
	}
	var p model.Product
	if err := tx.QueryRow(ctx, `INSERT INTO products(name,description,provider_id,provider_type,provider_product_ref,group_id) VALUES($1,$2,$3,$4,NULLIF($5,''),$6) RETURNING id,public_id::text,name,description,provider_type,active,created_at`, name, description, providerID, providerType, providerRef, groupID).Scan(&p.ID, &p.PublicID, &p.Name, &p.Description, &p.ProviderType, &p.Active, &p.CreatedAt); err != nil {
		return model.Product{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO product_prices(product_id,billing_cycle,currency,amount_cents) VALUES($1,$2,$3,$4)`, p.ID, billingCycle, currency, amount); err != nil {
		return model.Product{}, err
	}
	if groupID != nil {
		if err := tx.QueryRow(ctx, `SELECT public_id::text,name FROM product_groups WHERE id=$1`, *groupID).Scan(&p.GroupID, &p.GroupName); err != nil {
			return model.Product{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Product{}, err
	}
	p.ProviderID = providerPublicID
	p.ProviderName = providerName
	p.BillingCycle = billingCycle
	p.Currency = currency
	p.PriceCents = amount
	return p, nil
}

// CreateOrder prices the order server-side (§13): list price × quantity,
// then the caller's agent-group discount (代理系统), then an optional coupon
// (优惠系统) consumed atomically — all inside the serializable transaction.
// CreateOrder is the config-free entry point (kept for existing callers such as
// the magic cube compat layer and renewals).
func (s *Store) CreateOrder(ctx context.Context, userID int64, productPublicID, billingCycle string, quantity int, couponCode string) (model.Order, error) {
	return s.CreateOrderWithConfig(ctx, userID, productPublicID, billingCycle, quantity, couponCode, OrderConfigInput{})
}

// CreateOrderWithConfig places an order with configurable-option and custom-field
// selections. Everything that affects money is recomputed here on the server:
// base price from product_prices, option surcharges from ResolveConfigSelection,
// then the agent-group discount and the coupon — the client's quote is ignored.
func (s *Store) CreateOrderWithConfig(ctx context.Context, userID int64, productPublicID, billingCycle string, quantity int, couponCode string, cfgIn OrderConfigInput) (model.Order, error) {
	return s.CreateOrderInCurrency(ctx, userID, productPublicID, billingCycle, quantity, couponCode, cfgIn, "")
}

// CreateOrderInCurrency 是多币种版本：currencyIn 为空时自动按商品在该周期上的
// 可售币种解析（见 resolveOrderCurrency）。
func (s *Store) CreateOrderInCurrency(ctx context.Context, userID int64, productPublicID, billingCycle string, quantity int, couponCode string, cfgIn OrderConfigInput, currencyIn string) (model.Order, error) {
	return s.CreateOrderPostpaid(ctx, userID, productPublicID, billingCycle, quantity, couponCode, cfgIn, currencyIn, false)
}

// CreateOrderPostpaid 下后付费订单：不要求立即付款，按账期结算。
//
// 与预付费共用 createOrderInTx，所以库存占用、配置计价、组折扣、优惠码完全没有分支；
// 差别只有两处：订单记 pay_method='postpaid'，发票到期日换成用户账期。
// 下单时会占用授信额度（在事务内 FOR UPDATE 串行判断，并发下单不会超额）。
func (s *Store) CreateOrderPostpaid(ctx context.Context, userID int64, productPublicID, billingCycle string, quantity int, couponCode string, cfgIn OrderConfigInput, currencyIn string, postpaid bool) (model.Order, error) {
	var out model.Order
	err := retrySerializable(ctx, orderRetryAttempts, func() error {
		o, err := s.createOrderOnce(ctx, userID, productPublicID, billingCycle, quantity, couponCode, cfgIn, currencyIn, postpaid)
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	return out, err
}

// orderRetryAttempts 是订单事务遇到序列化冲突时的重试次数。
// 库存占用是同一商品行上的条件写入，高并发时 PostgreSQL 会中止部分事务，
// 重试是官方推荐的客户端处理方式。
const orderRetryAttempts = 6

// createOrderInTx 在一个已开启的事务里创建一笔订单。
//
// 抽出来是为了让购物车批量结算复用**完全相同**的下单逻辑：库存占用、配置项计价、
// 组折扣、专属价、优惠码、免费/试用判定。购物车如果另写一套，迟早会和单品下单产生
// 价格或库存口径上的偏差。groupID 为 0 表示不属于任何结算批次。
// postpaid 为 true 时走授信通道：不要求立即付款，账期结束后还款。
//
// 注意后付费**仍然生成正常的订单与发票**，只是发票到期日换成账期、
// 并且订单落库后直接进入开通流程。这样退款、对账、统计等既有逻辑无需分支。
func (s *Store) createOrderInTx(ctx context.Context, tx pgx.Tx, userID int64, productPublicID, billingCycle string, quantity int, couponCode string, cfgIn OrderConfigInput, currencyIn string, groupID int64, postpaidOrder bool) (model.Order, error) {
	if quantity < 1 || quantity > 100 {
		return model.Order{}, fmt.Errorf("invalid quantity")
	}
	var p model.Product
	var providerRef *string
	var providerID *int64
	// 先确定币种（多币种独立定价），再按 (商品, 周期, 币种) 取价——
	// 同一个商品在不同币种下可以是完全独立的价格，不能用汇率去推算。
	orderCurrency, err := resolveOrderCurrency(ctx, tx, productPublicID, billingCycle, currencyIn)
	if err != nil {
		return model.Order{}, err
	}
	err = tx.QueryRow(ctx, `SELECT p.id,p.public_id::text,p.name,p.description,p.provider_type,p.provider_id,p.provider_product_ref,p.active,pp.amount_cents,pp.currency,pp.billing_cycle,p.created_at,p.pay_type,p.trial_days,p.trial_price_cents,p.auto_terminate_days FROM products p JOIN product_prices pp ON pp.product_id=p.id WHERE p.public_id=$1 AND pp.billing_cycle=$2 AND pp.currency=$3 AND p.active=true AND pp.active=true AND p.deleted_at IS NULL`, productPublicID, billingCycle, orderCurrency).Scan(&p.ID, &p.PublicID, &p.Name, &p.Description, &p.ProviderType, &providerID, &providerRef, &p.Active, &p.PriceCents, &p.Currency, &p.BillingCycle, &p.CreatedAt, &p.PayType, &p.TrialDays, &p.TrialPriceCents, &p.AutoTerminateDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Order{}, ErrNotFound
	}
	if err != nil {
		return model.Order{}, err
	}
	// 计费类型约束：免费商品价格必须为 0；试用商品用试用价；一次性不按量。
	switch p.PayType {
	case PayTypeFree:
		p.PriceCents = 0
	case PayTypeTrial:
		if p.TrialDays <= 0 {
			return model.Order{}, fmt.Errorf("该试用商品未配置试用天数")
		}
		p.PriceCents = p.TrialPriceCents
		if quantity != 1 {
			return model.Order{}, fmt.Errorf("试用商品一次只能开通一个")
		}
	case PayTypeOneTime:
		if quantity != 1 {
			return model.Order{}, fmt.Errorf("一次性商品一次只能购买一个")
		}
	}
	// 库存 / 单次数量 / 单客户限购
	if err := checkStockAndQtyTx(ctx, tx, p.ID, userID, quantity); err != nil {
		return model.Order{}, err
	}
	// 配置项与自定义字段：校验 + 服务端计价
	cfg, selectionsJSON, fieldsJSON, err := s.resolveConfigTx(ctx, tx, productPublicID, cfgIn)
	if err != nil {
		return model.Order{}, err
	}
	// 定价：先算出「每件」的最终单价，再乘数量。
	//
	// 客户组按产品差异定价（魔方 shd_user_product_bates）：
	//   组专属固定价 > 标价 + 组折扣
	// 专属价**不再叠加组折扣**——固定价就是固定价，否则运营很难解释
	// 「为什么标 50 元最后收了 45 元」。
	//
	// 注意 discount 只作用于商品本身，不作用于配置项加价与初装费：
	// 那两项是成本项，打折卖会亏。
	groupPercent, err := s.userGroupDiscountTx(ctx, tx, userID)
	if err != nil {
		return model.Order{}, err
	}
	// 无论有没有组折扣都要查一次专属价：专属价可能挂在 discount_percent=0 的组上。
	overrideCents := int64(-1)
	if ov, oerr := s.userProductOverrideTx(ctx, tx, userID, p.ID, billingCycle, orderCurrency); oerr == nil {
		overrideCents = ov
	} else if !errors.Is(oerr, pgx.ErrNoRows) {
		return model.Order{}, oerr
	}
	unitQuote := ApplyGroupPrice(p.PriceCents, groupPercent, overrideCents)

	subtotal := unitQuote.UnitCents * int64(quantity)
	// 配置加价按“每件”计；初装费一次性，不随数量放大。
	configTotal := cfg.ConfigCents*int64(quantity) + cfg.SetupCents
	subtotal += configTotal
	// groupDiscount 只用于记账与展示（订单/发票上的「已优惠」），
	// **绝不能再从 subtotal 里减一次**——subtotal 用的是已经打过折的单价
	// （unitQuote.UnitCents），再减就是重复扣减。
	groupDiscount := unitQuote.DiscountCents * int64(quantity)
	couponDiscountCents := int64(0)
	couponID := int64(0)
	couponCode = strings.ToLower(strings.TrimSpace(couponCode))
	if couponCode != "" {
		var c model.Coupon
		var cProductIDs []string
		err = tx.QueryRow(ctx, `SELECT public_id::text,code,type,value,max_uses,max_uses_per_user,used_count,min_amount_cents,product_ids,starts_at,expires_at,active FROM coupons WHERE code=$1 FOR UPDATE`, couponCode).Scan(&c.PublicID, &c.Code, &c.Type, &c.Value, &c.MaxUses, &c.MaxUsesPerUser, &c.UsedCount, &c.MinAmountCents, &cProductIDs, &c.StartsAt, &c.ExpiresAt, &c.Active)
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Order{}, fmt.Errorf("优惠码不存在")
		}
		if err != nil {
			return model.Order{}, err
		}
		c.ProductIDs = cProductIDs
		var used int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM coupon_redemptions WHERE coupon_id=(SELECT id FROM coupons WHERE public_id=$1) AND user_id=$2`, c.PublicID, userID).Scan(&used); err != nil {
			return model.Order{}, err
		}
		base := subtotal - groupDiscount
		if base < 0 {
			base = 0
		}
		couponDiscountCents, err = couponDiscount(c, productPublicID, base, used)
		if err != nil {
			return model.Order{}, err
		}
		couponID = 1 // marker; real id resolved on insert
	}
	// subtotal 已经是「折后单价 × 数量 + 配置项加价」，组折扣已经含在里面，
	// 这里只需要再减优惠券。
	total := subtotal - couponDiscountCents
	if total < 0 {
		return model.Order{}, fmt.Errorf("订单金额异常")
	}
	var o model.Order
	// 注意：orders.coupon_code 是 NOT NULL DEFAULT ''，所以这里传空字符串而不是
	// NULLIF(...,'')——否则不使用优惠码的订单会直接撞 23502（与优惠券 product_ids
	// 是同一类“NOT NULL + 显式写 NULL”的坑）。
	payMethod := "prepaid"
	// 默认 24 小时付款窗口；后付费按用户账期，并在此刻占用授信额度。
	dueDays := 1
	if postpaidOrder {
		creditDays, cerr := s.checkPostpaidTx(ctx, tx, userID, total)
		if cerr != nil {
			return model.Order{}, cerr
		}
		payMethod = "postpaid"
		dueDays = creditDays
	}
	if err := tx.QueryRow(ctx, `INSERT INTO orders(user_id,status,kind,total_cents,currency,discount_cents,coupon_code,kind_detail,checkout_group_id,pay_method) VALUES($1,'unpaid','new',$2,$3,$4,$5,$6,NULLIF($7,0),$8) RETURNING id,public_id::text,user_id,status,kind,total_cents,currency,created_at,discount_cents,coupon_code`, userID, total, p.Currency, groupDiscount+couponDiscountCents, strings.ToLower(strings.TrimSpace(couponCode)), p.PayType, groupID, payMethod).Scan(&o.ID, &o.PublicID, &o.UserUID, &o.Status, &o.Kind, &o.TotalCents, &o.Currency, &o.CreatedAt, &o.DiscountCents, &o.CouponCode); err != nil {
		return model.Order{}, err
	}
	var itemID int64
	if err := tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,unit_price_cents,quantity,subtotal_cents,provider_id,provider_type,provider_product_ref,config_cents,setup_cents,config_selections,custom_fields)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14::jsonb) RETURNING id`, o.ID, p.ID, p.Name, billingCycle, p.PriceCents, quantity, subtotal, providerID, p.ProviderType, providerRef, cfg.ConfigCents, cfg.SetupCents, selectionsJSON, fieldsJSON).Scan(&itemID); err != nil {
		return model.Order{}, err
	}
	// 库存已在 checkStockAndQtyTx 里原子占用（条件 UPDATE），这里不再重复加。
	var invoiceID int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(order_id,user_id,status,total_cents,currency,due_at) VALUES($1,$2,'unpaid',$3,$4,now()+($5 || ' days')::interval) RETURNING id`, o.ID, userID, total, p.Currency, dueDays).Scan(&invoiceID); err != nil {
		return model.Order{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,amount_cents) VALUES($1,$2,$3)`, invoiceID, p.Name+" / "+billingCycle, total); err != nil {
		return model.Order{}, err
	}
	if couponCode != "" && couponDiscountCents > 0 {
		// Consume the coupon atomically: redemption row + global counter.
		if _, err := tx.Exec(ctx, `INSERT INTO coupon_redemptions(coupon_id,user_id,order_id,discount_cents)
SELECT id,$2,$3,$4 FROM coupons WHERE code=$1`, couponCode, userID, o.ID, couponDiscountCents); err != nil {
			return model.Order{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE coupons SET used_count=used_count+1 WHERE code=$1`, couponCode); err != nil {
			return model.Order{}, err
		}
	}
	_ = itemID
	_ = couponID
	return o, nil
}

// createOrderOnce 在独立事务里创建一笔订单（单品下单入口）。
func (s *Store) createOrderOnce(ctx context.Context, userID int64, productPublicID, billingCycle string, quantity int, couponCode string, cfgIn OrderConfigInput, currencyIn string, postpaid bool) (model.Order, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.Order{}, err
	}
	defer tx.Rollback(ctx)
	o, err := s.createOrderInTx(ctx, tx, userID, productPublicID, billingCycle, quantity, couponCode, cfgIn, currencyIn, 0, postpaid)
	if err != nil {
		return model.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Order{}, err
	}
	return o, nil
}

// 没有专属价时返回 pgx.ErrNoRows。
func (s *Store) userProductOverrideTx(ctx context.Context, tx pgx.Tx, userID, productID int64, billingCycle, currency string) (int64, error) {
	var amount int64
	err := tx.QueryRow(ctx, `SELECT upp.amount_cents
FROM user_product_prices upp
JOIN users u ON u.user_group_id = upp.group_id
WHERE u.id=$1 AND upp.product_id=$2 AND upp.billing_cycle=$3 AND upp.currency=$4`,
		userID, productID, strings.ToLower(strings.TrimSpace(billingCycle)), strings.ToUpper(strings.TrimSpace(currency))).Scan(&amount)
	return amount, err
}

// userGroupDiscountTx reads the buyer's agent-group discount inside a tx.
func (s *Store) userGroupDiscountTx(ctx context.Context, tx pgx.Tx, userID int64) (int, error) {
	var discount int
	err := tx.QueryRow(ctx, `SELECT coalesce(ug.discount_percent,0) FROM users u LEFT JOIN user_groups ug ON ug.id=u.user_group_id WHERE u.id=$1`, userID).Scan(&discount)
	return discount, err
}

func (s *Store) ListInvoices(ctx context.Context, userID int64) ([]model.Invoice, error) {
	rows, err := s.DB.Query(ctx, `SELECT i.public_id::text,o.public_id::text,i.status,i.total_cents,i.currency,i.due_at,i.created_at FROM invoices i JOIN orders o ON o.id=i.order_id WHERE i.user_id=$1 ORDER BY i.created_at DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Invoice{}
	for rows.Next() {
		var v model.Invoice
		if err := rows.Scan(&v.PublicID, &v.OrderID, &v.Status, &v.TotalCents, &v.Currency, &v.DueAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PayResult reports what a successful wallet payment created: new service
// ids to provision, or the renewed service id for the provider renew call.
type PayResult struct {
	ServiceIDs     []string
	RenewServiceID string
}

func (s *Store) PayOrderWithWallet(ctx context.Context, userID int64, orderPublicID, idempotency string) (PayResult, error) {
	if idempotency == "" {
		idempotency = uuid.NewString()
	}
	// 钱包扣款同样是 SERIALIZABLE：并发支付同一钱包时会用 40001 互相中止，
	// 这里自动重试，调用方不需要自己写重试循环。
	var out PayResult
	err := retrySerializable(ctx, orderRetryAttempts, func() error {
		res, err := s.payOrderWithWalletOnce(ctx, userID, orderPublicID, idempotency)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

// payOrderWithWalletOnce 是单品余额支付：独立事务 + 独立扣款。
// 合并结算请用 payCheckoutGroupOnce（它复用下面的 settlePaidOrderInTx）。
func (s *Store) payOrderWithWalletOnce(ctx context.Context, userID int64, orderPublicID, idempotency string) (PayResult, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PayResult{}, err
	}
	defer tx.Rollback(ctx)
	var orderID, total int64
	var status, currency, kind string
	err = tx.QueryRow(ctx, `SELECT id,total_cents,status,currency,kind FROM orders WHERE public_id=$1 AND user_id=$2 FOR UPDATE`, orderPublicID, userID).Scan(&orderID, &total, &status, &currency, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return PayResult{}, ErrNotFound
	}
	if err != nil {
		return PayResult{}, err
	}
	if status == "processing" || status == "paid" {
		rows, qerr := tx.Query(ctx, `SELECT public_id::text FROM services WHERE order_id=$1 AND status IN ('pending','failed','provisioning')`, orderID)
		if qerr != nil {
			return PayResult{}, qerr
		}
		retryIDs := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return PayResult{}, err
			}
			retryIDs = append(retryIDs, id)
		}
		rows.Close()
		return PayResult{ServiceIDs: retryIDs}, rows.Err()
	}
	if status == "completed" {
		return PayResult{ServiceIDs: []string{}}, nil
	}
	if status != "unpaid" {
		return PayResult{}, ErrInvalidState
	}
	// 单品付款：这里自己扣一次余额。
	if err := debitWalletTx(ctx, tx, userID, currency, total, "order", orderPublicID, idempotency); err != nil {
		return PayResult{}, err
	}
	res, err := s.settlePaidOrderInTx(ctx, tx, userID, orderID, orderPublicID, kind, currency, total, idempotency)
	if err != nil {
		return PayResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PayResult{}, err
	}
	return res, nil
}

// settlePaidOrderInTx 在**调用方的事务里**完成一笔已扣款订单的收尾：
// 记支付流水、核销发票、续费或建服务。余额扣减由调用方负责。
//
// 参数 total/currency 由调用方提供：单品付款取订单金额，合并结算取分摊金额。
func (s *Store) settlePaidOrderInTx(ctx context.Context, tx pgx.Tx, userID, orderID int64, orderPublicID, kind, currency string, total int64, idempotency string) (PayResult, error) {
	// 余额支付也要落一条 payments 记录：退款/对账/统计都以 payments 为准，
	// 缺少这一行会让 RefundOrder 找不到已完成的支付而直接判定「不可退款」。
	// transaction_id 用幂等键派生，重复支付同一订单不会冲突。
	walletTxn := "W" + strings.ReplaceAll(orderPublicID, "-", "")
	if idempotency != "" {
		walletTxn = "W" + strings.ReplaceAll(idempotency, "-", "")
	}
	if len(walletTxn) > 120 {
		walletTxn = walletTxn[:120]
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payments(user_id,order_id,method,transaction_id,amount_cents,currency,status,raw_payload)
VALUES($1,$2,'wallet',$3,$4,$5,'completed',$6::jsonb)
ON CONFLICT (transaction_id) DO NOTHING`, userID, orderID, walletTxn, total, currency, `{"kind":"order","provider":"wallet","method":"wallet"}`); err != nil {
		return PayResult{}, err
	}
	var invoiceID int64
	if err := tx.QueryRow(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE order_id=$1 AND status='unpaid' RETURNING id`, orderID).Scan(&invoiceID); err != nil {
		return PayResult{}, err
	}
	if kind == "renewal" {
		// 续费订单延长已有服务，不新开实例。
		renewID, renewed, err := s.ExtendServiceOnRenewalPayment(ctx, tx, orderID)
		if err != nil {
			return PayResult{}, err
		}
		if !renewed {
			return PayResult{}, ErrInvalidState
		}
		return PayResult{RenewServiceID: renewID}, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='processing',paid_at=now(),updated_at=now() WHERE id=$1`, orderID); err != nil {
		return PayResult{}, err
	}
	rows, err := tx.Query(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,status,provider_id,provider_type,provider_product_ref,expires_at)
SELECT $1,oi.order_id,oi.id,oi.product_id,'pending',oi.provider_id,oi.provider_type,oi.provider_product_ref,
now()+CASE oi.billing_cycle WHEN 'quarterly' THEN interval '3 months' WHEN 'semiannually' THEN interval '6 months' WHEN 'yearly' THEN interval '1 year' ELSE interval '1 month' END
FROM order_items oi WHERE oi.order_id=$2 RETURNING public_id::text`, userID, orderID)
	if err != nil {
		return PayResult{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return PayResult{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PayResult{}, err
	}
	return PayResult{ServiceIDs: ids}, nil
}
func (s *Store) Wallet(ctx context.Context, userID int64, currency string) (model.Wallet, error) {
	var w model.Wallet
	err := s.DB.QueryRow(ctx, `SELECT balance_cents,currency FROM wallet_accounts WHERE user_id=$1 AND currency=$2`, userID, currency).Scan(&w.BalanceCents, &w.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Wallet{}, ErrNotFound
	}
	return w, err
}

func (s *Store) WalletTransactions(ctx context.Context, userID int64, currency string) ([]model.WalletTransaction, error) {
	rows, err := s.DB.Query(ctx, `SELECT wt.public_id::text,wt.type,wt.amount_cents,wt.balance_before_cents,wt.balance_after_cents,wt.currency,wt.reference_type,wt.reference_id,wt.description,wt.created_at FROM wallet_transactions wt JOIN wallet_accounts wa ON wa.id=wt.account_id WHERE wa.user_id=$1 AND wa.currency=$2 ORDER BY wt.created_at DESC LIMIT 200`, userID, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.WalletTransaction{}
	for rows.Next() {
		var v model.WalletTransaction
		if err := rows.Scan(&v.PublicID, &v.Type, &v.AmountCents, &v.BalanceBefore, &v.BalanceAfter, &v.Currency, &v.ReferenceType, &v.ReferenceID, &v.Description, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AdjustWallet(ctx context.Context, actorID, targetUserID int64, currency string, amount int64, reason, requestID string) error {
	if amount == 0 {
		return fmt.Errorf("amount cannot be zero")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accountID, balance int64
	if err := tx.QueryRow(ctx, `SELECT id,balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE`, targetUserID, currency).Scan(&accountID, &balance); err != nil {
		return err
	}
	after := balance + amount
	if after < 0 {
		return ErrInsufficientBalance
	}
	if _, err := tx.Exec(ctx, `UPDATE wallet_accounts SET balance_cents=$1,updated_at=now() WHERE id=$2`, after, accountID); err != nil {
		return err
	}
	ref := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key) VALUES($1,$2,$3,$4,$5,$6,'admin_adjustment',$7,$8,$9)`, accountID, map[bool]string{true: "credit", false: "debit"}[amount > 0], amount, balance, after, currency, ref, reason, "admin:"+ref); err != nil {
		return err
	}
	before, _ := json.Marshal(map[string]any{"balance_cents": balance})
	afterJSON, _ := json.Marshal(map[string]any{"balance_cents": after, "reason": reason})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,action,object_type,object_id,request_id,before_data,after_data) VALUES($1,'wallet.adjust','wallet',$2,$3,$4,$5)`, actorID, fmt.Sprint(targetUserID), requestID, before, afterJSON); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListServices(ctx context.Context, userID int64) ([]model.Service, error) {
	rows, err := s.DB.Query(ctx, `SELECT s.public_id::text,s.status,s.provider_type,coalesce(s.provider_ref,''),p.name,
coalesce(oi.billing_cycle,''),coalesce(oi.unit_price_cents,0),coalesce(o.currency,'CNY'),s.expires_at,s.created_at
FROM services s
JOIN products p ON p.id=s.product_id
LEFT JOIN order_items oi ON oi.id=s.order_item_id
LEFT JOIN orders o ON o.id=s.order_id
WHERE s.user_id=$1 AND s.status NOT IN ('terminated') ORDER BY s.created_at DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Service{}
	for rows.Next() {
		var v model.Service
		if err := rows.Scan(&v.PublicID, &v.Status, &v.ProviderType, &v.ProviderRef, &v.ProductName, &v.BillingCycle, &v.PriceCents, &v.Currency, &v.ExpiresAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ClaimServiceForProvisioning 抢占一个待开通的服务并返回开通所需的信息。
//
// 接口（provider_id）的解析顺序：
//  1. 服务行上已经指定的接口（重试或人工指派过的，必须沿用，否则会在两台机器上各开一次）
//  2. 商品绑定的接口
//  3. 商品绑定的**接口分组**——按分组策略挑一个还有容量的接口（魔方 server_groups 的等价能力）
//
// 分组挑不出来（全满或全停用）时返回 claimed=false 且 err=ErrNotFound，让调用方
// 把服务标记为待处理并提示扩容，而不是硬塞到一个已经满的接口上。
func (s *Store) ClaimServiceForProvisioning(ctx context.Context, servicePublicID string) (serviceID int64, userID int64, providerID int64, providerType, productRef string, claimed bool, err error) {
	var groupPublic *string
	err = s.DB.QueryRow(ctx, `UPDATE services s SET status='provisioning',updated_at=now() FROM products p
WHERE s.product_id=p.id AND s.public_id=$1 AND s.status IN ('pending','failed')
RETURNING s.id,s.user_id,coalesce(s.provider_id,p.provider_id,0),s.provider_type,coalesce(s.provider_product_ref,p.provider_product_ref,''),
  (SELECT g.public_id::text FROM provider_groups g WHERE g.id=p.provider_group_id AND g.active=true)`, servicePublicID).
		Scan(&serviceID, &userID, &providerID, &providerType, &productRef, &groupPublic)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, "", "", false, nil
	}
	if err != nil {
		return 0, 0, 0, "", "", false, err
	}
	// 服务/商品都没有指定具体接口，但商品绑了分组：按策略挑一个。
	if providerID == 0 && groupPublic != nil && *groupPublic != "" {
		resolved, rerr := s.ResolveProviderForGroup(ctx, *groupPublic)
		if rerr != nil {
			// 把服务放回 pending，等管理员扩容后重试，而不是留在 provisioning 卡住。
			_, _ = s.DB.Exec(ctx, `UPDATE services SET status='pending',updated_at=now() WHERE id=$1`, serviceID)
			if errors.Is(rerr, ErrNotFound) {
				return 0, 0, 0, "", "", false, fmt.Errorf("接口分组 %s 没有可用的接口（已满或已停用），请扩容后重试", *groupPublic)
			}
			return 0, 0, 0, "", "", false, rerr
		}
		providerID = resolved
		// 把选中的接口记到服务上：后续暂停/删除/续费都找同一台机器。
		if _, uerr := s.DB.Exec(ctx, `UPDATE services SET provider_id=$2,provider_group_id=(SELECT id FROM provider_groups WHERE public_id=$3),updated_at=now() WHERE id=$1`, serviceID, resolved, *groupPublic); uerr != nil {
			return 0, 0, 0, "", "", false, uerr
		}
	}
	return serviceID, userID, providerID, providerType, productRef, true, nil
}

func (s *Store) PendingServiceIDs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text FROM services WHERE status='pending' ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) MarkService(ctx context.Context, servicePublicID, status, providerRef string, payload any) error {
	b, _ := json.Marshal(payload)
	_, err := s.DB.Exec(ctx, `UPDATE services SET status=$2,provider_ref=NULLIF($3,''),provider_payload=$4::jsonb,updated_at=now() WHERE public_id=$1`, servicePublicID, status, providerRef, string(b))
	return err
}

func (s *Store) CompleteOrderIfReady(ctx context.Context, servicePublicID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE orders o SET status='completed',updated_at=now() WHERE o.id=(SELECT order_id FROM services WHERE public_id=$1) AND NOT EXISTS (SELECT 1 FROM services s2 WHERE s2.order_id=o.id AND s2.status NOT IN ('active','terminated'))`, servicePublicID)
	return err
}

func (s *Store) CreateTicket(ctx context.Context, userID int64, subject, priority, message string) (model.Ticket, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return model.Ticket{}, err
	}
	defer tx.Rollback(ctx)
	var t model.Ticket
	if err := tx.QueryRow(ctx, `INSERT INTO tickets(user_id,subject,priority,last_reply_at,last_reply_is_staff) VALUES($1,$2,$3,now(),FALSE)
RETURNING public_id::text,subject,status,priority,created_at`, userID, subject, priority).Scan(&t.PublicID, &t.Subject, &t.Status, &t.Priority, &t.CreatedAt); err != nil {
		return model.Ticket{}, err
	}
	var ticketID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM tickets WHERE public_id=$1`, t.PublicID).Scan(&ticketID); err != nil {
		return model.Ticket{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_messages(ticket_id,user_id,body) VALUES($1,$2,$3)`, ticketID, userID, message); err != nil {
		return model.Ticket{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Ticket{}, err
	}
	return t, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name, keyID, secretHash string, scopes []string, expiresAt *time.Time) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO api_tokens(user_id,name,key_id,secret_hash,scopes,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, userID, name, keyID, secretHash, scopes, expiresAt)
	return err
}

func (s *Store) AuthenticateAPIToken(ctx context.Context, keyID, secretHash, ip string) (model.User, []string, error) {
	var u model.User
	var scopes []string
	var allow []string
	err := s.DB.QueryRow(ctx, `SELECT u.id,u.public_id::text,u.email,u.status,u.email_verified,u.created_at,t.scopes,t.ip_allowlist::text[] FROM api_tokens t JOIN users u ON u.id=t.user_id WHERE t.key_id=$1 AND t.secret_hash=$2 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at>now()) AND u.status='active'`, keyID, secretHash).Scan(&u.ID, &u.PublicID, &u.Email, &u.Status, &u.EmailVerified, &u.CreatedAt, &scopes, &allow)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, nil, ErrNotFound
	}
	if err != nil {
		return model.User{}, nil, err
	}
	if len(allow) > 0 {
		parsed := net.ParseIP(ip)
		ok := false
		for _, cidr := range allow {
			_, n, e := net.ParseCIDR(cidr)
			if e == nil && n.Contains(parsed) {
				ok = true
				break
			}
		}
		if !ok {
			return model.User{}, nil, ErrNotFound
		}
	}
	_, _ = s.DB.Exec(ctx, `UPDATE api_tokens SET last_used_at=now(),last_used_ip=$2 WHERE key_id=$1`, keyID, ip)
	return u, scopes, nil
}

func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,key_id,scopes,expires_at,last_used_at,created_at FROM api_tokens WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, key string
		var scopes []string
		var exp, last *time.Time
		var created time.Time
		if err := rows.Scan(&id, &name, &key, &scopes, &exp, &last, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "key_id": key, "scopes": scopes, "expires_at": exp, "last_used_at": last, "created_at": created})
	}
	return out, rows.Err()
}

func (s *Store) Audit(ctx context.Context, actorID int64, action, objectType, objectID, requestID, ip, ua string, before, after any) error {
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	// before_data / after_data 是 jsonb：必须传 string 且显式 ::jsonb，
	// 否则 pgx 会把 []byte 当 bytea 编码而报 22P02。
	_, err := s.DB.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,action,object_type,object_id,request_id,ip,user_agent,before_data,after_data) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::inet,$7,$8::jsonb,$9::jsonb)`, actor, action, objectType, objectID, requestID, ip, ua, string(b1), string(b2))
	return err
}

func (s *Store) RevokeAPIToken(ctx context.Context, userID int64, publicID string) error {
	cmd, err := s.DB.Exec(ctx, `UPDATE api_tokens SET revoked_at=now() WHERE user_id=$1 AND public_id=$2 AND revoked_at IS NULL`, userID, publicID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT id,coalesce(actor_user_id,0),action,object_type,object_id,coalesce(request_id,''),coalesce(ip::text,''),created_at FROM audit_logs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, actorID int64
		var action, objectType, objectID, requestID, ip string
		var createdAt time.Time
		if err := rows.Scan(&id, &actorID, &action, &objectType, &objectID, &requestID, &ip, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "actor_user_id": actorID, "action": action, "object_type": objectType, "object_id": objectID, "request_id": requestID, "ip": ip, "created_at": createdAt})
	}
	return out, rows.Err()
}

func (s *Store) RetryFailedService(ctx context.Context, publicID string) (bool, error) {
	cmd, err := s.DB.Exec(ctx, `UPDATE services SET status='pending',updated_at=now() WHERE public_id=$1 AND status='failed'`, publicID)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() == 1, nil
}

func (s *Store) CreateProvider(ctx context.Context, name, providerType, baseURL, username, secretEncrypted string, config map[string]any) (model.Provider, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(baseURL) == "" {
		return model.Provider{}, fmt.Errorf("provider name and base_url are required")
	}
	cfg, err := json.Marshal(config)
	if err != nil {
		return model.Provider{}, err
	}
	var v model.Provider
	var cfgBytes []byte
	err = s.DB.QueryRow(ctx, `INSERT INTO providers(name,provider_type,base_url,username,secret_encrypted,config) VALUES($1,$2,$3,$4,$5,$6::jsonb) RETURNING id,public_id::text,name,provider_type,coalesce(base_url,''),coalesce(username,''),config,active,status,last_checked_at,last_sync_at,last_error,created_at,updated_at`, name, providerType, baseURL, username, secretEncrypted, string(cfg)).Scan(&v.ID, &v.PublicID, &v.Name, &v.ProviderType, &v.BaseURL, &v.Username, &cfgBytes, &v.Active, &v.Status, &v.LastCheckedAt, &v.LastSyncAt, &v.LastError, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return model.Provider{}, err
	}
	_ = json.Unmarshal(cfgBytes, &v.Config)
	return v, nil
}

func (s *Store) UpdateProvider(ctx context.Context, publicID, name, baseURL, username, secretEncrypted string, config map[string]any) (model.Provider, error) {
	cfg, err := json.Marshal(config)
	if err != nil {
		return model.Provider{}, err
	}
	var v model.Provider
	var cfgBytes []byte
	err = s.DB.QueryRow(ctx, `UPDATE providers SET name=$2,base_url=$3,username=$4,secret_encrypted=CASE WHEN NULLIF($5,'') IS NULL THEN secret_encrypted ELSE $5 END,config=$6,status='unknown',last_error='',updated_at=now() WHERE public_id=$1 RETURNING id,public_id::text,name,provider_type,coalesce(base_url,''),coalesce(username,''),config,active,status,last_checked_at,last_sync_at,last_error,created_at,updated_at`, publicID, name, baseURL, username, secretEncrypted, cfg).Scan(&v.ID, &v.PublicID, &v.Name, &v.ProviderType, &v.BaseURL, &v.Username, &cfgBytes, &v.Active, &v.Status, &v.LastCheckedAt, &v.LastSyncAt, &v.LastError, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Provider{}, ErrNotFound
	}
	if err != nil {
		return model.Provider{}, err
	}
	_ = json.Unmarshal(cfgBytes, &v.Config)
	return v, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]model.Provider, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,public_id::text,name,provider_type,coalesce(base_url,''),coalesce(username,''),config,active,status,last_checked_at,last_sync_at,last_error,created_at,updated_at FROM providers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Provider{}
	for rows.Next() {
		var v model.Provider
		var cfg []byte
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.ProviderType, &v.BaseURL, &v.Username, &cfg, &v.Active, &v.Status, &v.LastCheckedAt, &v.LastSyncAt, &v.LastError, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cfg, &v.Config)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetProviderCredentials(ctx context.Context, publicID string) (model.Provider, string, error) {
	var v model.Provider
	var cfg []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,name,provider_type,coalesce(base_url,''),coalesce(username,''),coalesce(secret_encrypted,''),config,active,status,last_checked_at,last_sync_at,last_error,created_at,updated_at FROM providers WHERE public_id=$1`, publicID).Scan(&v.ID, &v.PublicID, &v.Name, &v.ProviderType, &v.BaseURL, &v.Username, &secret, &cfg, &v.Active, &v.Status, &v.LastCheckedAt, &v.LastSyncAt, &v.LastError, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Provider{}, "", ErrNotFound
	}
	if err != nil {
		return model.Provider{}, "", err
	}
	_ = json.Unmarshal(cfg, &v.Config)
	return v, secret, nil
}

func (s *Store) GetProviderCredentialsByID(ctx context.Context, id int64) (model.Provider, string, error) {
	var v model.Provider
	var cfg []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,name,provider_type,coalesce(base_url,''),coalesce(username,''),coalesce(secret_encrypted,''),config,active,status,last_checked_at,last_sync_at,last_error,created_at,updated_at FROM providers WHERE id=$1`, id).Scan(&v.ID, &v.PublicID, &v.Name, &v.ProviderType, &v.BaseURL, &v.Username, &secret, &cfg, &v.Active, &v.Status, &v.LastCheckedAt, &v.LastSyncAt, &v.LastError, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Provider{}, "", ErrNotFound
	}
	if err != nil {
		return model.Provider{}, "", err
	}
	_ = json.Unmarshal(cfg, &v.Config)
	return v, secret, nil
}

func (s *Store) UpdateProviderCheck(ctx context.Context, id int64, ok bool, message string) error {
	status := "online"
	if !ok {
		status = "error"
	}
	_, err := s.DB.Exec(ctx, `UPDATE providers SET status=$2,last_checked_at=now(),last_error=$3,updated_at=now() WHERE id=$1`, id, status, message)
	return err
}

func (s *Store) SaveProviderProducts(ctx context.Context, providerID int64, products []model.ProviderProduct) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range products {
		raw, err := json.Marshal(p.RawPayload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO provider_products(provider_id,upstream_product_id,name,description,price_cents,currency,billing_cycle,raw_payload,synced_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,now()) ON CONFLICT(provider_id,upstream_product_id) DO UPDATE SET name=excluded.name,description=excluded.description,price_cents=excluded.price_cents,currency=excluded.currency,billing_cycle=excluded.billing_cycle,raw_payload=excluded.raw_payload,synced_at=now()`, providerID, p.UpstreamProductID, p.Name, p.Description, p.PriceCents, p.Currency, p.BillingCycle, raw); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE providers SET last_sync_at=now(),status='online',last_error='',updated_at=now() WHERE id=$1`, providerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListProviderProducts(ctx context.Context, providerPublicID string) ([]model.ProviderProduct, error) {
	rows, err := s.DB.Query(ctx, `SELECT pp.id,pp.provider_id,pp.upstream_product_id,pp.name,pp.description,pp.price_cents,pp.currency,pp.billing_cycle,pp.raw_payload,pp.synced_at FROM provider_products pp JOIN providers p ON p.id=pp.provider_id WHERE p.public_id=$1 ORDER BY pp.name,pp.upstream_product_id`, providerPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProviderProduct{}
	for rows.Next() {
		var v model.ProviderProduct
		var raw []byte
		if err := rows.Scan(&v.ID, &v.ProviderID, &v.UpstreamProductID, &v.Name, &v.Description, &v.PriceCents, &v.Currency, &v.BillingCycle, &raw, &v.SyncedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &v.RawPayload)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ImportProviderProduct(ctx context.Context, providerPublicID, upstreamID, name, description, billingCycle, currency string, amount int64) (model.Product, error) {
	var providerID int64
	var providerType, providerName string
	if err := s.DB.QueryRow(ctx, `SELECT id,provider_type,name FROM providers WHERE public_id=$1 AND active=true`, providerPublicID).Scan(&providerID, &providerType, &providerName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Product{}, ErrNotFound
		}
		return model.Product{}, err
	}
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE provider_id=$1 AND provider_product_ref=$2 AND deleted_at IS NULL)`, providerID, upstreamID).Scan(&exists); err != nil {
		return model.Product{}, err
	}
	if exists {
		return model.Product{}, fmt.Errorf("upstream product is already imported")
	}
	// 名称/描述/周期/币种/价格任何一项没给，就用同步下来的上游资料补齐。
	if name == "" || description == "" || billingCycle == "" || currency == "" || amount <= 0 {
		var stored model.ProviderProduct
		err := s.DB.QueryRow(ctx, `SELECT name,description,price_cents,currency,billing_cycle FROM provider_products WHERE provider_id=$1 AND upstream_product_id=$2`, providerID, upstreamID).Scan(&stored.Name, &stored.Description, &stored.PriceCents, &stored.Currency, &stored.BillingCycle)
		if err != nil {
			return model.Product{}, err
		}
		if name == "" {
			name = stored.Name
		}
		if description == "" {
			description = stored.Description
		}
		if amount <= 0 {
			amount = stored.PriceCents
		}
		if currency == "" {
			currency = stored.Currency
		}
		if billingCycle == "" {
			billingCycle = stored.BillingCycle
		}
	}
	p, err := s.CreateProduct(ctx, name, description, providerType, providerPublicID, upstreamID, billingCycle, currency, amount)
	if err != nil {
		return model.Product{}, err
	}
	// 记住来源，避免同一个上游商品被重复导入。
	if _, err := s.DB.Exec(ctx, `UPDATE products SET provider_id=$2,provider_product_ref=$3,updated_at=now() WHERE id=$1`, p.ID, providerID, upstreamID); err != nil {
		return model.Product{}, err
	}
	return p, nil
}

// UpdateProviderConfigOnly 只更新 providers.config（规格编辑用），
// 不动名称/地址/密钥，也不重置连接状态。
func (s *Store) UpdateProviderConfigOnly(ctx context.Context, publicID string, config map[string]any) error {
	cfg, err := json.Marshal(config)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE providers SET config=$2::jsonb,updated_at=now() WHERE public_id=$1`, publicID, string(cfg))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
