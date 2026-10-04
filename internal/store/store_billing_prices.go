package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// CreateProductWithPrices 建商品并一次性写入多个价格档与计费配置。
func (s *Store) CreateProductWithPrices(ctx context.Context, name, description, providerType, providerPublicID, providerRef, currency, groupPublicID string, billing ProductBillingInput, prices []PriceInput) (model.Product, error) {
	payType, err := normalizePayType(billing.PayType)
	if err != nil {
		return model.Product{}, err
	}
	if payType == "" {
		payType = PayTypeRecurring
	}
	if err := validatePrices(payType, prices); err != nil {
		return model.Product{}, err
	}
	if strings.TrimSpace(currency) == "" {
		currency = "CNY"
	}
	// 先用第一个价格档建商品（沿用既有逻辑），再补齐其余价格档。
	first := prices[0]
	prod, err := s.CreateProductInGroup(ctx, name, description, providerType, providerPublicID, providerRef, first.BillingCycle, currency, first.AmountCents, groupPublicID)
	if err != nil {
		return model.Product{}, err
	}
	// 按币种分组写入：调用方可以在同一次请求里给多个币种各配一套价。
	byCurrency := map[string][]PriceInput{}
	for _, pr := range prices {
		cur := strings.ToUpper(strings.TrimSpace(pr.Currency))
		if cur == "" {
			cur = currency
		}
		byCurrency[cur] = append(byCurrency[cur], PriceInput{BillingCycle: pr.BillingCycle, AmountCents: pr.AmountCents})
	}
	for cur, group := range byCurrency {
		if err := s.SetProductPrices(ctx, prod.PublicID, cur, group); err != nil {
			return model.Product{}, err
		}
	}
	billing.PayType = payType
	if err := s.UpdateProductBilling(ctx, prod.PublicID, billing); err != nil {
		return model.Product{}, err
	}
	return s.GetProductByPublicID(ctx, prod.PublicID)
}

// SetProductPrices 覆盖一个商品**在某个币种下**的价格档：该币种里未提交的周期标为失效。
// 多币种独立定价的关键点：只动指定币种的行，绝不能把其它币种的价格一起下架——
// 否则给商品加一个美元价就会把它的人民币价也删掉。
func (s *Store) SetProductPrices(ctx context.Context, productPublicID, currency string, prices []PriceInput) error {
	if len(prices) == 0 {
		return fmt.Errorf("请至少设置一个价格档")
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "CNY"
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var productID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, productPublicID).Scan(&productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE product_prices SET active=FALSE WHERE product_id=$1 AND currency=$2`, productID, currency); err != nil {
		return err
	}
	for _, pr := range prices {
		if _, err := tx.Exec(ctx, `INSERT INTO product_prices(product_id,billing_cycle,currency,amount_cents,active) VALUES($1,$2,$3,$4,TRUE) ON CONFLICT (product_id,billing_cycle,currency) DO UPDATE SET amount_cents=excluded.amount_cents,active=TRUE`, productID, strings.TrimSpace(strings.ToLower(pr.BillingCycle)), currency, pr.AmountCents); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetProductByPublicID 读回一个商品的展示信息与计费配置。
func (s *Store) GetProductByPublicID(ctx context.Context, productPublicID string) (model.Product, error) {
	var v model.Product
	err := s.DB.QueryRow(ctx, `SELECT p.id,p.public_id::text,p.name,p.description,coalesce(pr.public_id::text,''),coalesce(pr.name,''),p.provider_type,p.active,pp.amount_cents,pp.currency,pp.billing_cycle,p.created_at,p.pay_type,p.trial_days,p.trial_price_cents,p.auto_terminate_days FROM products p JOIN product_prices pp ON pp.product_id=p.id AND pp.active=true LEFT JOIN providers pr ON pr.id=p.provider_id WHERE p.public_id=$1 AND p.deleted_at IS NULL ORDER BY pp.id LIMIT 1`, productPublicID).
		Scan(&v.ID, &v.PublicID, &v.Name, &v.Description, &v.ProviderID, &v.ProviderName, &v.ProviderType, &v.Active, &v.PriceCents, &v.Currency, &v.BillingCycle, &v.CreatedAt, &v.PayType, &v.TrialDays, &v.TrialPriceCents, &v.AutoTerminateDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Product{}, ErrNotFound
	}
	if err != nil {
		return model.Product{}, err
	}
	stock, err := s.ProductStock(ctx, productPublicID)
	if err == nil {
		v.StockControl = stock.StockControl
		v.StockQty = stock.StockQty
		v.SoldCount = stock.SoldCount
		v.AllowQty = stock.AllowQty
		v.MaxPerCustomer = stock.MaxPerCustomer
		v.Available = stock.Available
	}
	return v, nil
}

// UpdateProductBilling 更新计费类型、试用、库存与限购。空 PayType 表示不改动。
func (s *Store) UpdateProductBilling(ctx context.Context, productPublicID string, in ProductBillingInput) error {
	payType, err := normalizePayType(in.PayType)
	if err != nil {
		return err
	}
	if in.TrialDays < 0 || in.TrialPriceCents < 0 || in.AutoTerminateDays < 0 || in.StockQty < 0 || in.MaxPerCustomer < 0 {
		return fmt.Errorf("天数、库存与限购次数不能为负数")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE products SET
pay_type=coalesce(nullif($2,''),pay_type),
trial_days=$3,trial_price_cents=$4,auto_terminate_days=$5,
stock_control=$6,stock_qty=$7,allow_qty=$8,max_per_customer=$9,is_featured=$10,
updated_at=now()
WHERE public_id=$1 AND deleted_at IS NULL`,
		productPublicID, payType, in.TrialDays, in.TrialPriceCents, in.AutoTerminateDays, in.StockControl, in.StockQty, in.AllowQty, in.MaxPerCustomer, in.IsFeatured)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
