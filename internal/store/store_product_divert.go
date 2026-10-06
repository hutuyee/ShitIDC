package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 产品自助转移（对齐魔方主程序附属插件 product_divert）。
//
// 插件主类 ionCube 加密，但 config/config.php（状态字典：1 待接收 / 2 已完成 /
// 3 已关闭 / 4 已拒绝）、menu.php 与全部模板可读，契约取自这些文件：
//
//   - 后台设置：is_open / validity_period（转出有效期，超时未接受自动关闭）/
//     push_cost（转出费用）/ pull_cost（转入费用）/ protection_period（产品订购后
//     多久才能转移）/ product_range[]（支持自助转移的产品范围，多选）。
//   - 转出：按接收方手机号或邮箱查找用户（账号掩码展示），转出费用 > 0 时先支付，
//     支付后接收方收到转入通知。
//   - 接收：接收方在转移列表「接收」并支付转入费用，「支付后，该产品会立刻转移到
//     您的账户中」；也可「拒绝」。
//   - 转出方在待接收阶段可「取消」。
//
// 站内落法：费用沿用发票费用单的模式，各生成一张 kind='artificial' 的人工订单
// （kind_detail=divert_push / divert_pull），三条支付完成路径（钱包 / 在线支付 /
// 管理员标记支付）都会推进转移状态；产品归属迁移复用 §10.26 的 service_transfers
// 留痕。订单、账单、支付记录保持原用户归属不迁移（与 HostTransfer 一致）。

// Divert 状态字典（插件 config/config.php）。
const (
	DivertStatusPending   = 1 // 待接收
	DivertStatusCompleted = 2 // 已完成
	DivertStatusClosed    = 3 // 已关闭（转出方取消或超时）
	DivertStatusRejected  = 4 // 已拒绝（接收方拒绝）
)

var (
	// ErrDivertDisabled 产品自助转移未启用。
	ErrDivertDisabled = errors.New("product divert is disabled")
	// ErrDivertTargetSelf 接收方就是产品当前所有者。
	ErrDivertTargetSelf = errors.New("divert target is the current owner")
	// ErrDivertNotPushable 产品当前状态不可转移。
	ErrDivertNotPushable = errors.New("service cannot be diverted in current status")
	// ErrDivertProtected 产品仍在订购保护期内。
	ErrDivertProtected = errors.New("service is still in the protection period")
	// ErrDivertProductNotAllowed 商品不在允许自助转移的范围内。
	ErrDivertProductNotAllowed = errors.New("product is not in the allowed divert range")
	// ErrDivertExists 该产品已有一笔待接收的转移。
	ErrDivertExists = errors.New("a pending divert already exists for this service")
	// ErrDivertPushUnpaid 转出方尚未完成转出费用的支付。
	ErrDivertPushUnpaid = errors.New("push fee is not paid yet")
)

// ProductDivertConfig 是产品自助转移设置（system_settings 键 product_divert）。
// 字段名沿用插件表单语义；金额落「分」。
type ProductDivertConfig struct {
	Enabled              bool     `json:"is_open"`
	ValidityPeriodDays   int      `json:"validity_period_days"`   // 转出有效期（天），0 = 不自动关闭
	PushCostCents        int64    `json:"push_cost_cents"`        // 转出费用
	PullCostCents        int64    `json:"pull_cost_cents"`        // 转入费用
	ProtectionPeriodDays int      `json:"protection_period_days"` // 订购保护期（天），0 = 不限制
	ProductIDs           []string `json:"product_ids"`            // 允许自助转移的商品公开 ID；空 = 不限
}

// GetProductDivertConfig 读取设置；未配置时返回默认值（关闭、无费用、无保护期）。
func (s *Store) GetProductDivertConfig(ctx context.Context) (ProductDivertConfig, error) {
	out := ProductDivertConfig{}
	err := s.settingGet(ctx, "product_divert", &out)
	if errors.Is(err, ErrNotFound) {
		return ProductDivertConfig{}, nil
	}
	if err != nil {
		return ProductDivertConfig{}, err
	}
	if out.ValidityPeriodDays < 0 {
		out.ValidityPeriodDays = 0
	}
	if out.ProtectionPeriodDays < 0 {
		out.ProtectionPeriodDays = 0
	}
	if out.PushCostCents < 0 {
		out.PushCostCents = 0
	}
	if out.PullCostCents < 0 {
		out.PullCostCents = 0
	}
	return out, nil
}

// SaveProductDivertConfig 保存设置；负值与超长输入被拒绝而不是静默修正。
func (s *Store) SaveProductDivertConfig(ctx context.Context, in ProductDivertConfig) error {
	if in.ValidityPeriodDays < 0 || in.ValidityPeriodDays > 365 {
		return fmt.Errorf("转出有效期取值范围为 0~365 天")
	}
	if in.ProtectionPeriodDays < 0 || in.ProtectionPeriodDays > 3650 {
		return fmt.Errorf("保护期取值范围为 0~3650 天")
	}
	if in.PushCostCents < 0 || in.PullCostCents < 0 {
		return fmt.Errorf("费用不能为负数")
	}
	ids := make([]string, 0, len(in.ProductIDs))
	seen := map[string]bool{}
	for _, id := range in.ProductIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	in.ProductIDs = ids
	return s.settingSave(ctx, "product_divert", in)
}

// DivertLookupTarget 按手机号或邮箱精确查找接收方（自助转移的选人入口）。
// 只接受精确匹配，避免把用户列表当成模糊搜索用；展示时账号掩码（MaskDivertAccount）。
func (s *Store) DivertLookupTarget(ctx context.Context, name string) (int64, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, "", ErrNotFound
	}
	var id int64
	var email string
	err := s.DB.QueryRow(ctx, `SELECT id, email FROM users
WHERE (email = $1 OR (phone <> '' AND phone = $1)) AND status = 'active' AND deleted_at IS NULL`, name).
		Scan(&id, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", err
	}
	return id, email, nil
}

// MaskDivertAccount 掩码展示转移对方账号（对齐插件模板：保留前一半，其余打星）。
func MaskDivertAccount(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) == 0 {
		return ""
	}
	keep := len(r) / 2
	if keep < 1 {
		keep = 1
	}
	if keep > len(r) {
		keep = len(r)
	}
	return string(r[:keep]) + strings.Repeat("*", len(r)-keep)
}

// ProductDivert 是一行转移记录（接口序列化形态；订单 ID 为公开 ID）。
type ProductDivert struct {
	ID            int64      `json:"id"`
	PublicID      string     `json:"public_id"`
	ServiceID     string     `json:"service_id"`
	ProductName   string     `json:"product_name"`
	DedicatedIP   string     `json:"dedicated_ip"`
	PushUserID    int64      `json:"push_uid"`
	PushEmail     string     `json:"push_email"`
	PullUserID    int64      `json:"pull_uid"`
	PullEmail     string     `json:"pull_email"`
	Status        int        `json:"status"`
	PushCostCents int64      `json:"push_cost_cents"`
	PullCostCents int64      `json:"pull_cost_cents"`
	PushOrderID   string     `json:"push_order_id"`
	PullOrderID   string     `json:"pull_order_id"`
	PushPaidAt    *time.Time `json:"push_paid_at"`
	PullPaidAt    *time.Time `json:"pull_paid_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	EndAt         *time.Time `json:"end_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

const divertSelect = `SELECT d.id, d.public_id::text, s.public_id::text,
coalesce(oi.product_name, p.name, ''), d.push_user_id, pu.email, d.pull_user_id, qu.email,
d.status, d.push_cost_cents, d.pull_cost_cents,
coalesce(po.public_id::text, ''), coalesce(qo.public_id::text, ''),
d.push_paid_at, d.pull_paid_at, d.expires_at, d.end_at, d.created_at, s.provider_payload
FROM product_diverts d
JOIN services s ON s.id = d.service_id
JOIN products p ON p.id = s.product_id
LEFT JOIN order_items oi ON oi.id = s.order_item_id
LEFT JOIN users pu ON pu.id = d.push_user_id
LEFT JOIN users qu ON qu.id = d.pull_user_id
LEFT JOIN orders po ON po.id = d.push_order_id
LEFT JOIN orders qo ON qo.id = d.pull_order_id
`

func scanProductDivert(row pgx.Row) (ProductDivert, error) {
	var v ProductDivert
	var payload []byte
	if err := row.Scan(&v.ID, &v.PublicID, &v.ServiceID, &v.ProductName,
		&v.PushUserID, &v.PushEmail, &v.PullUserID, &v.PullEmail,
		&v.Status, &v.PushCostCents, &v.PullCostCents,
		&v.PushOrderID, &v.PullOrderID,
		&v.PushPaidAt, &v.PullPaidAt, &v.ExpiresAt, &v.EndAt, &v.CreatedAt, &payload); err != nil {
		return ProductDivert{}, err
	}
	data := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &data)
	}
	dedicated, _ := expiredIPsFromPayload(data)
	v.DedicatedIP = dedicated
	v.ProductName = divertDisplayProduct(v.ProductName, v.DedicatedIP)
	return v, nil
}

// divertDisplayProduct 把「商品名」与实例主 IP 拼成插件列表里的产品列
// （插件展示 product.domain 与 dedicatedip）。
func divertDisplayProduct(name, dedicated string) string {
	if dedicated == "" {
		return name
	}
	if name == "" {
		return dedicated
	}
	return name + " · " + dedicated
}

// CreateProductDivert 发起一次转出：校验设置 / 保护期 / 商品范围 / 目标用户，
// 转出费用 > 0 时生成人工费用单，接收方在费用支付后收到转入通知。
func (s *Store) CreateProductDivert(ctx context.Context, pushUserID int64, servicePublicID string, pullUserID int64) (ProductDivert, error) {
	cfg, err := s.GetProductDivertConfig(ctx)
	if err != nil {
		return ProductDivert{}, err
	}
	if !cfg.Enabled {
		return ProductDivert{}, ErrDivertDisabled
	}
	if pushUserID == pullUserID {
		return ProductDivert{}, ErrDivertTargetSelf
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ProductDivert{}, err
	}
	defer tx.Rollback(ctx)

	// 锁服务行：同一服务的并发转出在这里串行化，「无待接收转移」检查因此不会双过。
	var svcID, productID, ownerID int64
	var status string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `SELECT id, user_id, product_id, status, created_at FROM services WHERE public_id=$1 FOR UPDATE`, servicePublicID).
		Scan(&svcID, &ownerID, &productID, &status, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProductDivert{}, ErrNotFound
	}
	if err != nil {
		return ProductDivert{}, err
	}
	if ownerID != pushUserID {
		return ProductDivert{}, ErrNotFound
	}
	if status == "terminated" {
		return ProductDivert{}, ErrDivertNotPushable
	}
	if cfg.ProtectionPeriodDays > 0 && time.Since(createdAt) < time.Duration(cfg.ProtectionPeriodDays)*24*time.Hour {
		return ProductDivert{}, ErrDivertProtected
	}
	if len(cfg.ProductIDs) > 0 {
		var inRange bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1 AND public_id::text = ANY($2))`, productID, cfg.ProductIDs).Scan(&inRange); err != nil {
			return ProductDivert{}, err
		}
		if !inRange {
			return ProductDivert{}, ErrDivertProductNotAllowed
		}
	}
	var targetStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 AND deleted_at IS NULL`, pullUserID).Scan(&targetStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProductDivert{}, ErrNotFound
		}
		return ProductDivert{}, err
	}
	if targetStatus != "active" {
		return ProductDivert{}, ErrNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_diverts WHERE service_id=$1 AND status=$2)`, svcID, DivertStatusPending).Scan(&exists); err != nil {
		return ProductDivert{}, err
	}
	if exists {
		return ProductDivert{}, ErrDivertExists
	}

	productName := ""
	if err := tx.QueryRow(ctx, `SELECT coalesce(oi.product_name, p.name, '') FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1`, svcID).Scan(&productName); err != nil {
		return ProductDivert{}, err
	}
	if productName == "" {
		productName = "服务 " + servicePublicID
	}
	var expires *time.Time
	if cfg.ValidityPeriodDays > 0 {
		t := time.Now().Add(time.Duration(cfg.ValidityPeriodDays) * 24 * time.Hour)
		expires = &t
	}
	var pushOrder *int64
	var pushPaid *time.Time
	if cfg.PushCostCents > 0 {
		id, ferr := insertDivertFeeOrderTx(ctx, tx, pushUserID, "divert_push", "产品转出费用 / "+productName, cfg.PushCostCents)
		if ferr != nil {
			return ProductDivert{}, ferr
		}
		pushOrder = &id
	} else {
		now := time.Now()
		pushPaid = &now
	}
	var publicID string
	if err := tx.QueryRow(ctx, `INSERT INTO product_diverts(service_id,push_user_id,pull_user_id,status,push_cost_cents,pull_cost_cents,push_order_id,push_paid_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING public_id::text`,
		svcID, pushUserID, pullUserID, DivertStatusPending, cfg.PushCostCents, cfg.PullCostCents, pushOrder, pushPaid, expires).Scan(&publicID); err != nil {
		return ProductDivert{}, err
	}
	if pushPaid != nil {
		// 无转出费用：直接通知接收方（有费用时由费用单支付完成钩子通知）。
		if err := notifyUserTx(ctx, tx, pullUserID, "divert", "收到产品转入请求",
			"用户已向你发起产品转出，请到「产品转移」页接收或拒绝。", "/divert"); err != nil {
			return ProductDivert{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProductDivert{}, err
	}
	return s.GetProductDivert(ctx, publicID)
}

// insertDivertFeeOrderTx 为转移费用生成人工订单 + 账单（与发票费用单同构）。
// 人工订单固定以 CNY 记账，不参与订单超时自动取消。
func insertDivertFeeOrderTx(ctx context.Context, tx pgx.Tx, userID int64, kindDetail, description string, amountCents int64) (int64, error) {
	var orderID int64
	if err := tx.QueryRow(ctx, `INSERT INTO orders(user_id,status,kind,total_cents,currency,kind_detail,pay_method,created_at,updated_at)
VALUES($1,'unpaid','artificial',$2,'CNY',$3,'prepaid',now(),now()) RETURNING id`, userID, amountCents, kindDetail).Scan(&orderID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,unit_price_cents,quantity,subtotal_cents,provider_type)
VALUES($1,NULL,$2,'onetime',$3,1,$3,'manual')`, orderID, description, amountCents); err != nil {
		return 0, err
	}
	var invoiceID int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(order_id,user_id,status,total_cents,currency,due_at)
VALUES($1,$2,'unpaid',$3,'CNY',now()+interval '7 days') RETURNING id`, orderID, userID, amountCents).Scan(&invoiceID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,amount_cents) VALUES($1,$2,$3)`, invoiceID, description, amountCents); err != nil {
		return 0, err
	}
	return orderID, nil
}

// notifyUserTx 在当前事务里写一条站内通知（与业务同事务提交，不留孤儿通知）。
func notifyUserTx(ctx context.Context, tx pgx.Tx, userID int64, typ, title, body, link string) error {
	_, err := tx.Exec(ctx, `INSERT INTO notifications(user_id,type,title,body,link) VALUES($1,$2,$3,$4,NULLIF($5,''))`,
		userID, typ, title, body, link)
	return err
}

// AcceptProductDivert 接收方接受转移：转出费用未支付时拒绝；转入费用 > 0 时
// 生成转入费用单待支付，费用为 0 时立即完成迁移。
func (s *Store) AcceptProductDivert(ctx context.Context, userID int64, publicID string) (ProductDivert, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ProductDivert{}, err
	}
	defer tx.Rollback(ctx)
	d, err := lockDivertTx(ctx, tx, publicID)
	if err != nil {
		return ProductDivert{}, err
	}
	if d.pullUser != userID {
		return ProductDivert{}, ErrNotFound
	}
	if d.status != DivertStatusPending {
		return ProductDivert{}, ErrInvalidState
	}
	if d.pushPaidAt == nil {
		return ProductDivert{}, ErrDivertPushUnpaid
	}
	if d.pullCost > 0 && d.pullOrderID == nil {
		orderID, ferr := insertDivertFeeOrderTx(ctx, tx, userID, "divert_pull", "产品转入费用 / "+d.productName, d.pullCost)
		if ferr != nil {
			return ProductDivert{}, ferr
		}
		if _, err := tx.Exec(ctx, `UPDATE product_diverts SET pull_order_id=$2, updated_at=now() WHERE id=$1`, d.id, orderID); err != nil {
			return ProductDivert{}, err
		}
	} else if d.pullCost == 0 {
		if err := completeDivertTx(ctx, tx, d.id); err != nil {
			return ProductDivert{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProductDivert{}, err
	}
	return s.GetProductDivert(ctx, publicID)
}

// RejectProductDivert 接收方拒绝（状态 4）：未支付的费用单一并作废，已支付的费用
// 不自动退还（如需退款走管理员的既有退款流程）。
func (s *Store) RejectProductDivert(ctx context.Context, userID int64, publicID string) (ProductDivert, error) {
	return s.closeDivert(ctx, userID, publicID, DivertStatusRejected, false)
}

// CancelProductDivert 转出方取消（状态 3），同上作废未支付费用单。
func (s *Store) CancelProductDivert(ctx context.Context, userID int64, publicID string) (ProductDivert, error) {
	return s.closeDivert(ctx, userID, publicID, DivertStatusClosed, true)
}

// closeDivert 终止一笔待接收的转移；asPush 决定操作者必须是转出方还是接收方。
func (s *Store) closeDivert(ctx context.Context, userID int64, publicID string, finalStatus int, asPush bool) (ProductDivert, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ProductDivert{}, err
	}
	defer tx.Rollback(ctx)
	d, err := lockDivertTx(ctx, tx, publicID)
	if err != nil {
		return ProductDivert{}, err
	}
	owner := d.pullUser
	if asPush {
		owner = d.pushUser
	}
	if owner != userID {
		return ProductDivert{}, ErrNotFound
	}
	if d.status != DivertStatusPending {
		return ProductDivert{}, ErrInvalidState
	}
	if err := voidDivertFeeOrdersTx(ctx, tx, d.id); err != nil {
		return ProductDivert{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE product_diverts SET status=$2, end_at=now(), updated_at=now() WHERE id=$1 AND status=$3`, d.id, finalStatus, DivertStatusPending); err != nil {
		return ProductDivert{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProductDivert{}, err
	}
	return s.GetProductDivert(ctx, publicID)
}

// VerifyProductDivert 对齐插件的「手动检测」：转出与转入费用都已支付时完成迁移，
// 否则原样返回当前状态（幂等，无副作用）。
func (s *Store) VerifyProductDivert(ctx context.Context, userID int64, publicID string) (ProductDivert, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ProductDivert{}, err
	}
	defer tx.Rollback(ctx)
	d, err := lockDivertTx(ctx, tx, publicID)
	if err != nil {
		return ProductDivert{}, err
	}
	if d.pushUser != userID && d.pullUser != userID {
		return ProductDivert{}, ErrNotFound
	}
	if d.status == DivertStatusPending && d.pushPaidAt != nil && d.pullPaidAt != nil {
		if err := completeDivertTx(ctx, tx, d.id); err != nil {
			return ProductDivert{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProductDivert{}, err
	}
	return s.GetProductDivert(ctx, publicID)
}

// divertKey 是转移行锁定与状态推进用的内部键。
type divertKey struct {
	id            int64
	pushUser      int64
	pullUser      int64
	status        int
	pushCost      int64
	pullCost      int64
	pushOrderID   *int64
	pullOrderID   *int64
	pushPaidAt    *time.Time
	pullPaidAt    *time.Time
	servicePublic string
	productName   string
}

// lockDivertTx 按公开 ID 锁定转移行，读出状态推进所需的字段。
func lockDivertTx(ctx context.Context, tx pgx.Tx, publicID string) (divertKey, error) {
	var d divertKey
	err := tx.QueryRow(ctx, `SELECT d.id, d.push_user_id, d.pull_user_id, d.status,
d.push_cost_cents, d.pull_cost_cents, d.push_order_id, d.pull_order_id, d.push_paid_at, d.pull_paid_at,
s.public_id::text, coalesce(oi.product_name, p.name, '')
FROM product_diverts d
JOIN services s ON s.id = d.service_id
JOIN products p ON p.id = s.product_id
LEFT JOIN order_items oi ON oi.id = s.order_item_id
WHERE d.public_id=$1 FOR UPDATE OF d`, publicID).
		Scan(&d.id, &d.pushUser, &d.pullUser, &d.status,
			&d.pushCost, &d.pullCost, &d.pushOrderID, &d.pullOrderID, &d.pushPaidAt, &d.pullPaidAt,
			&d.servicePublic, &d.productName)
	if errors.Is(err, pgx.ErrNoRows) {
		return divertKey{}, ErrNotFound
	}
	if err != nil {
		return divertKey{}, err
	}
	if d.productName == "" {
		d.productName = "服务 " + d.servicePublic
	}
	return d, nil
}

// completeDivertTx 在当前事务里完成转移：迁移产品归属、写转移留痕、通知双方。
// 只对仍处于待接收状态的转移生效（重复调用是空操作）。
func completeDivertTx(ctx context.Context, tx pgx.Tx, divertID int64) error {
	tag, err := tx.Exec(ctx, `UPDATE product_diverts SET status=$2, pull_paid_at=now(), end_at=now(), updated_at=now()
WHERE id=$1 AND status=$3`, divertID, DivertStatusCompleted, DivertStatusPending)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	var svcID, pushUser, pullUser int64
	if err := tx.QueryRow(ctx, `SELECT service_id, push_user_id, pull_user_id FROM product_diverts WHERE id=$1`, divertID).
		Scan(&svcID, &pushUser, &pullUser); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE services SET user_id=$2, updated_at=now() WHERE id=$1 AND status <> 'terminated'`, svcID, pullUser); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO service_transfers(service_id, from_user_id, to_user_id, operator_id, remark)
VALUES($1, $2, $3, $2, '用户自助转移')`, svcID, pushUser, pullUser); err != nil {
		return err
	}
	if err := notifyUserTx(ctx, tx, pushUser, "divert", "产品已转出",
		"你发起的产品转移已完成，产品已迁移到接收方账户。", "/divert"); err != nil {
		return err
	}
	return notifyUserTx(ctx, tx, pullUser, "divert", "产品已转入",
		"转移费用支付成功，产品已立刻转移到你的账户中。", "/divert")
}

// advanceDivertFeeOrderTx 是转移费用单支付完成的收尾钩子（钱包 / 在线支付 /
// 管理员标记支付三条路径共用）：转出费用支付 → 通知接收方；转入费用支付 →
// 双方都已支付则立刻完成迁移。非转移费用单原样放行。
func advanceDivertFeeOrderTx(ctx context.Context, tx pgx.Tx, orderID int64) error {
	var detail string
	if err := tx.QueryRow(ctx, `SELECT kind_detail FROM orders WHERE id=$1`, orderID).Scan(&detail); err != nil {
		return err
	}
	switch detail {
	case "divert_push":
		var divertID, pullUser int64
		err := tx.QueryRow(ctx, `UPDATE product_diverts SET push_paid_at=now(), updated_at=now()
WHERE push_order_id=$1 AND status=$2 AND push_paid_at IS NULL RETURNING id, pull_user_id`, orderID, DivertStatusPending).
			Scan(&divertID, &pullUser)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return notifyUserTx(ctx, tx, pullUser, "divert", "收到产品转入请求",
			"转出方已支付转出费用，请到「产品转移」页接收或拒绝。", "/divert")
	case "divert_pull":
		var divertID int64
		var pushPaid *time.Time
		err := tx.QueryRow(ctx, `UPDATE product_diverts SET pull_paid_at=now(), updated_at=now()
WHERE pull_order_id=$1 AND status=$2 AND pull_paid_at IS NULL RETURNING id, push_paid_at`, orderID, DivertStatusPending).
			Scan(&divertID, &pushPaid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if pushPaid == nil {
			return nil
		}
		return completeDivertTx(ctx, tx, divertID)
	}
	return nil
}

// voidDivertFeeOrdersTx 作废一笔转移双方仍未支付的费用订单与账单。
// 已支付的费用不退（如需退款走管理员的既有退款流程）。
func voidDivertFeeOrdersTx(ctx context.Context, tx pgx.Tx, divertID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status='void'
WHERE status='unpaid' AND order_id IN (SELECT push_order_id FROM product_diverts WHERE id=$1 AND push_order_id IS NOT NULL
UNION SELECT pull_order_id FROM product_diverts WHERE id=$1 AND pull_order_id IS NOT NULL)`, divertID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE orders SET status='cancelled', updated_at=now()
WHERE status='unpaid' AND id IN (SELECT push_order_id FROM product_diverts WHERE id=$1 AND push_order_id IS NOT NULL
UNION SELECT pull_order_id FROM product_diverts WHERE id=$1 AND pull_order_id IS NOT NULL)`, divertID)
	return err
}

// ExpireProductDiverts 关闭超过转出有效期仍未被接受的转移（调度器调用）：
// 状态置为已关闭并作废未支付的费用单，返回本次关闭数量。
func (s *Store) ExpireProductDiverts(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT id FROM product_diverts
WHERE status=$1 AND expires_at IS NOT NULL AND expires_at < now() LIMIT $2 FOR UPDATE SKIP LOCKED`,
		DivertStatusPending, limit)
	if err != nil {
		return 0, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	closed := 0
	for _, id := range ids {
		if err := s.expireOneDivert(ctx, id); err != nil {
			return closed, err
		}
		closed++
	}
	return closed, nil
}

func (s *Store) expireOneDivert(ctx context.Context, id int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := voidDivertFeeOrdersTx(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE product_diverts SET status=$2, end_at=now(), updated_at=now() WHERE id=$1 AND status=$3`, id, DivertStatusClosed, DivertStatusPending); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetProductDivert 按公开 ID 读取一条转移记录（列表行形态）。
func (s *Store) GetProductDivert(ctx context.Context, publicID string) (ProductDivert, error) {
	row := s.DB.QueryRow(ctx, divertSelect+`WHERE d.public_id=$1`, publicID)
	return scanProductDivert(row)
}

// ListMyProductDiverts 当前用户的转移列表（转出或转入），可按状态过滤。
func (s *Store) ListMyProductDiverts(ctx context.Context, userID int64, status int, limit, offset int) ([]ProductDivert, int64, error) {
	return s.listDiverts(ctx, userID, status, "", limit, offset)
}

// AdminListProductDiverts 后台全量转移列表，关键词匹配商品 / 产品ID / 双方邮箱。
func (s *Store) AdminListProductDiverts(ctx context.Context, keyword string, status int, limit, offset int) ([]ProductDivert, int64, error) {
	return s.listDiverts(ctx, 0, status, keyword, limit, offset)
}

func (s *Store) listDiverts(ctx context.Context, userID int64, status int, keyword string, limit, offset int) ([]ProductDivert, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := " WHERE true"
	args := []any{}
	if userID > 0 {
		args = append(args, userID)
		where += fmt.Sprintf(" AND (d.push_user_id=$%d OR d.pull_user_id=$%d)", len(args), len(args))
	}
	if status >= 1 && status <= 4 {
		args = append(args, status)
		where += fmt.Sprintf(" AND d.status=$%d", len(args))
	}
	if keyword != "" {
		args = append(args, "%"+keyword+"%", keyword+"%")
		where += fmt.Sprintf(" AND (p.name ILIKE $%d OR s.public_id::text ILIKE $%d OR pu.email ILIKE $%d OR qu.email ILIKE $%d)",
			len(args)-1, len(args)-1, len(args), len(args))
	}
	countArgs := append([]any{}, args...)
	args = append(args, limit, offset)
	rows, err := s.DB.Query(ctx, divertSelect+where+fmt.Sprintf(" ORDER BY d.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ProductDivert{}
	for rows.Next() {
		v, err := scanProductDivert(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM product_diverts d
JOIN services s ON s.id=d.service_id
JOIN products p ON p.id=s.product_id
LEFT JOIN users pu ON pu.id=d.push_user_id
LEFT JOIN users qu ON qu.id=d.pull_user_id`+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// DivertServiceOption 是转出表单里的服务选项（含可转移判定与不可转移原因）。
type DivertServiceOption struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

// MyDivertableServices 列出当前用户可发起自助转移的服务（未删除、在商品范围内、
// 已过保护期、没有待接收的转移），供转出表单选择。
func (s *Store) MyDivertableServices(ctx context.Context, userID int64) ([]DivertServiceOption, error) {
	cfg, err := s.GetProductDivertConfig(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT s.public_id::text, coalesce(oi.product_name, p.name, ''), s.provider_payload, s.created_at,
EXISTS(SELECT 1 FROM product_diverts d WHERE d.service_id=s.id AND d.status=$1)
FROM services s
JOIN products p ON p.id=s.product_id
LEFT JOIN order_items oi ON oi.id=s.order_item_id
WHERE s.user_id=$2 AND s.status <> 'terminated' AND s.status <> 'failed'
ORDER BY s.id DESC LIMIT 200`, DivertStatusPending, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DivertServiceOption{}
	now := time.Now()
	for rows.Next() {
		var v DivertServiceOption
		var payload []byte
		var createdAt time.Time
		var hasPending bool
		if err := rows.Scan(&v.ID, &v.Name, &payload, &createdAt, &hasPending); err != nil {
			return nil, err
		}
		data := map[string]any{}
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &data)
		}
		v.IP, _ = expiredIPsFromPayload(data)
		switch {
		case !cfg.Enabled:
			v.Reason = "产品自助转移未启用"
		case cfg.ProtectionPeriodDays > 0 && now.Sub(createdAt) < time.Duration(cfg.ProtectionPeriodDays)*24*time.Hour:
			v.Reason = fmt.Sprintf("订购保护期内（%d 天）", cfg.ProtectionPeriodDays)
		case hasPending:
			v.Reason = "已有一笔待接收的转移"
		case len(cfg.ProductIDs) > 0 && !s.divertProductAllowed(ctx, v.ID, cfg.ProductIDs):
			v.Reason = "商品不在允许转移的范围内"
		default:
			v.Eligible = true
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) divertProductAllowed(ctx context.Context, servicePublicID string, productIDs []string) bool {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services sv JOIN products p ON p.id=sv.product_id
WHERE sv.public_id=$1 AND p.public_id::text = ANY($2))`, servicePublicID, productIDs).Scan(&ok)
	return err == nil && ok
}
