package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// 商品返现（对齐魔方 CBAP 插件 product_cashback）。
//
// 后台按商品配置一条返现规则（金额、期限、状态）；订单支付成功后
// PayProductCashback 按规则把金额返到买家余额，币种随订单。
// 幂等：同一订单只返一次（钱包流水 idempotency_key 唯一）。
// 期限口径：0=永久；N=购买后 N 天内（超期订单不返现）。

// ProductCashback 是一条商品返现规则。
type ProductCashback struct {
	PublicID        string    `json:"id"`
	ProductID       int64     `json:"product_id"`
	ProductPublicID string    `json:"product_public_id"`
	ProductName     string    `json:"product_name"`
	Type            string    `json:"type"`
	PriceCents      int64     `json:"price_cents"`
	PeriodDays      int       `json:"period_days"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
}

const cashbackSelect = `SELECT pc.public_id::text,pc.product_id,p.public_id::text,p.name,pc.type,pc.price_cents,pc.period_days,pc.active,pc.created_at
FROM product_cashbacks pc JOIN products p ON p.id=pc.product_id`

// ListProductCashbacks 列出全部返现规则（新建在前）。
func (s *Store) ListProductCashbacks(ctx context.Context) ([]ProductCashback, error) {
	rows, err := s.DB.Query(ctx, cashbackSelect+` ORDER BY pc.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductCashback{}
	for rows.Next() {
		var v ProductCashback
		if err := rows.Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.Type, &v.PriceCents, &v.PeriodDays, &v.Active, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) getProductCashback(ctx context.Context, publicID string) (ProductCashback, error) {
	var v ProductCashback
	err := s.DB.QueryRow(ctx, cashbackSelect+` WHERE pc.public_id=$1`, publicID).Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.Type, &v.PriceCents, &v.PeriodDays, &v.Active, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProductCashback{}, ErrNotFound
	}
	if err != nil {
		return ProductCashback{}, err
	}
	return v, nil
}

// CreateProductCashback 新增规则；同一商品只允许一条。
func (s *Store) CreateProductCashback(ctx context.Context, productPublicID, ptype string, priceCents int64, periodDays int, active bool) (ProductCashback, error) {
	var pid int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, productPublicID).Scan(&pid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProductCashback{}, ErrNotFound
		}
		return ProductCashback{}, err
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO product_cashbacks(product_id,type,price_cents,period_days,active) VALUES($1,$2,$3,$4,$5) RETURNING public_id::text`, pid, ptype, priceCents, periodDays, active).Scan(&publicID)
	if err != nil {
		if isUniqueViolation(err) {
			return ProductCashback{}, errors.New("该商品已配置返现规则")
		}
		return ProductCashback{}, err
	}
	return s.getProductCashback(ctx, publicID)
}

// UpdateProductCashback 修改返现类型、金额与期限。
func (s *Store) UpdateProductCashback(ctx context.Context, publicID, ptype string, priceCents int64, periodDays int) error {
	tag, err := s.DB.Exec(ctx, `UPDATE product_cashbacks SET type=$2,price_cents=$3,period_days=$4,updated_at=now() WHERE public_id=$1`, publicID, ptype, priceCents, periodDays)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProductCashbackStatus 启用/停用规则。
func (s *Store) SetProductCashbackStatus(ctx context.Context, publicID string, active bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE product_cashbacks SET active=$2,updated_at=now() WHERE public_id=$1`, publicID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProductCashback 删除规则。
func (s *Store) DeleteProductCashback(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM product_cashbacks WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PayProductCashback 在订单支付成功后按商品返现规则返现到买家余额。
//
// 口径：金额 = min(规则金额, 订单项小计)；多商品合并入账；同一订单只返一次。
// 该币种没有钱包账户（多币种用户）时跳过，不影响支付。
func (s *Store) PayProductCashback(ctx context.Context, orderPublicID string) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var orderID, userID int64
	var currency string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `SELECT id,user_id,currency,created_at FROM orders WHERE public_id=$1 AND status IN ('paid','processing','completed')`, orderPublicID).Scan(&orderID, &userID, &currency, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT oi.subtotal_cents,pc.price_cents,pc.period_days FROM order_items oi JOIN product_cashbacks pc ON pc.product_id=oi.product_id AND pc.active=TRUE WHERE oi.order_id=$1`, orderID)
	if err != nil {
		return 0, err
	}
	var total int64
	for rows.Next() {
		var subtotal, price int64
		var periodDays int
		if err := rows.Scan(&subtotal, &price, &periodDays); err != nil {
			rows.Close()
			return 0, err
		}
		if periodDays > 0 && time.Since(createdAt) > time.Duration(periodDays)*24*time.Hour {
			continue
		}
		amount := price
		if amount > subtotal {
			amount = subtotal
		}
		total += amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if total <= 0 {
		return 0, nil
	}
	var accountID, balance int64
	if err := tx.QueryRow(ctx, `SELECT id,balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE`, userID, currency).Scan(&accountID, &balance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	after := balance + total
	if _, err := tx.Exec(ctx, `UPDATE wallet_accounts SET balance_cents=$2,updated_at=now() WHERE id=$1`, accountID, after); err != nil {
		return 0, err
	}
	key := "cashback:" + orderPublicID
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key)
VALUES($1,'credit',$2,$3,$4,$5,'cashback',$6,'商品返现',$7)`, accountID, total, balance, after, currency, orderPublicID, key); err != nil {
		if isUniqueViolation(err) {
			return 0, nil
		}
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return total, nil
}
