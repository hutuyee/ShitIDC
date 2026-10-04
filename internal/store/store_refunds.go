package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// ErrNotRefundable is returned when an order has no completed payment or is
// already refunded.
var ErrNotRefundable = errors.New("order is not refundable")

// GetOrderUser resolves the owner of an order public id.
func (s *Store) GetOrderUser(ctx context.Context, orderPublicID string) (int64, error) {
	var userID int64
	err := s.DB.QueryRow(ctx, `SELECT user_id FROM orders WHERE public_id=$1`, orderPublicID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return userID, err
}

// RefundOrder reverses a completed payment back to the user's wallet
// (冲正交易): the original debit/credit rows are never touched — a new
// 'refund' credit row records the correction, preserving full history.
//
// The whole operation is one serializable transaction: refund row, wallet
// credit, payment status, order status, invoice status and audit entry either
// all land or none do. The wallet idempotency key "refund:<txn>" also makes
// the refund itself idempotent at the database level.
func (s *Store) RefundOrder(ctx context.Context, actorID, userID int64, orderPublicID, reason, requestID string) (model.Refund, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.Refund{}, err
	}
	defer tx.Rollback(ctx)

	var orderID int64
	var status, currency string
	err = tx.QueryRow(ctx, `SELECT id,status,currency FROM orders WHERE public_id=$1 FOR UPDATE`, orderPublicID).Scan(&orderID, &status, &currency)
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
	var method, txnID string
	err = tx.QueryRow(ctx, `SELECT id,amount_cents,method,transaction_id FROM payments
WHERE order_id=$1 AND status='completed' ORDER BY id DESC LIMIT 1 FOR UPDATE`, orderID).Scan(&paymentID, &amount, &method, &txnID)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Refund{}, ErrNotRefundable
	}
	if err != nil {
		return model.Refund{}, err
	}
	if method != "wallet" {
		// Online (e.g. epay) refunds must be issued at the gateway; record the
		// bookkeeping here but credit nothing — see admin API docs.
		return model.Refund{}, fmt.Errorf("在线支付订单请先在支付网关侧退款，再使用钱包冲正或手工处理")
	}

	var accountID, balance int64
	if err := tx.QueryRow(ctx, `SELECT id,balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE`, userID, currency).Scan(&accountID, &balance); err != nil {
		return model.Refund{}, err
	}
	after := balance + amount

	var paymentPublic, refundPublic string
	if err := tx.QueryRow(ctx, `SELECT public_id::text FROM payments WHERE id=$1`, paymentID).Scan(&paymentPublic); err != nil {
		return model.Refund{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key)
VALUES($1,'refund',$2,$3,$4,$5,'refund',$6,$7,$8)`,
		accountID, amount, balance, after, currency, paymentPublic, "订单退款冲正："+reason, "refund:"+txnID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return model.Refund{}, ErrNotRefundable
		}
		return model.Refund{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE wallet_accounts SET balance_cents=$2,updated_at=now() WHERE id=$1`, accountID, after); err != nil {
		return model.Refund{}, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO refunds(payment_id,amount_cents,reason) VALUES($1,$2,$3) RETURNING public_id::text`, paymentID, amount, reason).Scan(&refundPublic); err != nil {
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
	before, _ := json.Marshal(map[string]any{"order_status": status, "payment_status": "completed", "balance_cents": balance})
	afterJSON, _ := json.Marshal(map[string]any{"order_status": "refunded", "payment_status": "refunded", "balance_cents": after, "reason": reason})
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	// before_data / after_data 是 jsonb，必须传 string 并显式 ::jsonb：
	// []byte 会被 pgx 当 bytea 编码，报 22P02。
	if _, err := tx.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,action,object_type,object_id,request_id,before_data,after_data) VALUES($1,'order.refund','order',$2,$3,$4::jsonb,$5::jsonb)`, actor, orderPublicID, requestID, string(before), string(afterJSON)); err != nil {
		return model.Refund{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Refund{}, err
	}
	return model.Refund{PublicID: refundPublic, OrderID: orderPublicID, PaymentTxn: txnID, AmountCents: amount, Currency: currency, Method: method, Reason: reason, CreatedAt: time.Now()}, nil
}

// ListRefunds returns the latest refunds for the admin console.
func (s *Store) ListRefunds(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT r.public_id::text,o.public_id::text,p.method,r.amount_cents,o.currency,r.reason,r.created_at
FROM refunds r JOIN payments p ON p.id=r.payment_id JOIN orders o ON o.id=p.order_id ORDER BY r.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, orderID, method, currency, reason string
		var amount int64
		var created time.Time
		if err := rows.Scan(&id, &orderID, &method, &amount, &currency, &reason, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "order_id": orderID, "method": method, "amount_cents": amount, "currency": currency, "reason": reason, "created_at": created})
	}
	return out, rows.Err()
}
