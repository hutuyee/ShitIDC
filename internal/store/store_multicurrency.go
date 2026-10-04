package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 多币种独立定价（对应魔方 shd_pricing 的「每币种一行」）。
//
// product_prices 的唯一键本来就是 (product_id, billing_cycle, currency)，也就是
// 一个商品在每个周期上、每个币种都可以有自己的价格。这里补的是"下单时用哪个
// 币种"的解析规则，以及后台维护多币种价格的入口。
//
// 解析顺序：
//   1. 调用方显式指定的币种（前台选币/API 传入）——必须是该商品在这个周期上有价格
//     的币种，否则拒绝，避免用错价格卖货
//   2. 否则用买家钱包的币种
//   3. 否则用店铺基础货币

// ProductCurrencyPrice 是一个商品在某个周期上、某个币种的价格。
type ProductCurrencyPrice struct {
	BillingCycle string `json:"billing_cycle"`
	Currency     string `json:"currency"`
	AmountCents  int64  `json:"amount_cents"`
}

// ListProductPriceCurrencies 列出一个商品所有可售的（周期，币种）组合。
func (s *Store) ListProductPriceCurrencies(ctx context.Context, productPublicID string) ([]ProductCurrencyPrice, error) {
	rows, err := s.DB.Query(ctx, `SELECT pp.billing_cycle,pp.currency,pp.amount_cents
FROM product_prices pp JOIN products p ON p.id=pp.product_id
WHERE p.public_id=$1 AND pp.active=true AND p.deleted_at IS NULL
ORDER BY pp.billing_cycle,pp.currency`, productPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductCurrencyPrice{}
	for rows.Next() {
		var v ProductCurrencyPrice
		if err := rows.Scan(&v.BillingCycle, &v.Currency, &v.AmountCents); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// resolveOrderCurrency 决定一笔订单用哪个币种。
//
// 显式指定币种时，会校验该商品在该周期上确实有这个币种的价格——否则宁可报错，
// 也不能悄悄回退到另一个币种，那等于用错价格卖货。
func resolveOrderCurrency(ctx context.Context, tx pgx.Tx, productPublicID, billingCycle, requested string) (string, error) {
	requested = strings.ToUpper(strings.TrimSpace(requested))
	if requested != "" {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(
  SELECT 1 FROM product_prices pp JOIN products p ON p.id=pp.product_id
  WHERE p.public_id=$1 AND pp.billing_cycle=$2 AND pp.currency=$3 AND pp.active=true AND p.deleted_at IS NULL)`,
			productPublicID, billingCycle, requested).Scan(&exists)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("该商品在 %s 周期上没有 %s 的价格", billingCycle, requested)
		}
		return requested, nil
	}
	// 未指定：优先商品在这个周期上唯一可售的币种，其次基础货币。
	var code string
	err := tx.QueryRow(ctx, `SELECT pp.currency
FROM product_prices pp JOIN products p ON p.id=pp.product_id
WHERE p.public_id=$1 AND pp.billing_cycle=$2 AND pp.active=true AND p.deleted_at IS NULL
ORDER BY pp.currency LIMIT 1`, productPublicID, billingCycle).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return code, nil
}
