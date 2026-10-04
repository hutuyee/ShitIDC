// 客户组按产品差异定价（对应魔方 shd_user_product_bates）。
//
// 价格优先级（高 -> 低）：
//  1. user_product_prices 里「该用户所属组 + 该商品 + 该周期 + 该币种」的固定价
//  2. product_prices 的标价，再按组的 discount_percent 打折
//
// 关键决定：**固定价之上不再叠加组折扣**。固定价就是固定价，否则运营很难解释
// 「为什么标 50 元最后收了 45 元」，客户也会觉得被多扣钱。
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PriceQuote 是一次定价的完整结果，便于前台展示「原价多少、为什么便宜了」。
type PriceQuote struct {
	// UnitCents 是最终单价（已含所有优惠）。
	UnitCents int64 `json:"unit_cents"`
	// ListCents 是标价，用于展示划线价。
	ListCents int64 `json:"list_cents"`
	// DiscountCents 是省下的金额（ListCents - UnitCents）。
	DiscountCents int64 `json:"discount_cents"`
	// Source 说明这个价是怎么来的：group_product / group_discount / list。
	Source string `json:"source"`
	// GroupName 是命中的客户组名（无组时为空）。
	GroupName string `json:"group_name,omitempty"`
}

// 定价来源。
const (
	PriceSourceList          = "list"           // 标价
	PriceSourceGroupDiscount = "group_discount" // 标价 + 组折扣
	PriceSourceGroupProduct  = "group_product"  // 组专属固定价
)

// ListUserProductPrices 列出一个客户组的所有专属价。
func (s *Store) ListUserProductPrices(ctx context.Context, groupPublicID string) ([]GroupProductPrice, error) {
	rows, err := s.DB.Query(ctx, `SELECT upp.billing_cycle,upp.currency,upp.amount_cents,p.public_id::text,p.name
FROM user_product_prices upp
JOIN user_groups g ON g.id=upp.group_id
JOIN products p ON p.id=upp.product_id
WHERE g.public_id=$1
ORDER BY p.name,upp.billing_cycle,upp.currency`, groupPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupProductPrice{}
	for rows.Next() {
		var v GroupProductPrice
		if err := rows.Scan(&v.BillingCycle, &v.Currency, &v.AmountCents, &v.ProductID, &v.ProductName); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GroupProductPrice 是「某个组买某个商品」的专属价。
type GroupProductPrice struct {
	ProductID    string `json:"product_id"`
	ProductName  string `json:"product_name"`
	BillingCycle string `json:"billing_cycle"`
	Currency     string `json:"currency"`
	AmountCents  int64  `json:"amount_cents"`
}

// SetUserProductPrice 设置（或更新）某个组买某个商品的专属价。
func (s *Store) SetUserProductPrice(ctx context.Context, groupPublicID, productPublicID, billingCycle, currency string, amountCents int64) error {
	if amountCents < 0 {
		return fmt.Errorf("价格不能为负数")
	}
	billingCycle = strings.ToLower(strings.TrimSpace(billingCycle))
	if !ValidBillingCycle(billingCycle) {
		return fmt.Errorf("不支持的计费周期：%s", billingCycle)
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "CNY"
	}
	tag, err := s.DB.Exec(ctx, `INSERT INTO user_product_prices(group_id,product_id,billing_cycle,currency,amount_cents)
SELECT g.id,p.id,$3,$4,$5 FROM user_groups g, products p
WHERE g.public_id=$1 AND p.public_id=$2 AND p.deleted_at IS NULL
ON CONFLICT (group_id,product_id,billing_cycle,currency)
DO UPDATE SET amount_cents=excluded.amount_cents,updated_at=now()`,
		groupPublicID, productPublicID, billingCycle, currency, amountCents)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserProductPrice 删除一条专属价（删掉后回落到标价 + 组折扣）。
func (s *Store) DeleteUserProductPrice(ctx context.Context, groupPublicID, productPublicID, billingCycle, currency string) error {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "CNY"
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM user_product_prices upp
USING user_groups g, products p
WHERE upp.group_id=g.id AND upp.product_id=p.id
  AND g.public_id=$1 AND p.public_id=$2 AND upp.billing_cycle=$3 AND upp.currency=$4`,
		groupPublicID, productPublicID, strings.ToLower(strings.TrimSpace(billingCycle)), currency)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyGroupPrice 计算最终单价。纯函数，便于对优先级与取整写测试。
//
//	listCents       标价
//	groupDiscount   组折扣百分比（0~100，0 表示不打折）
//	overrideCents   组专属固定价；<0 表示没有专属价
func ApplyGroupPrice(listCents int64, groupDiscount int, overrideCents int64) PriceQuote {
	q := PriceQuote{ListCents: listCents, UnitCents: listCents, Source: PriceSourceList}
	if overrideCents >= 0 {
		// 专属固定价优先，且不再叠加组折扣。
		q.UnitCents = overrideCents
		q.Source = PriceSourceGroupProduct
	} else if groupDiscount > 0 {
		if groupDiscount > 100 {
			groupDiscount = 100
		}
		// 折扣金额四舍五入到分；100% 折扣就是 0 元。
		off := (listCents*int64(groupDiscount) + 50) / 100
		q.UnitCents = listCents - off
		if q.UnitCents < 0 {
			q.UnitCents = 0
		}
		q.Source = PriceSourceGroupDiscount
	}
	q.DiscountCents = q.ListCents - q.UnitCents
	if q.DiscountCents < 0 {
		// 专属价高于标价时，展示上不把「负优惠」当折扣。
		q.DiscountCents = 0
	}
	return q
}

// QuoteProductPrice 查出一个用户买某个商品某个周期的最终单价。
// 用户没有客户组时等价于标价。
func (s *Store) QuoteProductPrice(ctx context.Context, userID int64, productPublicID, billingCycle, currency string) (PriceQuote, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "CNY"
	}
	var listCents int64
	var discount int
	var groupName *string
	var override *int64

	const q = `SELECT pp.amount_cents,
  coalesce(ug.discount_percent,0),
  ug.name,
  (SELECT upp.amount_cents FROM user_product_prices upp
     WHERE upp.group_id=ug.id AND upp.product_id=p.id AND upp.billing_cycle=pp.billing_cycle AND upp.currency=pp.currency)
FROM product_prices pp
JOIN products p ON p.id=pp.product_id
LEFT JOIN users u ON u.id=$1
LEFT JOIN user_groups ug ON ug.id=u.user_group_id
WHERE p.public_id=$2 AND pp.billing_cycle=$3 AND pp.currency=$4 AND pp.active=true AND p.deleted_at IS NULL`

	err := s.DB.QueryRow(ctx, q, userID, productPublicID, strings.ToLower(strings.TrimSpace(billingCycle)), currency).
		Scan(&listCents, &discount, &groupName, &override)
	if errors.Is(err, pgx.ErrNoRows) {
		return PriceQuote{}, ErrNotFound
	}
	if err != nil {
		return PriceQuote{}, err
	}
	overrideCents := int64(-1)
	if override != nil {
		overrideCents = *override
	}
	quote := ApplyGroupPrice(listCents, discount, overrideCents)
	if groupName != nil {
		quote.GroupName = *groupName
	}
	return quote, nil
}
