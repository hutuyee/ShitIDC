package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// 周期人工订单（对齐魔方 CBAP 插件 CycleArtificialOrder）。
//
// 与普通订单不同，周期人工订单没有商品：它是一条「生成规则」，按 num + unit
// 的周期在 start_at ~ end_at 范围内为指定用户生成人工订单（kind='artificial'）。
// 首次生成按订单金额，之后按续费金额（为 0 时退回订单金额）。人工订单支付后
// 只把订单置为已完成，不开通任何服务（插件语义：人工订单由管理员线下处理）。
type CycleArtificialOrder struct {
	PublicID          string     `json:"id"`
	ClientPublicID    string     `json:"client_id"`
	Username          string     `json:"username"`
	Company           string     `json:"company"`
	Email             string     `json:"email"`
	Description       string     `json:"description"`
	AmountCents       int64      `json:"amount_cents"`
	RenewAmountCents  int64      `json:"renew_amount_cents"`
	StartAt           time.Time  `json:"start_at"`
	EndAt             *time.Time `json:"end_at"`
	Num               int        `json:"num"`
	Unit              string     `json:"unit"`
	LastGeneratedAt   *time.Time `json:"last_generated_at"`
	NextGenerateAt    *time.Time `json:"next_generate_at"`
	GeneratedCount    int        `json:"generated_count"`
	ClientCreditCents int64      `json:"client_credit_cents"`
	CreatedAt         time.Time  `json:"created_at"`
}

// CycleArtificialOrderInput 是新增 / 修改生成规则的表单（金额已换算成「分」）。
type CycleArtificialOrderInput struct {
	ClientPublicID   string
	Description      string
	AmountCents      int64
	RenewAmountCents int64
	StartAt          time.Time
	EndAt            *time.Time
	Num              int
	Unit             string
}

const (
	// 人工订单以站点主币种 CNY 记账（插件同样以 ¥ 计价）。
	cycleArtificialCurrency = "CNY"
	// 单次调度最多为一个规则补生成的订单数，防止规则改成极短周期后刷单。
	cycleArtificialCatchUp = 30
)

func normalizeCycleArtificialInput(in *CycleArtificialOrderInput) error {
	in.Description = strings.TrimSpace(in.Description)
	if in.Description == "" {
		return errors.New("请填写订单描述")
	}
	if len([]rune(in.Description)) > 1000 {
		return errors.New("订单描述长度不能超过 1000 字")
	}
	if in.AmountCents < 0 || in.RenewAmountCents < 0 {
		return errors.New("金额不能为负数")
	}
	if in.Num <= 0 {
		return errors.New("生成周期必须为正整数")
	}
	switch in.Unit {
	case "day", "month", "year":
	default:
		return errors.New("生成周期单位不合法")
	}
	if in.StartAt.IsZero() {
		return errors.New("请选择开始时间")
	}
	if in.EndAt != nil && !in.EndAt.After(in.StartAt) {
		return errors.New("结束时间必须晚于开始时间")
	}
	return nil
}

// cycleArtificialNext 按 num / unit 从 base 推进一个周期（按自然日 / 自然月）。
func cycleArtificialNext(base time.Time, num int, unit string) time.Time {
	switch unit {
	case "day":
		return base.AddDate(0, 0, num)
	case "year":
		return base.AddDate(num, 0, 0)
	default:
		return base.AddDate(0, num, 0)
	}
}

// cycleArtificialSchedule 从最近一次生成时间（没有则开始时间）重新计算下次
// 生成时间；超出结束时间时返回 nil（不再生成）。对应插件提示 cycle_tip1。
func cycleArtificialSchedule(start time.Time, last *time.Time, end *time.Time, num int, unit string) *time.Time {
	base := start
	if last != nil {
		base = *last
	}
	next := cycleArtificialNext(base, num, unit)
	if end != nil && next.After(*end) {
		return nil
	}
	return &next
}

const cycleArtificialSelect = `SELECT c.public_id::text,u.public_id::text,
coalesce(nullif(p.nickname,''),''),coalesce(p.company,''),u.email,
c.description,c.amount_cents,c.renew_amount_cents,c.start_at,c.end_at,c.num,c.unit,
c.last_generated_at,c.next_generate_at,c.generated_count,c.created_at,
coalesce((SELECT wa.balance_cents FROM wallet_accounts wa WHERE wa.user_id=u.id AND wa.currency='CNY'),0)
FROM cycle_artificial_orders c
JOIN users u ON u.id=c.user_id
LEFT JOIN user_profiles p ON p.user_id=u.id`

func scanCycleArtificialOrder(row pgx.Row) (CycleArtificialOrder, error) {
	var v CycleArtificialOrder
	err := row.Scan(&v.PublicID, &v.ClientPublicID, &v.Username, &v.Company, &v.Email,
		&v.Description, &v.AmountCents, &v.RenewAmountCents, &v.StartAt, &v.EndAt, &v.Num, &v.Unit,
		&v.LastGeneratedAt, &v.NextGenerateAt, &v.GeneratedCount, &v.CreatedAt, &v.ClientCreditCents)
	return v, err
}

// ListCycleArtificialOrders 分页列出生成规则，keywords 匹配描述 / 邮箱 / 昵称 / 公司。
func (s *Store) ListCycleArtificialOrders(ctx context.Context, keywords string, limit, offset int) ([]CycleArtificialOrder, int64, error) {
	keywords = strings.TrimSpace(keywords)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where := `WHERE ($1='' OR c.description ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%'
  OR coalesce(p.nickname,'') ILIKE '%'||$1||'%' OR coalesce(p.company,'') ILIKE '%'||$1||'%')`
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM cycle_artificial_orders c JOIN users u ON u.id=c.user_id LEFT JOIN user_profiles p ON p.user_id=u.id `+where, keywords).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, cycleArtificialSelect+` `+where+` ORDER BY c.id DESC LIMIT $2 OFFSET $3`, keywords, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []CycleArtificialOrder{}
	for rows.Next() {
		v, err := scanCycleArtificialOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// GetCycleArtificialOrder 按 public_id 读一条生成规则。
func (s *Store) GetCycleArtificialOrder(ctx context.Context, publicID string) (CycleArtificialOrder, error) {
	v, err := scanCycleArtificialOrder(s.DB.QueryRow(ctx, cycleArtificialSelect+` WHERE c.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return CycleArtificialOrder{}, ErrNotFound
	}
	return v, err
}

// CreateCycleArtificialOrder 新增生成规则：首次生成时间为开始时间。
func (s *Store) CreateCycleArtificialOrder(ctx context.Context, in CycleArtificialOrderInput) (CycleArtificialOrder, error) {
	if err := normalizeCycleArtificialInput(&in); err != nil {
		return CycleArtificialOrder{}, err
	}
	var userID int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM users WHERE public_id=$1 AND deleted_at IS NULL`, in.ClientPublicID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CycleArtificialOrder{}, errors.New("用户不存在")
	}
	if err != nil {
		return CycleArtificialOrder{}, err
	}
	var publicID string
	if err := s.DB.QueryRow(ctx, `INSERT INTO cycle_artificial_orders(user_id,description,amount_cents,renew_amount_cents,start_at,end_at,num,unit,next_generate_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$5) RETURNING public_id::text`,
		userID, in.Description, in.AmountCents, in.RenewAmountCents, in.StartAt, in.EndAt, in.Num, in.Unit).Scan(&publicID); err != nil {
		return CycleArtificialOrder{}, err
	}
	return s.GetCycleArtificialOrder(ctx, publicID)
}

// UpdateCycleArtificialOrder 修改生成规则；生成周期 / 时间范围变化后按
// 「最近一次已生成订单的日期」重算下次生成时间。
func (s *Store) UpdateCycleArtificialOrder(ctx context.Context, publicID string, in CycleArtificialOrderInput) error {
	if err := normalizeCycleArtificialInput(&in); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	var last *time.Time
	err = tx.QueryRow(ctx, `SELECT id,last_generated_at FROM cycle_artificial_orders WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	next := cycleArtificialSchedule(in.StartAt, last, in.EndAt, in.Num, in.Unit)
	if _, err := tx.Exec(ctx, `UPDATE cycle_artificial_orders
SET description=$2,amount_cents=$3,renew_amount_cents=$4,start_at=$5,end_at=$6,num=$7,unit=$8,next_generate_at=$9,updated_at=now()
WHERE id=$1`, id, in.Description, in.AmountCents, in.RenewAmountCents, in.StartAt, in.EndAt, in.Num, in.Unit, next); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteCycleArtificialOrder 删除生成规则；已生成的子订单保留（关联置空）。
func (s *Store) DeleteCycleArtificialOrder(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM cycle_artificial_orders WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GenerateDueCycleArtificialOrders 为到期的生成规则补生成人工订单，返回本轮
// 生成的订单数。每个规则独立事务（FOR UPDATE 串行化），重复执行不会为同一
// 周期重复生成。
func (s *Store) GenerateDueCycleArtificialOrders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text FROM cycle_artificial_orders
WHERE next_generate_at IS NOT NULL AND next_generate_at <= now() AND (end_at IS NULL OR next_generate_at <= end_at)
ORDER BY next_generate_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
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
	generated := 0
	var firstErr error
	for _, id := range ids {
		n, err := s.generateCycleArtificialOrder(ctx, id)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("周期人工订单 %s: %w", id, err)
			}
			continue
		}
		generated += n
	}
	return generated, firstErr
}

// generateCycleArtificialOrder 在独立事务里为一条规则补生成到期订单。
// 订单的 created_at 落在对应的周期时点上（补生成也保持账期对齐）。
func (s *Store) generateCycleArtificialOrder(ctx context.Context, publicID string) (int, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id, userID, amount, renewAmount int64
	var num, generatedCount int
	var description, unit string
	var startAt time.Time
	var endAt, nextAt *time.Time
	err = tx.QueryRow(ctx, `SELECT id,user_id,description,amount_cents,renew_amount_cents,start_at,end_at,num,unit,next_generate_at,generated_count
FROM cycle_artificial_orders WHERE public_id=$1 FOR UPDATE`, publicID).
		Scan(&id, &userID, &description, &amount, &renewAmount, &startAt, &endAt, &num, &unit, &nextAt, &generatedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if nextAt == nil {
		return 0, nil
	}
	now := time.Now()
	due := *nextAt
	created := 0
	var lastDue time.Time
	for created < cycleArtificialCatchUp && !due.After(now) && (endAt == nil || !due.After(*endAt)) {
		// 首次按订单金额，之后的周期按续费金额（未填则沿用订单金额）。
		price := amount
		if generatedCount+created > 0 && renewAmount > 0 {
			price = renewAmount
		}
		if err := insertCycleArtificialOrderTx(ctx, tx, userID, id, description, price, due); err != nil {
			return created, err
		}
		lastDue = due
		created++
		due = cycleArtificialNext(due, num, unit)
	}
	if created == 0 {
		return 0, nil
	}
	var next *time.Time
	if endAt == nil || !due.After(*endAt) {
		next = &due
	}
	if _, err := tx.Exec(ctx, `UPDATE cycle_artificial_orders
SET last_generated_at=$2,next_generate_at=$3,generated_count=generated_count+$4,updated_at=now() WHERE id=$1`,
		id, lastDue, next, created); err != nil {
		return created, err
	}
	if err := tx.Commit(ctx); err != nil {
		return created, err
	}
	return created, nil
}

// insertCycleArtificialOrderTx 写入一笔人工订单（订单 + 明细 + 未支付账单）。
// 明细行的 product_id 为 NULL：人工订单没有商品，服务端也不为其开通服务。
func insertCycleArtificialOrderTx(ctx context.Context, tx pgx.Tx, userID, cycleID int64, description string, priceCents int64, createdAt time.Time) error {
	var orderID int64
	if err := tx.QueryRow(ctx, `INSERT INTO orders(user_id,status,kind,total_cents,currency,kind_detail,pay_method,cycle_artificial_order_id,created_at,updated_at)
VALUES($1,'unpaid','artificial',$2,$3,'artificial','prepaid',$4,$5,$5) RETURNING id`,
		userID, priceCents, cycleArtificialCurrency, cycleID, createdAt).Scan(&orderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,unit_price_cents,quantity,subtotal_cents,provider_type)
VALUES($1,NULL,$2,'onetime',$3,1,$3,'manual')`, orderID, description, priceCents); err != nil {
		return err
	}
	var invoiceID int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(order_id,user_id,status,total_cents,currency,due_at)
VALUES($1,$2,'unpaid',$3,$4,now()+interval '7 days') RETURNING id`, orderID, userID, priceCents, cycleArtificialCurrency).Scan(&invoiceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,amount_cents) VALUES($1,$2,$3)`, invoiceID, description, priceCents); err != nil {
		return err
	}
	return nil
}

// CycleOrderChildrenFilter 是详情页子订单列表的筛选条件（金额单位「分」，
// 时间为订单创建时间）。
type CycleOrderChildrenFilter struct {
	Status      string
	Gateway     string
	AmountCents int64
	StartAt     *time.Time
	EndAt       *time.Time
	OrderBy     string
	Sort        string
	Limit       int
	Offset      int
}

// ListCycleArtificialOrderChildren 分页列出某个生成规则产出的子订单。
func (s *Store) ListCycleArtificialOrderChildren(ctx context.Context, cyclePublicID string, f CycleOrderChildrenFilter) ([]model.Order, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 20
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	args := []any{cyclePublicID}
	where := `WHERE o.cycle_artificial_order_id=(SELECT id FROM cycle_artificial_orders WHERE public_id=$1)`
	if f.Status != "" {
		args = append(args, f.Status)
		where += fmt.Sprintf(` AND o.status=$%d`, len(args))
	}
	if f.Gateway != "" {
		args = append(args, f.Gateway)
		where += fmt.Sprintf(` AND EXISTS(SELECT 1 FROM payments p WHERE p.order_id=o.id AND p.method=$%d)`, len(args))
	}
	if f.AmountCents > 0 {
		args = append(args, f.AmountCents)
		where += fmt.Sprintf(` AND o.total_cents=$%d`, len(args))
	}
	if f.StartAt != nil {
		args = append(args, *f.StartAt)
		where += fmt.Sprintf(` AND o.created_at >= $%d`, len(args))
	}
	if f.EndAt != nil {
		args = append(args, *f.EndAt)
		where += fmt.Sprintf(` AND o.created_at <= $%d`, len(args))
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM orders o `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	orderCol := "o.id"
	switch f.OrderBy {
	case "amount":
		orderCol = "o.total_cents"
	case "create_time":
		orderCol = "o.created_at"
	}
	dir := "DESC"
	if strings.EqualFold(f.Sort, "asc") {
		dir = "ASC"
	}
	args = append(args, f.Limit, f.Offset)
	query := `SELECT o.id,o.public_id::text,o.user_id,o.status,o.kind,o.total_cents,o.currency,o.created_at,o.paid_at,o.cancelled_at,
COALESCE((SELECT json_agg(json_build_object('product_name',oi.product_name,'billing_cycle',oi.billing_cycle,'unit_price_cents',oi.unit_price_cents,'quantity',oi.quantity,'subtotal_cents',oi.subtotal_cents) ORDER BY oi.id)
 FROM order_items oi WHERE oi.order_id=o.id),'[]'::json),
(SELECT json_build_object('method',p.method,'type',p.raw_payload->>'pay_type','paid_at',p.created_at)
 FROM payments p WHERE p.order_id=o.id AND p.status='completed' ORDER BY p.id DESC LIMIT 1)
FROM orders o ` + where + fmt.Sprintf(` ORDER BY %s %s LIMIT $%d OFFSET $%d`, orderCol, dir, len(args)-1, len(args))
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.Order{}
	for rows.Next() {
		var o model.Order
		var items, payment []byte
		if err := rows.Scan(&o.ID, &o.PublicID, &o.UserUID, &o.Status, &o.Kind, &o.TotalCents, &o.Currency, &o.CreatedAt, &o.PaidAt, &o.CancelledAt, &items, &payment); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(items, &o.Items)
		if len(payment) > 0 {
			var p model.OrderPayment
			if err := json.Unmarshal(payment, &p); err == nil {
				o.Payment = &p
			}
		}
		out = append(out, o)
	}
	return out, total, rows.Err()
}

// AdjustArtificialOrder 调整未支付人工订单的金额与描述（订单 / 明细 / 账单同步）。
// 对应插件详情页的「调整价格」。
func (s *Store) AdjustArtificialOrder(ctx context.Context, orderPublicID string, amountCents int64, description string) error {
	description = strings.TrimSpace(description)
	if amountCents < 0 {
		return errors.New("金额不能为负数")
	}
	if description == "" {
		return errors.New("请填写订单描述")
	}
	if len([]rune(description)) > 1000 {
		return errors.New("订单描述长度不能超过 1000 字")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var orderID int64
	var status, kind string
	err = tx.QueryRow(ctx, `SELECT id,status,kind FROM orders WHERE public_id=$1 FOR UPDATE`, orderPublicID).Scan(&orderID, &status, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if kind != "artificial" || status != "unpaid" {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET total_cents=$2,updated_at=now() WHERE id=$1`, orderID, amountCents); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE order_items SET product_name=$2,unit_price_cents=$3,subtotal_cents=$3 WHERE order_id=$1`, orderID, description, amountCents); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET total_cents=$2 WHERE order_id=$1 AND status='unpaid'`, orderID, amountCents); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoice_items SET description=$2,amount_cents=$3
WHERE invoice_id IN (SELECT id FROM invoices WHERE order_id=$1 AND status='unpaid')`, orderID, description, amountCents); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AdminMarkArtificialOrderPaid 后台「标记支付」：可选优先扣除用户余额
// （余额不足时扣可用部分），余下记为线下收款；订单直接置为已完成，不开通服务。
// 返回本单实际扣除的余额（分）。
func (s *Store) AdminMarkArtificialOrderPaid(ctx context.Context, orderPublicID string, useCredit bool) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var orderID, userID, total int64
	var status, kind, currency string
	err = tx.QueryRow(ctx, `SELECT id,user_id,total_cents,status,kind,currency FROM orders WHERE public_id=$1 FOR UPDATE`, orderPublicID).
		Scan(&orderID, &userID, &total, &status, &kind, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if kind != "artificial" || status != "unpaid" {
		return 0, ErrInvalidState
	}
	credit := int64(0)
	if useCredit {
		var accountID, balance int64
		err = tx.QueryRow(ctx, `SELECT id,balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE`, userID, currency).
			Scan(&accountID, &balance)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		if err == nil && balance > 0 {
			credit = balance
			if credit > total {
				credit = total
			}
			if err := debitWalletTx(ctx, tx, userID, currency, credit, "order", orderPublicID, "markpaid-"+orderPublicID); err != nil {
				return 0, err
			}
		}
	}
	method := "manual"
	if credit > 0 && credit == total {
		method = "wallet"
	}
	outTrade := "M" + strings.ReplaceAll(orderPublicID, "-", "")
	raw := fmt.Sprintf(`{"kind":"order","method":"%s","marked_by_admin":true,"credit_used_cents":%d}`, method, credit)
	if _, err := tx.Exec(ctx, `INSERT INTO payments(user_id,order_id,method,transaction_id,amount_cents,currency,status,raw_payload)
VALUES($1,$2,$3,$4,$5,$6,'completed',$7::jsonb)`, userID, orderID, method, outTrade, total, currency, raw); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE payments SET status='failed',raw_payload=raw_payload||jsonb_build_object('superseded_by','mark-paid')
WHERE order_id=$1 AND status='pending'`, orderID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE order_id=$1 AND status='unpaid'`, orderID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='completed',paid_at=now(),updated_at=now() WHERE id=$1`, orderID); err != nil {
		return 0, err
	}
	// 发票费用单支付完成：把关联的发票申请推进到「待审核」（普通人工订单无关联申请时为空操作）。
	if err := advanceInvoiceFeeOrderTx(ctx, tx, orderID); err != nil {
		return 0, err
	}
	// 产品转移费用单支付完成：推进自助转移状态（store_product_divert.go）。
	if err := advanceDivertFeeOrderTx(ctx, tx, orderID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return credit, nil
}

// CancelArtificialOrders 批量删除（作废）未支付人工订单，返回实际删除数。
// 已支付订单不能删除（需要走退款），站内不做物理删除以保留账目。
func (s *Store) CancelArtificialOrders(ctx context.Context, orderPublicIDs []string) (int, error) {
	if len(orderPublicIDs) == 0 {
		return 0, nil
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	cancelled := 0
	for _, publicID := range orderPublicIDs {
		var orderID int64
		var status, kind string
		err := tx.QueryRow(ctx, `SELECT id,status,kind FROM orders WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&orderID, &status, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return cancelled, err
		}
		if kind != "artificial" || status != "unpaid" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE orders SET status='cancelled',cancelled_at=now(),updated_at=now() WHERE id=$1`, orderID); err != nil {
			return cancelled, err
		}
		if _, err := tx.Exec(ctx, `UPDATE invoices SET status='void' WHERE order_id=$1 AND status='unpaid'`, orderID); err != nil {
			return cancelled, err
		}
		if _, err := tx.Exec(ctx, `UPDATE payments SET status='failed',raw_payload=raw_payload||jsonb_build_object('voided_by','artificial-order-delete')
WHERE order_id=$1 AND status='pending'`, orderID); err != nil {
			return cancelled, err
		}
		cancelled++
	}
	if err := tx.Commit(ctx); err != nil {
		return cancelled, err
	}
	return cancelled, nil
}
