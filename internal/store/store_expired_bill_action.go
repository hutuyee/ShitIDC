package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 到期账单处理（对齐魔方主程序附属插件 expired_auto_delete_bill）。
//
// 插件主类与语言包 ionCube 加密，但 menu.php 与全部模板可读，契约取自这些文件：
//
//   - 设置页（setting.tpl）只有一个字段：「产品到期账单处理方式」——
//     无（空）/ delete（直接删除）/ cancel（标记取消）；
//   - 记录页（index.tpl）是「账单处理记录」列表：账单号（取消状态带链接）/
//     状态（着色）/ 处理时间 / 关联产品（domain + dedicatedip）。
//
// 行为对应：产品被终止（到期删除）时，把该服务未支付的**续费**订单与账单
// 一并了结并留档。站内账目不物理删除（§10.23 口径：订单 / 账单 / 支付记录
// 不物理删除），因此「直接删除」与「标记取消」的最终账面状态都是 void（作废），
// 配置里选的动作原样记进日志的 action 列，两种方式都不再把账单留给用户支付。
// 未处理的配置（action 为空）时整个钩子是空操作，默认行为与从前完全一致。

// ExpiredBillActionNone 等处理方式的合法值（对齐插件 setting.tpl 的下拉）。
const (
	ExpiredBillActionNone   = ""
	ExpiredBillActionDelete = "delete"
	ExpiredBillActionCancel = "cancel"
)

// ExpiredBillConfig 是到期账单处理设置（system_settings 键 expired_auto_delete_bill）。
type ExpiredBillConfig struct {
	Action string `json:"expired_bill_action"`
}

// GetExpiredBillConfig 读取设置；未配置时返回「无」（不处理）。
func (s *Store) GetExpiredBillConfig(ctx context.Context) (ExpiredBillConfig, error) {
	out := ExpiredBillConfig{Action: ExpiredBillActionNone}
	err := s.settingGet(ctx, "expired_auto_delete_bill", &out)
	if errors.Is(err, ErrNotFound) {
		return ExpiredBillConfig{Action: ExpiredBillActionNone}, nil
	}
	if err != nil {
		return ExpiredBillConfig{}, err
	}
	if out.Action != ExpiredBillActionDelete && out.Action != ExpiredBillActionCancel {
		out.Action = ExpiredBillActionNone
	}
	return out, nil
}

// SaveExpiredBillAction 保存处理方式；非法值被拒绝而不是静默归位。
func (s *Store) SaveExpiredBillAction(ctx context.Context, action string) error {
	if action != ExpiredBillActionNone && action != ExpiredBillActionDelete && action != ExpiredBillActionCancel {
		return ErrInvalidState
	}
	return s.settingSave(ctx, "expired_auto_delete_bill", ExpiredBillConfig{Action: action})
}

// ExpiredBillLog 是一行处理记录（对齐插件 index.tpl 的列表列）。
type ExpiredBillLog struct {
	ID            int64     `json:"id"`
	InvoiceID     string    `json:"invoice_id"`
	InvoiceStatus string    `json:"invoice_status"`
	Action        string    `json:"action"`
	ServiceID     string    `json:"service_id"`
	UserID        int64     `json:"user_uid"`
	UserEmail     string    `json:"user_email"`
	ProductName   string    `json:"product_name"`
	DedicatedIP   string    `json:"dedicated_ip"`
	CreatedAt     time.Time `json:"created_at"`
}

// RecordExpiredBillAction 在服务终止后按配置了结其未支付的续费订单 / 账单并留档。
// 由 worker 的 terminate 收尾（RecordExpiredIPLog 旁）调用；没有任何配置时是
// 空操作。失败只记日志，不影响终止本身（与到期 IP 记录同一口径）。
func (s *Store) RecordExpiredBillAction(ctx context.Context, servicePublicID string) error {
	cfg, err := s.GetExpiredBillConfig(ctx)
	if err != nil {
		return err
	}
	if cfg.Action == ExpiredBillActionNone {
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 服务快照（插件记录页的「关联产品」列：商品名 + 实例主 IP）。
	var svcID, userID int64
	var productName string
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT s.id, s.user_id, coalesce(oi.product_name, p.name, ''), s.provider_payload
FROM services s
JOIN products p ON p.id = s.product_id
LEFT JOIN order_items oi ON oi.id = s.order_item_id
WHERE s.public_id = $1`, servicePublicID).Scan(&svcID, &userID, &productName, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	data := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &data)
	}
	dedicated, _ := expiredIPsFromPayload(data)

	// 该服务名下仍未支付的续费订单 / 账单（到期后已不可续费，账单不可能再被支付）。
	rows, err := tx.Query(ctx, `SELECT i.id, i.public_id::text FROM invoices i
JOIN orders o ON o.id = i.order_id
WHERE o.renew_service_id = $1 AND o.kind = 'renewal' AND o.status = 'unpaid' AND i.status = 'unpaid'
FOR UPDATE OF i`, svcID)
	if err != nil {
		return err
	}
	type pending struct {
		id    int64
		pubID string
	}
	targets := []pending{}
	for rows.Next() {
		var t pending
		if err := rows.Scan(&t.id, &t.pubID); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range targets {
		if _, err := tx.Exec(ctx, `UPDATE invoices SET status='void' WHERE id=$1 AND status='unpaid'`, t.id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE orders SET status='cancelled', updated_at=now()
WHERE id = (SELECT order_id FROM invoices WHERE id=$1) AND status='unpaid'`, t.id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO expired_bill_logs(invoice_id, invoice_public_id, invoice_status, action, service_id, service_public_id, user_id, product_name, dedicated_ip)
VALUES($1, $2, 'void', $3, $4, $5, $6, $7, $8)`,
			t.id, t.pubID, cfg.Action, svcID, servicePublicID, userID, productName, dedicated); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListExpiredBillLogs 按关键词分页读取处理记录（关键词匹配账单号 / 产品 / IP / 用户邮箱）。
func (s *Store) ListExpiredBillLogs(ctx context.Context, keyword string, limit, offset int) ([]ExpiredBillLog, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := `FROM expired_bill_logs l
LEFT JOIN users u ON u.id = l.user_id
WHERE ($1 = '' OR l.invoice_public_id ILIKE '%' || $1 || '%' OR l.product_name ILIKE '%' || $1 || '%'
       OR l.dedicated_ip ILIKE '%' || $1 || '%' OR l.service_public_id ILIKE '%' || $1 || '%'
       OR u.email ILIKE '%' || $1 || '%')`
	rows, err := s.DB.Query(ctx, `SELECT l.id, l.invoice_public_id, l.invoice_status, l.action, l.service_public_id,
l.user_id, coalesce(u.email, ''), l.product_name, l.dedicated_ip, l.created_at
`+where+`
ORDER BY l.id DESC LIMIT $2 OFFSET $3`, keyword, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ExpiredBillLog{}
	for rows.Next() {
		var v ExpiredBillLog
		if err := rows.Scan(&v.ID, &v.InvoiceID, &v.InvoiceStatus, &v.Action, &v.ServiceID,
			&v.UserID, &v.UserEmail, &v.ProductName, &v.DedicatedIP, &v.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) `+where, keyword).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
