package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- 免费 / 试用订单：0 元直接开通 ----

// FreeOrderResult 描述一笔免费订单结算后的结果。
type FreeOrderResult struct {
	ServiceIDs []string
	TrialEnds  *time.Time
}

// CompleteFreeOrder 结算一笔金额为 0 的订单（免费商品或 0 元试用）并生成服务。
// 免费/试用订单不该走支付网关：没有钱要收，走网关只会产生一堆 0 元交易记录。
// 这里直接把订单标记为已支付、创建服务，之后交给 worker 开通。
// 试用订单额外写入 trial_ends_at，Scheduler 据此到期回收。
func (s *Store) CompleteFreeOrder(ctx context.Context, userID int64, orderPublicID string) (FreeOrderResult, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return FreeOrderResult{}, err
	}
	defer tx.Rollback(ctx)

	var orderID, total int64
	var status, payType string
	err = tx.QueryRow(ctx, `SELECT id,total_cents,status,kind_detail FROM orders WHERE public_id=$1 AND user_id=$2 FOR UPDATE`, orderPublicID, userID).Scan(&orderID, &total, &status, &payType)
	if errors.Is(err, pgx.ErrNoRows) {
		return FreeOrderResult{}, ErrNotFound
	}
	if err != nil {
		return FreeOrderResult{}, err
	}
	if status != "unpaid" {
		return FreeOrderResult{}, ErrInvalidState
	}
	if total != 0 {
		return FreeOrderResult{}, fmt.Errorf("订单金额不为 0，请走支付流程")
	}
	if payType != PayTypeFree && payType != PayTypeTrial {
		return FreeOrderResult{}, fmt.Errorf("该订单不是免费或试用订单")
	}

	var trialEnds *time.Time
	if payType == PayTypeTrial {
		var days int
		if err := tx.QueryRow(ctx, `SELECT p.trial_days FROM products p JOIN order_items oi ON oi.product_id=p.id WHERE oi.order_id=$1 LIMIT 1`, orderID).Scan(&days); err != nil {
			return FreeOrderResult{}, err
		}
		if days <= 0 {
			return FreeOrderResult{}, fmt.Errorf("该试用商品未配置试用天数")
		}
		t := time.Now().AddDate(0, 0, days)
		trialEnds = &t
	}

	if _, err := tx.Exec(ctx, `UPDATE orders SET status='paid',updated_at=now() WHERE id=$1`, orderID); err != nil {
		return FreeOrderResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE order_id=$1`, orderID); err != nil {
		return FreeOrderResult{}, err
	}
	rows, err := tx.Query(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,status,provider_type,expires_at) SELECT $1,$2,oi.id,oi.product_id,'pending',oi.provider_type,$3 FROM order_items oi WHERE oi.order_id=$2 RETURNING public_id::text`, userID, orderID, trialEnds)
	if err != nil {
		return FreeOrderResult{}, err
	}
	serviceIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return FreeOrderResult{}, err
		}
		serviceIDs = append(serviceIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FreeOrderResult{}, err
	}
	if len(serviceIDs) == 0 {
		return FreeOrderResult{}, fmt.Errorf("订单没有可开通的服务")
	}
	if err := tx.Commit(ctx); err != nil {
		return FreeOrderResult{}, err
	}
	return FreeOrderResult{ServiceIDs: serviceIDs, TrialEnds: trialEnds}, nil
}

// ExpiredTrialServices 返回试用到期、需要回收的服务。
func (s *Store) ExpiredTrialServices(ctx context.Context, limit int) ([]ServiceRef, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,user_id,coalesce(provider_id,0),provider_type,coalesce(provider_ref,'') FROM services WHERE trial_ends_at IS NOT NULL AND trial_ends_at <= now() AND status IN ('active','suspended') ORDER BY trial_ends_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceRef{}
	for rows.Next() {
		var ref ServiceRef
		if err := rows.Scan(&ref.PublicID, &ref.UserID, &ref.ProviderID, &ref.ProviderType, &ref.ProviderRef); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
