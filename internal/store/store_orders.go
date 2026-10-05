package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// ListOrders returns the user's orders with product line items and the
// completed payment (method / channel) so the UI can show real order content.
func (s *Store) ListOrders(ctx context.Context, userID int64) ([]model.Order, error) {
	return s.listOrders(ctx, `WHERE o.user_id=$1`, 200, userID)
}

func (s *Store) ListOrdersAdmin(ctx context.Context, limit int) ([]model.Order, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.listOrders(ctx, ``, limit)
}

func (s *Store) listOrders(ctx context.Context, where string, limit int, args ...any) ([]model.Order, error) {
	args = append(args, limit)
	query := `SELECT o.id,o.public_id::text,o.user_id,o.status,o.kind,o.total_cents,o.currency,o.created_at,o.paid_at,o.cancelled_at,
COALESCE((SELECT json_agg(json_build_object('product_name',oi.product_name,'billing_cycle',oi.billing_cycle,'unit_price_cents',oi.unit_price_cents,'quantity',oi.quantity,'subtotal_cents',oi.subtotal_cents) ORDER BY oi.id)
 FROM order_items oi WHERE oi.order_id=o.id),'[]'::json),
(SELECT json_build_object('method',p.method,'type',p.raw_payload->>'pay_type','paid_at',p.created_at)
 FROM payments p WHERE p.order_id=o.id AND p.status='completed' ORDER BY p.id DESC LIMIT 1)
FROM orders o ` + where + ` ORDER BY o.created_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Order{}
	for rows.Next() {
		var o model.Order
		var items []byte
		var payment []byte
		if err := rows.Scan(&o.ID, &o.PublicID, &o.UserUID, &o.Status, &o.Kind, &o.TotalCents, &o.Currency, &o.CreatedAt, &o.PaidAt, &o.CancelledAt, &items, &payment); err != nil {
			return nil, err
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
	return out, rows.Err()
}

// cycleIntervalSQL maps a billing_cycle column to a PostgreSQL interval.
// Used everywhere service terms are extended or initialised.
const cycleIntervalSQL = `CASE COALESCE($1,'monthly')
 WHEN 'quarterly' THEN interval '3 months'
 WHEN 'semiannually' THEN interval '6 months'
 WHEN 'yearly' THEN interval '12 months'
 WHEN 'biennially' THEN interval '24 months'
 WHEN 'triennially' THEN interval '36 months'
 WHEN 'fourly' THEN interval '48 months'
 WHEN 'fively' THEN interval '60 months'
 WHEN 'sixly' THEN interval '72 months'
 WHEN 'sevenly' THEN interval '84 months'
 WHEN 'eightly' THEN interval '96 months'
 WHEN 'ninely' THEN interval '108 months'
 WHEN 'tenly' THEN interval '120 months'
 ELSE interval '1 month' END`

// CreateRenewalOrder builds an unpaid 'renewal' order for an existing,
// still-renewable service. On payment the service's expiry is extended
// instead of provisioning a new service row.
func (s *Store) CreateRenewalOrder(ctx context.Context, userID int64, servicePublicID string) (model.Order, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.Order{}, err
	}
	defer tx.Rollback(ctx)
	var serviceID, productID, unitPrice int64
	var cycle, productName, currency, serviceStatus, providerType string
	var providerID *int64
	var providerRef *string
	err = tx.QueryRow(ctx, `SELECT s.id,s.status,s.product_id,coalesce(oi.product_name,p.name),coalesce(oi.billing_cycle,'monthly'),
coalesce(oi.unit_price_cents,0),o.currency,oi.provider_id,coalesce(oi.provider_type,'manual'),oi.provider_product_ref
FROM services s
JOIN orders o ON o.id=s.order_id
JOIN products p ON p.id=s.product_id
LEFT JOIN order_items oi ON oi.id=s.order_item_id
WHERE s.public_id=$1 AND s.user_id=$2 FOR UPDATE OF s`, servicePublicID, userID).
		Scan(&serviceID, &serviceStatus, &productID, &productName, &cycle, &unitPrice, &currency, &providerID, &providerType, &providerRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Order{}, ErrNotFound
	}
	if err != nil {
		return model.Order{}, err
	}
	if serviceStatus != "active" && serviceStatus != "suspended" {
		return model.Order{}, ErrInvalidState
	}
	// 关联限购的「必需 / 互斥」在续费时同样生效（捆绑无法在单服务续费里满足，
	// 仅在购买时校验，见 CheckProductBundleLimits）。
	if err := checkProductRelatedOwnership(ctx, tx, userID, productID, productName); err != nil {
		return model.Order{}, err
	}
	// Prefer the current list price for the cycle; fall back to what the
	// user originally paid (e.g. custom imported products without prices).
	var price int64
	err = tx.QueryRow(ctx, `SELECT amount_cents FROM product_prices WHERE product_id=$1 AND billing_cycle=$2 AND active=true`, productID, cycle).Scan(&price)
	if errors.Is(err, pgx.ErrNoRows) {
		if unitPrice <= 0 {
			return model.Order{}, errors.New("该服务没有可用的续费价格，请联系管理员")
		}
		price = unitPrice
	} else if err != nil {
		return model.Order{}, err
	}
	var o model.Order
	if err := tx.QueryRow(ctx, `INSERT INTO orders(user_id,status,kind,total_cents,currency,renew_service_id) VALUES($1,'unpaid','renewal',$2,$3,$4)
RETURNING id,public_id::text,user_id,status,kind,total_cents,currency,created_at`, userID, price, currency, serviceID).
		Scan(&o.ID, &o.PublicID, &o.UserUID, &o.Status, &o.Kind, &o.TotalCents, &o.Currency, &o.CreatedAt); err != nil {
		return model.Order{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,unit_price_cents,quantity,subtotal_cents,provider_id,provider_type,provider_product_ref)
VALUES($1,$2,$3,$4,$5,1,$5,$6,$7,$8)`, o.ID, productID, productName+"（续费）", cycle, price, providerID, providerType, providerRef); err != nil {
		return model.Order{}, err
	}
	var invoiceID int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(order_id,user_id,status,total_cents,currency,due_at) VALUES($1,$2,'unpaid',$3,$4,now()+interval '24 hours') RETURNING id`, o.ID, userID, price, currency).Scan(&invoiceID); err != nil {
		return model.Order{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,amount_cents) VALUES($1,$2,$3)`, invoiceID, productName+" 续费 / "+cycle, price); err != nil {
		return model.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Order{}, err
	}
	return o, nil
}

// ExtendServiceOnRenewalPayment finalises a paid 'renewal' order: invoice
// paid, order completed and the target service's expiry extended from
// max(now, current expiry) by the ordered cycle. Returns the renewed
// service public id and false when the order is not an unpaid renewal.
func (s *Store) ExtendServiceOnRenewalPayment(ctx context.Context, tx pgx.Tx, orderID int64) (string, bool, error) {
	var kind, status, cycle string
	var renewServiceID *int64
	err := tx.QueryRow(ctx, `SELECT kind,status,coalesce((SELECT oi.billing_cycle FROM order_items oi WHERE oi.order_id=$1 ORDER BY oi.id LIMIT 1),'monthly'),renew_service_id
FROM orders WHERE id=$1`, orderID).Scan(&kind, &status, &cycle, &renewServiceID)
	if err != nil {
		return "", false, err
	}
	if kind != "renewal" {
		return "", false, nil
	}
	if renewServiceID != nil {
		if _, err := tx.Exec(ctx, `UPDATE services SET expires_at=GREATEST(COALESCE(expires_at,now()),now())+(`+cycleIntervalSQL+`),updated_at=now() WHERE id=$2`, cycle, *renewServiceID); err != nil {
			return "", true, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='completed',paid_at=now(),updated_at=now() WHERE id=$1`, orderID); err != nil {
		return "", true, err
	}
	var renewPublic string
	if renewServiceID != nil {
		_ = tx.QueryRow(ctx, `SELECT public_id::text FROM services WHERE id=$1`, *renewServiceID).Scan(&renewPublic)
	}
	return renewPublic, true, nil
}

// CancelOrder cancels the caller's own unpaid order and voids its invoice.
func (s *Store) CancelOrder(ctx context.Context, userID int64, orderPublicID string) (model.Order, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return model.Order{}, err
	}
	defer tx.Rollback(ctx)
	var o model.Order
	var items []byte
	err = tx.QueryRow(ctx, `UPDATE orders SET status='cancelled',cancelled_at=now(),updated_at=now()
WHERE public_id=$1 AND user_id=$2 AND status='unpaid' RETURNING id,public_id::text,user_id,status,total_cents,currency,created_at,
COALESCE((SELECT json_agg(json_build_object('product_name',oi.product_name,'billing_cycle',oi.billing_cycle,'unit_price_cents',oi.unit_price_cents,'quantity',oi.quantity,'subtotal_cents',oi.subtotal_cents) ORDER BY oi.id)
 FROM order_items oi WHERE oi.order_id=orders.id),'[]'::json)`,
		orderPublicID, userID).Scan(&o.ID, &o.PublicID, &o.UserUID, &o.Status, &o.TotalCents, &o.Currency, &o.CreatedAt, &items)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Order{}, ErrInvalidState
	}
	if err != nil {
		return model.Order{}, err
	}
	_ = json.Unmarshal(items, &o.Items)
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status='void' WHERE order_id=$1 AND status='unpaid'`, o.ID); err != nil {
		return model.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Order{}, err
	}
	return o, nil
}

// CancelExpiredOrders cancels unpaid orders older than the given hours
// (scheduler job) and voids their invoices. Returns affected order count.
func (s *Store) CancelExpiredOrders(ctx context.Context, hours int) (int64, error) {
	if hours <= 0 {
		hours = 24
	}
	tag, err := s.DB.Exec(ctx, `WITH expired AS (
 UPDATE orders SET status='cancelled',cancelled_at=now(),updated_at=now()
 WHERE status='unpaid' AND created_at < now()-make_interval(hours => $1) RETURNING id
), voided AS (
 UPDATE invoices i SET status='void' FROM expired e WHERE i.order_id=e.id AND i.status='unpaid' RETURNING i.id
)
SELECT count(*) FROM expired`, hours)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
