package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 产品转移（对齐魔方 CBAP HostTransfer 插件）。
//
// 站内的「产品」即服务（services）。一次转移会把同一订单里仍属于原用户的
// 未删除服务一起迁移（魔方把这些同单商品称为「关联商品」）；订单、账单、
// 支付记录不迁移。VPC / 安全组 / 付费镜像在本站不存在，无需镜像同步。

var (
	// ErrTransferSelf 目标用户就是产品当前所有者。
	ErrTransferSelf = errors.New("cannot transfer to the current owner")
	// ErrTransferTerminated 已删除的产品不能转移。
	ErrTransferTerminated = errors.New("terminated service cannot be transferred")
)

// ServiceTransferLog 是一行转移记录。
type ServiceTransferLog struct {
	ID              int64     `json:"id"`
	ServicePublicID string    `json:"service_id"`
	ProductName     string    `json:"product_name"`
	ProductRef      string    `json:"product_ref"`
	FromUserID      int64     `json:"from_uid"`
	FromEmail       string    `json:"from_email"`
	ToUserID        int64     `json:"to_uid"`
	ToEmail         string    `json:"to_email"`
	OperatorEmail   string    `json:"operator"`
	Remark          string    `json:"remark"`
	CreatedAt       time.Time `json:"created_at"`
}

// ListServiceTransferLogs 按关键词分页读取转移记录。
// 关键词匹配商品名称 / 产品ID / 双方邮箱 / 备注。
func (s *Store) ListServiceTransferLogs(ctx context.Context, keyword string, limit, offset int) ([]ServiceTransferLog, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := `FROM service_transfers t
JOIN services s ON s.id = t.service_id
JOIN products p ON p.id = s.product_id
LEFT JOIN users fu ON fu.id = t.from_user_id
LEFT JOIN users tu ON tu.id = t.to_user_id
LEFT JOIN users ou ON ou.id = t.operator_id
WHERE ($1 = '' OR p.name ILIKE '%' || $1 || '%' OR s.public_id::text ILIKE $1 || '%'
       OR fu.email ILIKE '%' || $1 || '%' OR tu.email ILIKE '%' || $1 || '%'
       OR t.remark ILIKE '%' || $1 || '%')`
	rows, err := s.DB.Query(ctx, `SELECT t.id, s.public_id::text, p.name, COALESCE(p.provider_product_ref, ''),
 t.from_user_id, COALESCE(fu.email, ''), t.to_user_id, COALESCE(tu.email, ''),
 COALESCE(ou.email, ''), t.remark, t.created_at
`+where+`
ORDER BY t.id DESC LIMIT $2 OFFSET $3`, keyword, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ServiceTransferLog{}
	for rows.Next() {
		var v ServiceTransferLog
		if err := rows.Scan(&v.ID, &v.ServicePublicID, &v.ProductName, &v.ProductRef,
			&v.FromUserID, &v.FromEmail, &v.ToUserID, &v.ToEmail,
			&v.OperatorEmail, &v.Remark, &v.CreatedAt); err != nil {
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

// TransferServiceResult 描述一次转移的落库结果。
type TransferServiceResult struct {
	FromUserID int64 `json:"from_uid"`
	ToUserID   int64 `json:"to_uid"`
	Moved      int   `json:"moved"`
}

// TransferService 把服务（及其同订单未删除的关联服务）迁移到目标用户名下。
func (s *Store) TransferService(ctx context.Context, servicePublicID string, toUserID, operatorID int64, remark string) (TransferServiceResult, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return TransferServiceResult{}, err
	}
	defer tx.Rollback(ctx)

	var svcID, fromUserID, orderID int64
	var status string
	err = tx.QueryRow(ctx, `SELECT id, user_id, order_id, status FROM services WHERE public_id = $1 FOR UPDATE`, servicePublicID).
		Scan(&svcID, &fromUserID, &orderID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return TransferServiceResult{}, ErrNotFound
	}
	if err != nil {
		return TransferServiceResult{}, err
	}
	if fromUserID == toUserID {
		return TransferServiceResult{}, ErrTransferSelf
	}
	if status == "terminated" {
		return TransferServiceResult{}, ErrTransferTerminated
	}
	var targetEmail string
	err = tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1 AND status = 'active' AND deleted_at IS NULL`, toUserID).Scan(&targetEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return TransferServiceResult{}, ErrNotFound
	}
	if err != nil {
		return TransferServiceResult{}, err
	}

	// 同订单的关联产品一起迁移（不含已删除），与插件「关联商品自动迁移」一致。
	rows, err := tx.Query(ctx, `SELECT id FROM services WHERE order_id = $1 AND user_id = $2 AND status <> 'terminated' ORDER BY id`, orderID, fromUserID)
	if err != nil {
		return TransferServiceResult{}, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return TransferServiceResult{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TransferServiceResult{}, err
	}
	if len(ids) == 0 {
		return TransferServiceResult{}, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE services SET user_id = $1, updated_at = now() WHERE id = ANY($2)`, toUserID, ids); err != nil {
		return TransferServiceResult{}, err
	}
	remark = strings.TrimSpace(remark)
	if r := []rune(remark); len(r) > 200 {
		remark = string(r[:200])
	}
	if _, err := tx.Exec(ctx, `INSERT INTO service_transfers(service_id, from_user_id, to_user_id, operator_id, remark)
SELECT unnest($1::bigint[]), $2, $3, $4, $5`, ids, fromUserID, toUserID, operatorID, remark); err != nil {
		return TransferServiceResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TransferServiceResult{}, err
	}
	return TransferServiceResult{FromUserID: fromUserID, ToUserID: toUserID, Moved: len(ids)}, nil
}
