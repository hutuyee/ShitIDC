package store

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// V2 features: coupons (优惠系统), agent user groups (代理系统), referral
// commissions (推广系统), gateway refunds (自动退款), financial statistics
// (财务统计), notifications, attachments, mail templates, currencies and the
// extension registry.

// ---- coupons (优惠系统) ----

func (s *Store) ListCoupons(ctx context.Context, limit int) ([]model.Coupon, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,code,type,value,max_uses,max_uses_per_user,used_count,min_amount_cents,product_ids,starts_at,expires_at,active,created_at FROM coupons ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return scanCoupons(rows)
}

// normalizeProductIDs turns "no product scope" into an empty array. pgx encodes
// a nil slice as SQL NULL, and coupons.product_ids is NOT NULL DEFAULT '{}', so
// an unscoped coupon used to fail with SQLSTATE 23502 instead of becoming a
// site-wide code. Blank entries are dropped so a stray "" never widens or
// breaks the scope check in couponDiscount.
func normalizeProductIDs(productIDs []string) []string {
	out := make([]string, 0, len(productIDs))
	for _, id := range productIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s *Store) CreateCoupon(ctx context.Context, code, typ string, value int64, maxUses *int, maxUsesPerUser int, minAmountCents int64, productIDs []string, startsAt, expiresAt *time.Time, active bool) (model.Coupon, error) {
	var v model.Coupon
	scope := normalizeProductIDs(productIDs)
	// coupons.product_ids 是 uuid[]，而 scope 是 []string（pgx 会当 text[] 发出去）。
	// 不加显式转换就会撞 42804：column "product_ids" is of type uuid[] but expression is of type text。
	// 这里不能用 coalesce($7,'{}')——'{}' 是未知类型字面量，同样无法推断成 uuid[]；
	// 用 NULLIF 把空数组变成 NULL，再用 $8::uuid[] 统一转换，两端类型都明确。
	err := s.DB.QueryRow(ctx, `INSERT INTO coupons(code,type,value,max_uses,max_uses_per_user,min_amount_cents,product_ids,starts_at,expires_at,active)
VALUES(lower($1),$2,$3,$4,$5,$6,coalesce(NULLIF($7::text[],'{}')::uuid[],'{}'::uuid[]),$8,$9,$10) RETURNING public_id::text,code,type,value,max_uses,max_uses_per_user,used_count,min_amount_cents,product_ids,starts_at,expires_at,active,created_at`,
		strings.TrimSpace(code), typ, value, maxUses, maxUsesPerUser, minAmountCents, scope, startsAt, expiresAt, active).Scan(&v.PublicID, &v.Code, &v.Type, &v.Value, &v.MaxUses, &v.MaxUsesPerUser, &v.UsedCount, &v.MinAmountCents, &v.ProductIDs, &v.StartsAt, &v.ExpiresAt, &v.Active, &v.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return model.Coupon{}, fmt.Errorf("优惠码已存在")
		}
		return model.Coupon{}, err
	}
	return v, nil
}

func (s *Store) UpdateCoupon(ctx context.Context, publicID string, maxUses *int, minAmountCents *int64, expiresAt *time.Time, active *bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE coupons SET
max_uses=coalesce($2,max_uses),
min_amount_cents=coalesce($3,min_amount_cents),
expires_at=coalesce($4,expires_at),
active=coalesce($5,active)
WHERE public_id=$1`, publicID, maxUses, minAmountCents, expiresAt, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteCoupon(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM coupons WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ValidateCoupon previews a coupon without consuming it (order dialog).
func (s *Store) ValidateCoupon(ctx context.Context, code string, userID int64, productPublicID string, subtotalCents int64) (int64, error) {
	c, err := s.loadCoupon(ctx, code)
	if err != nil {
		return 0, err
	}
	used, err := s.couponUserUses(ctx, c.PublicID, userID)
	if err != nil {
		return 0, err
	}
	return couponDiscount(c, productPublicID, subtotalCents, used)
}

// loadCoupon fetches a coupon by code for rule checks.
func (s *Store) loadCoupon(ctx context.Context, code string) (model.Coupon, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,code,type,value,max_uses,max_uses_per_user,used_count,min_amount_cents,product_ids,starts_at,expires_at,active,created_at FROM coupons WHERE code=lower($1)`, strings.TrimSpace(code))
	if err != nil {
		return model.Coupon{}, err
	}
	list, err := scanCoupons(rows)
	if err != nil {
		return model.Coupon{}, err
	}
	if len(list) == 0 {
		return model.Coupon{}, fmt.Errorf("优惠码不存在")
	}
	return list[0], nil
}

func (s *Store) couponUserUses(ctx context.Context, couponPublicID string, userID int64) (int64, error) {
	var used int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM coupon_redemptions WHERE coupon_id=(SELECT id FROM coupons WHERE public_id=$1) AND user_id=$2`, couponPublicID, userID).Scan(&used)
	return used, err
}

// couponDiscount is the pure rule set: window, budget, scope, per-user cap.
func couponDiscount(c model.Coupon, productPublicID string, subtotal int64, userUses int64) (int64, error) {
	if !c.Active {
		return 0, fmt.Errorf("优惠码已停用")
	}
	now := time.Now()
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return 0, fmt.Errorf("优惠码尚未开始")
	}
	if c.ExpiresAt != nil && now.After(*c.ExpiresAt) {
		return 0, fmt.Errorf("优惠码已过期")
	}
	if c.MaxUses != nil && int64(*c.MaxUses) <= c.UsedCount {
		return 0, fmt.Errorf("优惠码已被领完")
	}
	if subtotal < c.MinAmountCents {
		return 0, fmt.Errorf("订单金额未达到优惠码最低消费")
	}
	if len(c.ProductIDs) > 0 {
		found := false
		for _, p := range c.ProductIDs {
			if p == productPublicID {
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("优惠码不适用于该商品")
		}
	}
	if userUses >= int64(c.MaxUsesPerUser) {
		return 0, fmt.Errorf("你已使用过该优惠码")
	}
	discount := c.Value
	if c.Type == "percent" {
		discount = subtotal * c.Value / 100
	}
	if discount > subtotal {
		discount = subtotal
	}
	return discount, nil
}

func scanCoupons(rows pgx.Rows) ([]model.Coupon, error) {
	defer rows.Close()
	out := []model.Coupon{}
	for rows.Next() {
		var v model.Coupon
		if err := rows.Scan(&v.PublicID, &v.Code, &v.Type, &v.Value, &v.MaxUses, &v.MaxUsesPerUser, &v.UsedCount, &v.MinAmountCents, &v.ProductIDs, &v.StartsAt, &v.ExpiresAt, &v.Active, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- agent user groups (代理系统) ----

func (s *Store) ListUserGroups(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT g.public_id::text,g.name,g.discount_percent,g.created_at,count(u.id) FROM user_groups g LEFT JOIN users u ON u.user_group_id=g.id GROUP BY g.id ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name string
		var discount int
		var members int64
		var created time.Time
		if err := rows.Scan(&id, &name, &discount, &created, &members); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "discount_percent": discount, "members": members, "created_at": created})
	}
	return out, rows.Err()
}

func (s *Store) CreateUserGroup(ctx context.Context, name string, discountPercent int) (map[string]any, error) {
	var id string
	var created time.Time
	err := s.DB.QueryRow(ctx, `INSERT INTO user_groups(name,discount_percent) VALUES($1,$2) RETURNING public_id::text,created_at`, name, discountPercent).Scan(&id, &created)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("用户组名称已存在")
		}
		return nil, err
	}
	return map[string]any{"id": id, "name": name, "discount_percent": discountPercent, "created_at": created}, nil
}

func (s *Store) UpdateUserGroup(ctx context.Context, publicID, name string, discountPercent int) error {
	tag, err := s.DB.Exec(ctx, `UPDATE user_groups SET name=$2,discount_percent=$3 WHERE public_id=$1`, publicID, name, discountPercent)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("用户组名称已存在")
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUserGroup(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM user_groups WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserGroup assigns (or clears, with "") a user's agent group.
func (s *Store) SetUserGroup(ctx context.Context, userPublicID, groupPublicID string) error {
	var gid *int64
	if groupPublicID != "" {
		if err := s.DB.QueryRow(ctx, `SELECT id FROM user_groups WHERE public_id=$1`, groupPublicID).Scan(&gid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE users SET user_group_id=$2,updated_at=now() WHERE public_id=$1`, userPublicID, gid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// userGroupDiscount returns the caller's group discount percent (0 = none).
func (s *Store) userGroupDiscount(ctx context.Context, userID int64) (int, error) {
	var discount int
	err := s.DB.QueryRow(ctx, `SELECT coalesce(ug.discount_percent,0) FROM users u LEFT JOIN user_groups ug ON ug.id=u.user_group_id WHERE u.id=$1`, userID).Scan(&discount)
	return discount, err
}

// ---- referral commissions (推广系统) ----

// EnsureReferralCode lazily issues the user's own invite code.
func (s *Store) EnsureReferralCode(ctx context.Context, userID int64) (string, error) {
	var code *string
	if err := s.DB.QueryRow(ctx, `SELECT referral_code FROM users WHERE id=$1`, userID).Scan(&code); err != nil {
		return "", err
	}
	if code != nil && *code != "" {
		return *code, nil
	}
	newCode := randomReferralCode()
	tag, err := s.DB.Exec(ctx, `UPDATE users SET referral_code=$2 WHERE id=$1 AND (referral_code IS NULL OR referral_code='')`, userID, newCode)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return newCode, nil
	}
	// Lost a concurrent insert race: re-read the winner.
	var existing *string
	if err := s.DB.QueryRow(ctx, `SELECT referral_code FROM users WHERE id=$1`, userID).Scan(&existing); err != nil {
		return "", err
	}
	if existing == nil {
		return "", fmt.Errorf("referral code conflict, retry")
	}
	return *existing, nil
}

func randomReferralCode() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	buf := make([]byte, 8)
	if _, err := cryptorand.Read(buf); err != nil {
		// crypto/rand failing means the process is broken; a time-based
		// fallback keeps registration working and stays unique enough.
		return fmt.Sprintf("u%x", time.Now().UnixNano())
	}
	b := make([]byte, len(buf))
	for i, v := range buf {
		b[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(b)
}

// ApplyReferral attributes a fresh registration to the invite code owner.
// Self-referral is refused; unknown codes are a no-op (no enumeration).
func (s *Store) ApplyReferral(ctx context.Context, userID int64, code string) (bool, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return false, nil
	}
	tag, err := s.DB.Exec(ctx, `UPDATE users SET referred_by=(SELECT id FROM users WHERE referral_code=$1 AND id<>$2 AND deleted_at IS NULL) WHERE id=$2 AND referred_by IS NULL`, code, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReferralInfo returns the caller's code, invited count and commissions.
func (s *Store) ReferralInfo(ctx context.Context, userID int64) (map[string]any, error) {
	code, err := s.EnsureReferralCode(ctx, userID)
	if err != nil {
		return nil, err
	}
	var invited int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE referred_by=$1`, userID).Scan(&invited); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT rc.public_id::text,u.email,rc.amount_cents,rc.currency,rc.created_at FROM referral_commissions rc JOIN users u ON u.id=rc.referee_id WHERE rc.referrer_id=$1 ORDER BY rc.id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commissions := []map[string]any{}
	for rows.Next() {
		var id, email, currency string
		var amount int64
		var created time.Time
		if err := rows.Scan(&id, &email, &amount, &currency, &created); err != nil {
			return nil, err
		}
		commissions = append(commissions, map[string]any{"id": id, "referee_email": email, "amount_cents": amount, "currency": currency, "created_at": created})
	}
	return map[string]any{"code": code, "invited": invited, "commissions": commissions}, rows.Err()
}

// PayReferralCommission credits the order's referrer once (idempotent per
// order). Called after an order payment commits; errors never break payment.
func (s *Store) PayReferralCommission(ctx context.Context, orderPublicID string, percent int) (bool, error) {
	if percent <= 0 {
		return false, nil
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var orderID, userID, total int64
	var currency string
	err = tx.QueryRow(ctx, `SELECT id,user_id,total_cents,currency FROM orders WHERE public_id=$1 AND status IN ('paid','processing','completed')`, orderPublicID).Scan(&orderID, &userID, &total, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var referrer int64
	err = tx.QueryRow(ctx, `SELECT coalesce(referred_by,0) FROM users WHERE id=$1`, userID).Scan(&referrer)
	if err != nil || referrer == 0 {
		return false, err
	}
	commission := total * int64(percent) / 100
	if commission <= 0 {
		return false, nil
	}
	var accountID, balance int64
	if err := tx.QueryRow(ctx, `SELECT id,balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE`, referrer, currency).Scan(&accountID, &balance); err != nil {
		return false, err
	}
	after := balance + commission
	if _, err := tx.Exec(ctx, `UPDATE wallet_accounts SET balance_cents=$2,updated_at=now() WHERE id=$1`, accountID, after); err != nil {
		return false, err
	}
	key := "referral:" + orderPublicID
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key)
VALUES($1,'credit',$2,$3,$4,$5,'referral',$6,'推广返佣',$7)`, accountID, commission, balance, after, currency, fmt.Sprint(referrer), key); err != nil {
		if isUniqueViolation(err) {
			return false, nil // commission already paid for this order
		}
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO referral_commissions(referrer_id,referee_id,order_id,amount_cents,currency,idempotency_key) VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT (idempotency_key) DO NOTHING`, referrer, userID, orderID, commission, currency, key); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// ---- gateway refunds (自动退款) ----

// OrderPaymentInfo is the completed payment behind an order, for gateway
// refund dispatch.
type OrderPaymentInfo struct {
	PaymentPublicID string
	Method          string
	TransactionID   string
	AmountCents     int64
	Currency        string
}

func (s *Store) GetOrderPaymentInfo(ctx context.Context, orderPublicID string) (OrderPaymentInfo, error) {
	var info OrderPaymentInfo
	err := s.DB.QueryRow(ctx, `SELECT p.public_id::text,p.method,p.transaction_id,p.amount_cents,p.currency FROM payments p JOIN orders o ON o.id=p.order_id WHERE o.public_id=$1 AND p.status='completed' ORDER BY p.id DESC LIMIT 1`, orderPublicID).Scan(&info.PaymentPublicID, &info.Method, &info.TransactionID, &info.AmountCents, &info.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderPaymentInfo{}, ErrNotRefundable
	}
	return info, err
}

// RecordGatewayRefund books a gateway-side refund: refund row with the
// gateway id, payment/order/invoice status flips and the audit entry, in one
// transaction. No wallet credit — money went back through the gateway.
func (s *Store) RecordGatewayRefund(ctx context.Context, actorID int64, orderPublicID, reason, requestID, gatewayRefundID string) (model.Refund, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.Refund{}, err
	}
	defer tx.Rollback(ctx)
	var orderID int64
	var status string
	err = tx.QueryRow(ctx, `SELECT id,status FROM orders WHERE public_id=$1 FOR UPDATE`, orderPublicID).Scan(&orderID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Refund{}, ErrNotFound
	}
	if err != nil {
		return model.Refund{}, err
	}
	if status != "paid" && status != "processing" && status != "completed" {
		return model.Refund{}, ErrNotRefundable
	}
	var paymentID int64
	var amount int64
	var method, txnID, currency string
	err = tx.QueryRow(ctx, `SELECT id,amount_cents,method,transaction_id,currency FROM payments WHERE order_id=$1 AND status='completed' ORDER BY id DESC LIMIT 1 FOR UPDATE`, orderID).Scan(&paymentID, &amount, &method, &txnID, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Refund{}, ErrNotRefundable
	}
	if err != nil {
		return model.Refund{}, err
	}
	var refundPublic string
	if err := tx.QueryRow(ctx, `INSERT INTO refunds(payment_id,amount_cents,reason,gateway_refund_id) VALUES($1,$2,$3,$4) RETURNING public_id::text`, paymentID, amount, reason, gatewayRefundID).Scan(&refundPublic); err != nil {
		return model.Refund{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE payments SET status='refunded' WHERE id=$1`, paymentID); err != nil {
		return model.Refund{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='refunded',updated_at=now() WHERE id=$1`, orderID); err != nil {
		return model.Refund{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status='refunded' WHERE order_id=$1 AND status IN ('paid','void')`, orderID); err != nil {
		return model.Refund{}, err
	}
	before, _ := json.Marshal(map[string]any{"order_status": status, "payment_status": "completed"})
	afterJSON, _ := json.Marshal(map[string]any{"order_status": "refunded", "gateway_refund_id": gatewayRefundID, "reason": reason})
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,action,object_type,object_id,request_id,before_data,after_data) VALUES($1,'order.refund','order',$2,$3,$4,$5)`, actor, orderPublicID, requestID, before, afterJSON); err != nil {
		return model.Refund{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Refund{}, err
	}
	return model.Refund{PublicID: refundPublic, OrderID: orderPublicID, PaymentTxn: txnID, AmountCents: amount, Currency: currency, Method: method, Reason: reason, CreatedAt: time.Now()}, nil
}

// ---- financial statistics (财务统计) ----

// FinancialStats aggregates the admin console charts.
type FinancialStats struct {
	BaseCurrency   string           `json:"base_currency"`
	RevenueToday   int64            `json:"revenue_today"`
	Revenue7d      int64            `json:"revenue_7d"`
	Revenue30d     int64            `json:"revenue_30d"`
	Refunds30d     int64            `json:"refunds_30d"`
	Recharge30d    int64            `json:"recharge_30d"`
	MRREstimate    int64            `json:"mrr_estimate"`
	NewUsers7d     int64            `json:"new_users_7d"`
	NewUsers30d    int64            `json:"new_users_30d"`
	TotalUsers     int64            `json:"total_users"`
	ActiveServices int64            `json:"active_services"`
	OpenTickets    int64            `json:"open_tickets"`
	OrdersByStatus map[string]int64 `json:"orders_by_status"`
	RevenueSeries  []StatPoint      `json:"revenue_series"`
	TopProducts    []StatProduct    `json:"top_products"`
}

type StatPoint struct {
	Day   string `json:"day"`
	Cents int64  `json:"cents"`
}

type StatProduct struct {
	Product string `json:"product"`
	Orders  int64  `json:"orders"`
	Cents   int64  `json:"cents"`
}

func (s *Store) FinancialStatistics(ctx context.Context) (*FinancialStats, error) {
	st := &FinancialStats{OrdersByStatus: map[string]int64{}}
	scanInt := func(q string) (int64, error) {
		var v int64
		err := s.DB.QueryRow(ctx, q).Scan(&v)
		return v, err
	}
	var err error
	if st.RevenueToday, err = scanInt(`SELECT coalesce(sum(amount_cents),0) FROM payments WHERE status='completed' AND order_id IS NOT NULL AND created_at>=date_trunc('day',now())`); err != nil {
		return nil, err
	}
	if st.Revenue7d, err = scanInt(`SELECT coalesce(sum(amount_cents),0) FROM payments WHERE status='completed' AND order_id IS NOT NULL AND created_at>=now()-interval '7 days'`); err != nil {
		return nil, err
	}
	if st.Revenue30d, err = scanInt(`SELECT coalesce(sum(amount_cents),0) FROM payments WHERE status='completed' AND order_id IS NOT NULL AND created_at>=now()-interval '30 days'`); err != nil {
		return nil, err
	}
	if st.Refunds30d, err = scanInt(`SELECT coalesce(sum(amount_cents),0) FROM refunds WHERE created_at>=now()-interval '30 days'`); err != nil {
		return nil, err
	}
	if st.Recharge30d, err = scanInt(`SELECT coalesce(sum(amount_cents),0) FROM payments WHERE status='completed' AND order_id IS NULL AND created_at>=now()-interval '30 days'`); err != nil {
		return nil, err
	}
	// MRR proxy: active services normalized to a monthly price.
	if st.MRREstimate, err = scanInt(`SELECT coalesce(sum(CASE oi.billing_cycle WHEN 'yearly' THEN oi.unit_price_cents/12 WHEN 'semiannually' THEN oi.unit_price_cents/6 WHEN 'quarterly' THEN oi.unit_price_cents/3 ELSE oi.unit_price_cents END),0)
FROM services s LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.status='active'`); err != nil {
		return nil, err
	}
	if st.NewUsers7d, err = scanInt(`SELECT count(*) FROM users WHERE created_at>=now()-interval '7 days' AND deleted_at IS NULL`); err != nil {
		return nil, err
	}
	if st.NewUsers30d, err = scanInt(`SELECT count(*) FROM users WHERE created_at>=now()-interval '30 days' AND deleted_at IS NULL`); err != nil {
		return nil, err
	}
	if st.TotalUsers, err = scanInt(`SELECT count(*) FROM users WHERE deleted_at IS NULL`); err != nil {
		return nil, err
	}
	if st.ActiveServices, err = scanInt(`SELECT count(*) FROM services WHERE status='active'`); err != nil {
		return nil, err
	}
	if st.OpenTickets, err = scanInt(`SELECT count(*) FROM tickets WHERE status<>'closed'`); err != nil {
		return nil, err
	}
	orows, err := s.DB.Query(ctx, `SELECT status,count(*) FROM orders GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var status string
		var n int64
		if err := orows.Scan(&status, &n); err != nil {
			orows.Close()
			return nil, err
		}
		st.OrdersByStatus[status] = n
	}
	orows.Close()
	// 30-day daily revenue series.
	rrows, err := s.DB.Query(ctx, `SELECT to_char(d::date,'MM-DD'),coalesce(sum(p.amount_cents),0)
FROM generate_series(date_trunc('day',now())-interval '29 days', date_trunc('day',now()), interval '1 day') d
LEFT JOIN payments p ON date_trunc('day',p.created_at)=d AND p.status='completed' AND p.order_id IS NOT NULL
GROUP BY d ORDER BY d`)
	if err != nil {
		return nil, err
	}
	st.RevenueSeries = []StatPoint{}
	for rrows.Next() {
		var day string
		var cents int64
		if err := rrows.Scan(&day, &cents); err != nil {
			rrows.Close()
			return nil, err
		}
		st.RevenueSeries = append(st.RevenueSeries, StatPoint{Day: day, Cents: cents})
	}
	rrows.Close()
	// Top products in the last 30 days.
	prows, err := s.DB.Query(ctx, `SELECT oi.product_name,count(DISTINCT o.id),coalesce(sum(oi.subtotal_cents),0)
FROM order_items oi JOIN orders o ON o.id=oi.order_id
WHERE o.created_at>=now()-interval '30 days' AND o.status IN ('paid','processing','completed')
GROUP BY oi.product_name ORDER BY 3 DESC LIMIT 8`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	st.TopProducts = []StatProduct{}
	for prows.Next() {
		var p StatProduct
		if err := prows.Scan(&p.Product, &p.Orders, &p.Cents); err != nil {
			return nil, err
		}
		st.TopProducts = append(st.TopProducts, p)
	}
	return st, prows.Err()
}
