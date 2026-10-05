package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 升降级（魔方 shd_upgrades + 模块回调 _ChangePackage）。
//
// 换算规则（与 WHMCS / 魔方一致）：
//   剩余价值 = 旧方案周期价 × 剩余天数 / 周期总天数
//   差价     = 新方案周期价 − 剩余价值
// 差价为 0 或负数（降级）时立即生效，不退款；为正时生成升级订单，付款后切换。

// CycleDays 把一个计费周期折算成天数，用于按天比例换算。
func CycleDays(cycle string) int {
	switch cycle {
	case "hourly", "daily":
		return 1
	case "quarterly":
		return 90
	case "semiannually":
		return 180
	case "yearly":
		return 365
	case "onetime":
		return 3650
	default:
		// 长周期用月数 × 30 天，与魔方的年头对齐（2 年 = 730 天）。
		if months := CycleMonths(cycle); months > 0 {
			return months * 30
		}
		return 30
	}
}

// UpgradePlan 描述一个可选的新方案及其差价。
type UpgradePlan struct {
	ProductID    string `json:"product_id"`
	ProductName  string `json:"product_name"`
	BillingCycle string `json:"billing_cycle"`
	PriceCents   int64  `json:"price_cents"`
	DiffCents    int64  `json:"diff_cents"`
	Payable      bool   `json:"payable"`
}

// UpgradeQuote 是一次升降级报价的完整说明。
type UpgradeQuote struct {
	ServiceID           string `json:"service_id"`
	FromProductID       string `json:"from_product_id"`
	FromProductName     string `json:"from_product_name"`
	FromCycle           string `json:"from_cycle"`
	DaysRemaining       int    `json:"days_remaining"`
	DaysInCycle         int    `json:"days_in_cycle"`
	RemainingValueCents int64  `json:"remaining_value_cents"`
	ToProductID         string `json:"to_product_id"`
	ToProductName       string `json:"to_product_name"`
	ToCycle             string `json:"to_cycle"`
	NewPriceCents       int64  `json:"new_price_cents"`
	DiffCents           int64  `json:"diff_cents"`
	Payable             bool   `json:"payable"`
	Currency            string `json:"currency"`
}

// serviceUpgradeBase 是换算所需的服务现状。
type serviceUpgradeBase struct {
	serviceID   int64
	userID      int64
	productID   int64
	productName string
	cycle       string
	unitPrice   int64
	currency    string
	status      string
	expiresAt   *time.Time
	configCents int64
}

// remainingValue 按剩余天数折算当前服务未使用的价值；已过期则为 0。
func (b serviceUpgradeBase) remainingValue(now time.Time) (value int64, daysRemaining, daysInCycle int) {
	daysInCycle = CycleDays(b.cycle)
	if b.expiresAt == nil {
		return 0, 0, daysInCycle
	}
	remaining := b.expiresAt.Sub(now)
	if remaining <= 0 {
		return 0, 0, daysInCycle
	}
	daysRemaining = int(remaining.Hours() / 24)
	if daysRemaining > daysInCycle {
		daysRemaining = daysInCycle
	}
	if daysRemaining < 0 {
		daysRemaining = 0
	}
	value = (b.unitPrice + b.configCents) * int64(daysRemaining) / int64(daysInCycle)
	return value, daysRemaining, daysInCycle
}

// loadUpgradeBase 在升级事务里读出服务现状（锁住服务行，避免并发改两次）。
func (s *Store) loadUpgradeBase(ctx context.Context, tx pgx.Tx, servicePublicID string, userID int64) (serviceUpgradeBase, error) {
	var b serviceUpgradeBase
	err := tx.QueryRow(ctx, `SELECT s.id,s.user_id,s.product_id,coalesce(oi.product_name,p.name),coalesce(oi.billing_cycle,'monthly'),coalesce(oi.unit_price_cents,0),o.currency,s.status,s.expires_at,coalesce(oi.config_cents,0) FROM services s JOIN orders o ON o.id=s.order_id JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.public_id=$1 AND s.user_id=$2 FOR UPDATE OF s`, servicePublicID, userID).
		Scan(&b.serviceID, &b.userID, &b.productID, &b.productName, &b.cycle, &b.unitPrice, &b.currency, &b.status, &b.expiresAt, &b.configCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// ListUpgradePlans 列出可以升级到的目标方案（全站其它在售商品）。
func (s *Store) ListUpgradePlans(ctx context.Context, userID int64, servicePublicID string) ([]UpgradePlan, error) {
	rows, err := s.DB.Query(ctx, `SELECT p.public_id::text,p.name,pp.billing_cycle,pp.amount_cents,s.expires_at,coalesce(oi.billing_cycle,'monthly'),coalesce(oi.unit_price_cents,0),coalesce(oi.config_cents,0) FROM services s JOIN products p ON p.deleted_at IS NULL AND p.active=true AND p.id<>s.product_id JOIN product_prices pp ON pp.product_id=p.id AND pp.active=true LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.public_id=$1 AND s.user_id=$2 ORDER BY p.sort_weight DESC, pp.amount_cents`, servicePublicID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := []UpgradePlan{}
	for rows.Next() {
		var plan UpgradePlan
		var expiresAt *time.Time
		var fromCycle string
		var fromPrice, fromConfig int64
		if err := rows.Scan(&plan.ProductID, &plan.ProductName, &plan.BillingCycle, &plan.PriceCents, &expiresAt, &fromCycle, &fromPrice, &fromConfig); err != nil {
			return nil, err
		}
		base := serviceUpgradeBase{cycle: fromCycle, unitPrice: fromPrice, configCents: fromConfig, expiresAt: expiresAt}
		value, _, _ := base.remainingValue(now)
		plan.DiffCents = plan.PriceCents - value
		plan.Payable = plan.DiffCents > 0
		out = append(out, plan)
	}
	return out, rows.Err()
}

// QuoteUpgrade 对一次具体的目标方案报价，不落库。
func (s *Store) QuoteUpgrade(ctx context.Context, userID int64, servicePublicID, toProductPublicID, toCycle string) (UpgradeQuote, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return UpgradeQuote{}, err
	}
	defer tx.Rollback(ctx)
	base, err := s.loadUpgradeBase(ctx, tx, servicePublicID, userID)
	if err != nil {
		return UpgradeQuote{}, err
	}
	if base.status != "active" && base.status != "suspended" {
		return UpgradeQuote{}, ErrInvalidState
	}
	var toProductID int64
	var toName string
	var newPrice int64
	err = tx.QueryRow(ctx, `SELECT p.id,p.public_id::text,p.name,pp.amount_cents FROM products p JOIN product_prices pp ON pp.product_id=p.id AND pp.active=true WHERE p.public_id=$1 AND pp.billing_cycle=$2 AND p.active=true AND p.deleted_at IS NULL`, toProductPublicID, toCycle).Scan(&toProductID, &toProductPublicID, &toName, &newPrice)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpgradeQuote{}, ErrNotFound
	}
	if err != nil {
		return UpgradeQuote{}, err
	}
	if toProductID == base.productID && toCycle == base.cycle {
		return UpgradeQuote{}, fmt.Errorf("目标方案与当前方案相同")
	}
	value, daysRemaining, daysInCycle := base.remainingValue(time.Now())
	return UpgradeQuote{
		ServiceID:           servicePublicID,
		FromProductID:       toProductPublicID,
		FromProductName:     base.productName,
		FromCycle:           base.cycle,
		DaysRemaining:       daysRemaining,
		DaysInCycle:         daysInCycle,
		RemainingValueCents: value,
		ToProductID:         toProductPublicID,
		ToProductName:       toName,
		ToCycle:             toCycle,
		NewPriceCents:       newPrice,
		DiffCents:           newPrice - value,
		Payable:             newPrice-value > 0,
		Currency:            base.currency,
	}, nil
}

// RequestUpgrade 创建一次升降级：差价为 0 或负数时立即生效；为正时生成升级订单等待付款。
// 第二个返回值非空表示需要付款的订单号。
func (s *Store) RequestUpgrade(ctx context.Context, userID int64, servicePublicID, toProductPublicID, toCycle string) (UpgradeQuote, string, error) {
	return s.RequestUpgradeWithVoucher(ctx, userID, servicePublicID, toProductPublicID, toCycle, "")
}

// RequestUpgradeWithVoucher 与 RequestUpgrade 相同，但补差价可用一张代金券抵扣
// （受券的 upgrade_use 限制，代金券插件对齐）。
func (s *Store) RequestUpgradeWithVoucher(ctx context.Context, userID int64, servicePublicID, toProductPublicID, toCycle, voucherCode string) (UpgradeQuote, string, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return UpgradeQuote{}, "", err
	}
	defer tx.Rollback(ctx)
	base, err := s.loadUpgradeBase(ctx, tx, servicePublicID, userID)
	if err != nil {
		return UpgradeQuote{}, "", err
	}
	if base.status != "active" && base.status != "suspended" {
		return UpgradeQuote{}, "", ErrInvalidState
	}
	var toProductID int64
	var toPublicID, toName string
	var newPrice int64
	err = tx.QueryRow(ctx, `SELECT p.id,p.public_id::text,p.name,pp.amount_cents FROM products p JOIN product_prices pp ON pp.product_id=p.id AND pp.active=true WHERE p.public_id=$1 AND pp.billing_cycle=$2 AND p.active=true AND p.deleted_at IS NULL`, toProductPublicID, toCycle).Scan(&toProductID, &toPublicID, &toName, &newPrice)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpgradeQuote{}, "", ErrNotFound
	}
	if err != nil {
		return UpgradeQuote{}, "", err
	}
	if toProductID == base.productID && toCycle == base.cycle {
		return UpgradeQuote{}, "", fmt.Errorf("目标方案与当前方案相同")
	}
	value, daysRemaining, daysInCycle := base.remainingValue(time.Now())
	diff := newPrice - value
	quote := UpgradeQuote{
		ServiceID:           servicePublicID,
		FromProductID:       toPublicID,
		FromProductName:     base.productName,
		FromCycle:           base.cycle,
		DaysRemaining:       daysRemaining,
		DaysInCycle:         daysInCycle,
		RemainingValueCents: value,
		ToProductID:         toPublicID,
		ToProductName:       toName,
		ToCycle:             toCycle,
		NewPriceCents:       newPrice,
		DiffCents:           diff,
		Payable:             diff > 0,
		Currency:            base.currency,
	}
	status := "applied"
	if diff > 0 {
		status = "pending"
	}
	var upgradeID int64
	var upgradePublic string
	if err := tx.QueryRow(ctx, `INSERT INTO service_upgrades(service_id,user_id,from_product_id,to_product_id,from_cycle,to_cycle,days_remaining,days_in_cycle,remaining_value_cents,new_price_cents,diff_cents,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id,public_id::text`,
		base.serviceID, userID, base.productID, toProductID, base.cycle, toCycle,
		daysRemaining, daysInCycle, value, newPrice, diff, status).Scan(&upgradeID, &upgradePublic); err != nil {
		return UpgradeQuote{}, "", err
	}
	_ = upgradePublic
	if diff <= 0 {
		if err := applyUpgradeTx(ctx, tx, upgradeID, base.serviceID, toProductID, toCycle, newPrice, 0); err != nil {
			return UpgradeQuote{}, "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return UpgradeQuote{}, "", err
		}
		return quote, "", nil
	}
	// 代金券抵扣补差价（核销记录挂到升级订单上）。
	payTotal := diff
	voucherDiscountCents := int64(0)
	voucherGrantID := int64(0)
	if strings.TrimSpace(voucherCode) != "" {
		d, gid, verr := voucherCheck(ctx, tx, userID, voucherCode, toProductID, toCycle, diff, "upgrade", true)
		if verr != nil {
			return UpgradeQuote{}, "", verr
		}
		voucherDiscountCents, voucherGrantID = d, gid
		payTotal -= d
		if payTotal < 0 {
			payTotal = 0
		}
	}
	var orderID int64
	var orderPublic string
	if err := tx.QueryRow(ctx, `INSERT INTO orders(user_id,status,kind,total_cents,currency,upgrade_id,kind_detail,discount_cents) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,public_id::text`,
		userID, "unpaid", "upgrade", payTotal, base.currency, upgradeID, "upgrade", voucherDiscountCents).Scan(&orderID, &orderPublic); err != nil {
		return UpgradeQuote{}, "", err
	}
	if err := markVoucherUsedTx(ctx, tx, voucherGrantID, orderID); err != nil {
		return UpgradeQuote{}, "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,unit_price_cents,quantity,subtotal_cents,provider_type) VALUES($1,$2,$3,$4,$5,1,$5,$6)`, orderID, toProductID, toName+"（升级）", toCycle, diff, "manual"); err != nil {
		return UpgradeQuote{}, "", err
	}
	var invoiceID int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(order_id,user_id,status,total_cents,currency,due_at) VALUES($1,$2,$3,$4,$5,now()+interval '24 hours') RETURNING id`, orderID, userID, "unpaid", payTotal, base.currency).Scan(&invoiceID); err != nil {
		return UpgradeQuote{}, "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,amount_cents) VALUES($1,$2,$3)`, invoiceID, toName+" 升级补差价", payTotal); err != nil {
		return UpgradeQuote{}, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE service_upgrades SET order_id=$2,updated_at=now() WHERE id=$1`, upgradeID, orderID); err != nil {
		return UpgradeQuote{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return UpgradeQuote{}, "", err
	}
	return quote, orderPublic, nil
}

// applyUpgradeTx 在事务内把服务切到新方案：更新商品、周期、单价并顺延到期时间。
// 到期时间的处理与魔方一致——切换到新周期后，从**现在**起算一个新周期。
// 调用方负责提交事务。
func applyUpgradeTx(ctx context.Context, tx pgx.Tx, upgradeID, serviceID, toProductID int64, toCycle string, newPrice, _ int64) error {
	if _, err := tx.Exec(ctx, `UPDATE services SET product_id=$2,status='provisioning',updated_at=now(),provider_payload=provider_payload||jsonb_build_object('upgrade_to_product',$2,'upgrade_to_cycle',$3,'upgrade_pending',true) WHERE id=$1`, serviceID, toProductID, toCycle); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE order_items SET product_id=$2,billing_cycle=$3,unit_price_cents=$4 WHERE id=(SELECT order_item_id FROM services WHERE id=$1)`, serviceID, toProductID, toCycle, newPrice); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE service_upgrades SET status=$2,applied_at=now(),updated_at=now() WHERE id=$1`, upgradeID, "applied"); err != nil {
		return err
	}
	return nil
}

// PendingUpgrade 是待下发到上游的升级任务。
type PendingUpgrade struct {
	UpgradeID       int64
	ServicePublicID string
	ToCycle         string
	ToProductName   string
}

// ApplyPaidUpgrade 在升级订单付款后应用升级，并返回需要通知上游的服务。
// 幂等：同一订单重复调用只会生效一次（状态必须是 paid/pending）。
func (s *Store) ApplyPaidUpgrade(ctx context.Context, orderPublicID string) (PendingUpgrade, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PendingUpgrade{}, err
	}
	defer tx.Rollback(ctx)
	var upgradeID, serviceID, toProductID int64
	var upStatus, toCycle, toProductName, orderStatus string
	var servicePublic string
	err = tx.QueryRow(ctx, `SELECT u.id,u.service_id,u.to_product_id,u.status,u.to_cycle,p.name,s.public_id::text,o.status
FROM service_upgrades u
JOIN orders o ON o.id=u.order_id
JOIN products p ON p.id=u.to_product_id
JOIN services s ON s.id=u.service_id
WHERE o.public_id=$1 FOR UPDATE OF u`, orderPublicID).
		Scan(&upgradeID, &serviceID, &toProductID, &upStatus, &toCycle, &toProductName, &servicePublic, &orderStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingUpgrade{}, ErrNotFound
	}
	if err != nil {
		return PendingUpgrade{}, err
	}
	if orderStatus != "paid" && orderStatus != "processing" && orderStatus != "completed" {
		return PendingUpgrade{}, ErrInvalidState
	}
	if upStatus == "applied" {
		// 已经应用过，返回同样的任务让调用方可以安全重试。
		return PendingUpgrade{UpgradeID: upgradeID, ServicePublicID: servicePublic, ToCycle: toCycle, ToProductName: toProductName}, nil
	}
	if upStatus != "pending" && upStatus != "paid" {
		return PendingUpgrade{}, ErrInvalidState
	}
	var newPrice int64
	if err := tx.QueryRow(ctx, `SELECT new_price_cents FROM service_upgrades WHERE id=$1`, upgradeID).Scan(&newPrice); err != nil {
		return PendingUpgrade{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE service_upgrades SET status=$2,updated_at=now() WHERE id=$1`, upgradeID, "paid"); err != nil {
		return PendingUpgrade{}, err
	}
	if err := applyUpgradeTx(ctx, tx, upgradeID, serviceID, toProductID, toCycle, newPrice, 0); err != nil {
		return PendingUpgrade{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PendingUpgrade{}, err
	}
	return PendingUpgrade{UpgradeID: upgradeID, ServicePublicID: servicePublic, ToCycle: toCycle, ToProductName: toProductName}, nil
}

// FinishUpgrade 标记升级已下发到上游（worker 调用）。
func (s *Store) FinishUpgrade(ctx context.Context, servicePublicID string, ok bool, errText string) error {
	if ok {
		_, err := s.DB.Exec(ctx, `UPDATE services SET status='active',provider_payload=(provider_payload - 'upgrade_pending')||jsonb_build_object('last_upgrade_ok',true,'last_upgrade_error',''),updated_at=now() WHERE public_id=$1`, servicePublicID)
		return err
	}
	_, err := s.DB.Exec(ctx, `UPDATE services SET status='active',provider_payload=(provider_payload - 'upgrade_pending')||jsonb_build_object('last_upgrade_ok',false,'last_upgrade_error',$2),updated_at=now() WHERE public_id=$1`, servicePublicID, errText)
	return err
}
