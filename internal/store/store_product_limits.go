package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 商品购买限制（对齐魔方 CBAP 插件 product_cert_limit / product_cycle_limit /
// product_related_limit）。
//
// 三个插件共享同一套「按商品配置限制 + 下单时校验」形态：
//   * 实名要求：指定商品只允许已实名用户购买（插件 type：1 个人/企业、2 个人、
//     3 企业；本站实名不区分个人/企业，三种取值都按「已通过实名」校验）；
//   * 周期性限购：指定商品在 N 天内最多拥有 num 件（cycle=0 视为永久）；
//   * 关联限购：捆绑（须与关联商品同单购买）/ 必需（需已拥有激活中的关联商品）/
//     互斥（不得拥有激活中的关联商品）。
//
// 计数口径与插件语言包一致：用户账户中的服务，状态 terminated / failed
//（对应插件的「已删除 / 已取消」）不计数，其余状态均计数。
// ProductNumLimit 插件（单客户数量限制）与站内商品自带的 max_per_customer
// 等价（见 checkStockAndQtyTx），不重复建表。

// ProductCertLimit 是一条商品实名要求。
type ProductCertLimit struct {
	PublicID        string    `json:"id"`
	ProductID       int64     `json:"-"`
	ProductPublicID string    `json:"product_id"`
	ProductName     string    `json:"product_name"`
	Type            int       `json:"type"`
	Status          bool      `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// ProductCycleLimit 是一条商品周期性限购。
type ProductCycleLimit struct {
	PublicID        string    `json:"id"`
	ProductID       int64     `json:"-"`
	ProductPublicID string    `json:"product_id"`
	ProductName     string    `json:"product_name"`
	Num             int       `json:"num"`
	Cycle           int       `json:"cycle"`
	Status          bool      `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// ProductRelatedLimit 是一条商品关联限购。
type ProductRelatedLimit struct {
	PublicID            string    `json:"id"`
	ProductID           int64     `json:"-"`
	ProductPublicID     string    `json:"product_id"`
	ProductName         string    `json:"product_name"`
	RelatedProductIDs   []int64   `json:"-"`
	RelatedPublicIDs    []string  `json:"related_product_ids"`
	RelatedProductNames string    `json:"related_product_names"`
	Type                int       `json:"type"`
	Status              bool      `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
}

const productCertLimitSelect = `SELECT l.public_id::text,l.product_id,p.public_id::text,p.name,l.type,l.status,l.created_at
FROM product_cert_limits l JOIN products p ON p.id=l.product_id`

const productCycleLimitSelect = `SELECT l.public_id::text,l.product_id,p.public_id::text,p.name,l.num,l.cycle,l.status,l.created_at
FROM product_cycle_limits l JOIN products p ON p.id=l.product_id`

const productRelatedLimitSelect = `SELECT l.public_id::text,l.product_id,p.public_id::text,p.name,l.related_product_ids,
array(SELECT rp.public_id::text FROM products rp WHERE rp.id=ANY(l.related_product_ids) ORDER BY array_position(l.related_product_ids,rp.id)),
(SELECT string_agg(rp.name,' / ' ORDER BY array_position(l.related_product_ids,rp.id)) FROM products rp WHERE rp.id=ANY(l.related_product_ids)),
l.type,l.status,l.created_at
FROM product_related_limits l JOIN products p ON p.id=l.product_id`

// ListProductCertLimits 列出全部商品实名要求。
func (s *Store) ListProductCertLimits(ctx context.Context) ([]ProductCertLimit, error) {
	rows, err := s.DB.Query(ctx, productCertLimitSelect+` ORDER BY l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductCertLimit{}
	for rows.Next() {
		var v ProductCertLimit
		if err := rows.Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.Type, &v.Status, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) getProductCertLimit(ctx context.Context, publicID string) (ProductCertLimit, error) {
	var v ProductCertLimit
	err := s.DB.QueryRow(ctx, productCertLimitSelect+` WHERE l.public_id=$1`, publicID).
		Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.Type, &v.Status, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProductCertLimit{}, ErrNotFound
	}
	return v, err
}

// CreateProductCertLimit 新增一条实名要求；同一商品只允许一条。
func (s *Store) CreateProductCertLimit(ctx context.Context, productPublicID string, typ int) (ProductCertLimit, error) {
	if typ < 1 || typ > 3 {
		return ProductCertLimit{}, fmt.Errorf("类型要求不合法")
	}
	var pid int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, productPublicID).Scan(&pid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProductCertLimit{}, ErrNotFound
		}
		return ProductCertLimit{}, err
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO product_cert_limits(product_id,type) VALUES($1,$2) RETURNING public_id::text`, pid, typ).Scan(&publicID)
	if err != nil {
		if isUniqueViolation(err) {
			return ProductCertLimit{}, fmt.Errorf("该商品已配置实名要求")
		}
		return ProductCertLimit{}, err
	}
	return s.getProductCertLimit(ctx, publicID)
}

// UpdateProductCertLimit 修改类型要求（商品不可改，与插件一致）。
func (s *Store) UpdateProductCertLimit(ctx context.Context, publicID string, typ int) error {
	if typ < 1 || typ > 3 {
		return fmt.Errorf("类型要求不合法")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE product_cert_limits SET type=$2,updated_at=now() WHERE public_id=$1`, publicID, typ)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProductCertLimitStatus 启用 / 停用实名要求。
func (s *Store) SetProductCertLimitStatus(ctx context.Context, publicID string, status bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE product_cert_limits SET status=$2,updated_at=now() WHERE public_id=$1`, publicID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProductCertLimit 删除实名要求。
func (s *Store) DeleteProductCertLimit(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM product_cert_limits WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListProductCycleLimits 列出全部周期性限购。
func (s *Store) ListProductCycleLimits(ctx context.Context) ([]ProductCycleLimit, error) {
	rows, err := s.DB.Query(ctx, productCycleLimitSelect+` ORDER BY l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductCycleLimit{}
	for rows.Next() {
		var v ProductCycleLimit
		if err := rows.Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.Num, &v.Cycle, &v.Status, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) getProductCycleLimit(ctx context.Context, publicID string) (ProductCycleLimit, error) {
	var v ProductCycleLimit
	err := s.DB.QueryRow(ctx, productCycleLimitSelect+` WHERE l.public_id=$1`, publicID).
		Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.Num, &v.Cycle, &v.Status, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProductCycleLimit{}, ErrNotFound
	}
	return v, err
}

// CreateProductCycleLimit 新增一条周期性限购；同一商品只允许一条。
func (s *Store) CreateProductCycleLimit(ctx context.Context, productPublicID string, num, cycle int) (ProductCycleLimit, error) {
	if num < 1 {
		return ProductCycleLimit{}, fmt.Errorf("限制数量至少为 1")
	}
	if cycle < 0 {
		return ProductCycleLimit{}, fmt.Errorf("限制周期不能为负数")
	}
	var pid int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, productPublicID).Scan(&pid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProductCycleLimit{}, ErrNotFound
		}
		return ProductCycleLimit{}, err
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO product_cycle_limits(product_id,num,cycle) VALUES($1,$2,$3) RETURNING public_id::text`, pid, num, cycle).Scan(&publicID)
	if err != nil {
		if isUniqueViolation(err) {
			return ProductCycleLimit{}, fmt.Errorf("该商品已配置周期性限购")
		}
		return ProductCycleLimit{}, err
	}
	return s.getProductCycleLimit(ctx, publicID)
}

// UpdateProductCycleLimit 修改限制数量与周期（商品不可改）。
func (s *Store) UpdateProductCycleLimit(ctx context.Context, publicID string, num, cycle int) error {
	if num < 1 {
		return fmt.Errorf("限制数量至少为 1")
	}
	if cycle < 0 {
		return fmt.Errorf("限制周期不能为负数")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE product_cycle_limits SET num=$2,cycle=$3,updated_at=now() WHERE public_id=$1`, publicID, num, cycle)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProductCycleLimitStatus 启用 / 停用周期性限购。
func (s *Store) SetProductCycleLimitStatus(ctx context.Context, publicID string, status bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE product_cycle_limits SET status=$2,updated_at=now() WHERE public_id=$1`, publicID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProductCycleLimit 删除周期性限购。
func (s *Store) DeleteProductCycleLimit(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM product_cycle_limits WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListProductRelatedLimits 列出全部关联限购。
func (s *Store) ListProductRelatedLimits(ctx context.Context) ([]ProductRelatedLimit, error) {
	rows, err := s.DB.Query(ctx, productRelatedLimitSelect+` ORDER BY l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductRelatedLimit{}
	for rows.Next() {
		var v ProductRelatedLimit
		if err := rows.Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.RelatedProductIDs,
			&v.RelatedPublicIDs, &v.RelatedProductNames, &v.Type, &v.Status, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) getProductRelatedLimit(ctx context.Context, publicID string) (ProductRelatedLimit, error) {
	var v ProductRelatedLimit
	err := s.DB.QueryRow(ctx, productRelatedLimitSelect+` WHERE l.public_id=$1`, publicID).
		Scan(&v.PublicID, &v.ProductID, &v.ProductPublicID, &v.ProductName, &v.RelatedProductIDs,
			&v.RelatedPublicIDs, &v.RelatedProductNames, &v.Type, &v.Status, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProductRelatedLimit{}, ErrNotFound
	}
	return v, err
}

// resolveRelatedProductIDs 把关联商品的 public_id 列表解析成内部 ID（保持输入顺序）。
func (s *Store) resolveRelatedProductIDs(ctx context.Context, productPublicID string, relatedPublicIDs []string) ([]int64, error) {
	var selfID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, productPublicID).Scan(&selfID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ids := make([]int64, 0, len(relatedPublicIDs))
	for _, rp := range relatedPublicIDs {
		rp = strings.TrimSpace(rp)
		if rp == "" {
			continue
		}
		var id int64
		if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, rp).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("关联商品不存在")
			}
			return nil, err
		}
		if id == selfID {
			return nil, fmt.Errorf("关联商品不能包含被限制商品自身")
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("请至少选择一个关联商品")
	}
	return ids, nil
}

// CreateProductRelatedLimit 新增一条关联限购。
func (s *Store) CreateProductRelatedLimit(ctx context.Context, productPublicID string, relatedPublicIDs []string, typ int) (ProductRelatedLimit, error) {
	if typ < 0 || typ > 2 {
		return ProductRelatedLimit{}, fmt.Errorf("限制类型不合法")
	}
	ids, err := s.resolveRelatedProductIDs(ctx, productPublicID, relatedPublicIDs)
	if err != nil {
		return ProductRelatedLimit{}, err
	}
	var pid int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, productPublicID).Scan(&pid); err != nil {
		return ProductRelatedLimit{}, err
	}
	var publicID string
	if err := s.DB.QueryRow(ctx, `INSERT INTO product_related_limits(product_id,related_product_ids,type) VALUES($1,$2,$3) RETURNING public_id::text`, pid, ids, typ).Scan(&publicID); err != nil {
		return ProductRelatedLimit{}, err
	}
	return s.getProductRelatedLimit(ctx, publicID)
}

// UpdateProductRelatedLimit 修改关联商品与类型（被限制商品不可改）。
func (s *Store) UpdateProductRelatedLimit(ctx context.Context, publicID string, relatedPublicIDs []string, typ int) error {
	if typ < 0 || typ > 2 {
		return fmt.Errorf("限制类型不合法")
	}
	var productPublicID string
	if err := s.DB.QueryRow(ctx, `SELECT p.public_id::text FROM product_related_limits l JOIN products p ON p.id=l.product_id WHERE l.public_id=$1`, publicID).Scan(&productPublicID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	ids, err := s.resolveRelatedProductIDs(ctx, productPublicID, relatedPublicIDs)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE product_related_limits SET related_product_ids=$2,type=$3,updated_at=now() WHERE public_id=$1`, publicID, ids, typ)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProductRelatedLimitStatus 启用 / 停用关联限购。
func (s *Store) SetProductRelatedLimitStatus(ctx context.Context, publicID string, status bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE product_related_limits SET status=$2,updated_at=now() WHERE public_id=$1`, publicID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProductRelatedLimit 删除关联限购。
func (s *Store) DeleteProductRelatedLimit(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM product_related_limits WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// rowQuerier 同时覆盖 *pgxpool.Pool 与 pgx.Tx（下单事务 / 普通查询共用校验逻辑）。
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// checkProductCertLimit 校验实名要求。
func checkProductCertLimit(ctx context.Context, q rowQuerier, userID, productID int64, productName string) error {
	var typ int
	err := q.QueryRow(ctx, `SELECT type FROM product_cert_limits WHERE product_id=$1 AND status=TRUE`, productID).Scan(&typ)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM certifications WHERE user_id=$1 AND status='approved')`, userID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("商品「%s」仅限已实名用户购买，请先完成实名认证", productName)
	}
	return nil
}

// cycleWindowCount 按插件口径计算「当前限制周期内」已拥有的数量：
// 周期开始时间以用户未在限制内下的第一单时间为准，逐段推进；
// cycle<=0 表示永久限制，直接返回总数。
func cycleWindowCount(times []time.Time, cycleDays int) int {
	if len(times) == 0 {
		return 0
	}
	if cycleDays <= 0 {
		return len(times)
	}
	start := times[0]
	count := 0
	for _, t := range times {
		if !t.Before(start.AddDate(0, 0, cycleDays)) {
			start = t
			count = 1
			continue
		}
		count++
	}
	return count
}

// checkProductCycleLimit 校验周期性限购。
func checkProductCycleLimit(ctx context.Context, q rowQuerier, userID, productID int64, productName string, quantity int) error {
	var num, cycle int
	err := q.QueryRow(ctx, `SELECT num,cycle FROM product_cycle_limits WHERE product_id=$1 AND status=TRUE`, productID).Scan(&num, &cycle)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := q.Query(ctx, `SELECT created_at FROM services WHERE user_id=$1 AND product_id=$2 AND status NOT IN ('terminated','failed') ORDER BY created_at`, userID, productID)
	if err != nil {
		return err
	}
	times := []time.Time{}
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		times = append(times, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	used := cycleWindowCount(times, cycle)
	if used+quantity > num {
		if cycle > 0 {
			return fmt.Errorf("商品「%s」每 %d 天限购 %d 件（当前周期已购 %d 件）", productName, cycle, num, used)
		}
		return fmt.Errorf("商品「%s」最多限购 %d 件（已购 %d 件）", productName, num, used)
	}
	return nil
}

// relatedLimitRow 是关联限购的校验输入。
type relatedLimitRow struct {
	Type int
	IDs  []int64
}

// checkProductRelatedOwnership 校验关联限购里的「必需 / 互斥」；
// 「捆绑」需要在整车维度校验，见 checkProductBundleLimits。
func checkProductRelatedOwnership(ctx context.Context, q rowQuerier, userID, productID int64, productName string) error {
	rows, err := q.Query(ctx, `SELECT type,related_product_ids FROM product_related_limits WHERE product_id=$1 AND status=TRUE AND type IN (1,2)`, productID)
	if err != nil {
		return err
	}
	list := []relatedLimitRow{}
	for rows.Next() {
		var v relatedLimitRow
		if err := rows.Scan(&v.Type, &v.IDs); err != nil {
			rows.Close()
			return err
		}
		if len(v.IDs) > 0 {
			list = append(list, v)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v := range list {
		var names string
		if err := q.QueryRow(ctx, `SELECT coalesce(string_agg(name,' / ' ORDER BY id),'') FROM products WHERE id=ANY($1)`, v.IDs).Scan(&names); err != nil {
			return err
		}
		if v.Type == 1 {
			var owned bool
			if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND status='active' AND product_id=ANY($2))`, userID, v.IDs).Scan(&owned); err != nil {
				return err
			}
			if !owned {
				return fmt.Errorf("购买商品「%s」前需先拥有激活中的关联商品：%s", productName, names)
			}
			continue
		}
		var ownedNames string
		if err := q.QueryRow(ctx, `SELECT coalesce(string_agg(DISTINCT p.name,' / '),'') FROM services s JOIN products p ON p.id=s.product_id WHERE s.user_id=$1 AND s.status='active' AND s.product_id=ANY($2)`, userID, v.IDs).Scan(&ownedNames); err != nil {
			return err
		}
		if ownedNames != "" {
			return fmt.Errorf("商品「%s」与已拥有的 %s 互斥，请先退订后再购买", productName, ownedNames)
		}
	}
	return nil
}

// checkProductPurchaseLimitsTx 在下单事务里执行实名 / 周期 / 关联（必需、互斥）校验。
func (s *Store) checkProductPurchaseLimitsTx(ctx context.Context, tx pgx.Tx, userID, productID int64, productName string, quantity int) error {
	if err := checkProductCertLimit(ctx, tx, userID, productID, productName); err != nil {
		return err
	}
	if err := checkProductCycleLimit(ctx, tx, userID, productID, productName, quantity); err != nil {
		return err
	}
	return checkProductRelatedOwnership(ctx, tx, userID, productID, productName)
}

// CheckProductBundleLimits 校验捆绑限制：配置了捆绑的商品必须与全部关联商品
// 在同一次购买里一起下单。productPublicIDs 是本次购买的商品 public_id 集合
// （单品下单时只有它自己，因此单品购买捆绑商品会明确提示去购物车一起结算）。
func (s *Store) CheckProductBundleLimits(ctx context.Context, productPublicIDs []string) error {
	return checkProductBundleLimits(ctx, s.DB, productPublicIDs)
}

func checkProductBundleLimits(ctx context.Context, q rowQuerier, productPublicIDs []string) error {
	if len(productPublicIDs) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT p.public_id::text,p.name,l.related_product_ids
FROM product_related_limits l JOIN products p ON p.id=l.product_id
WHERE l.status=TRUE AND l.type=0 AND p.public_id::text=ANY($1)`, productPublicIDs)
	if err != nil {
		return err
	}
	type bundleRow struct {
		Name string
		IDs  []int64
	}
	list := []bundleRow{}
	for rows.Next() {
		var publicID string
		var v bundleRow
		if err := rows.Scan(&publicID, &v.Name, &v.IDs); err != nil {
			rows.Close()
			return err
		}
		list = append(list, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v := range list {
		var missing string
		if err := q.QueryRow(ctx, `SELECT coalesce(string_agg(p.name,' / ' ORDER BY array_position($1::bigint[],p.id)),'') FROM products p
WHERE p.id=ANY($1::bigint[]) AND NOT (p.public_id::text=ANY($2::text[]))`, v.IDs, productPublicIDs).Scan(&missing); err != nil {
			return err
		}
		if missing != "" {
			return fmt.Errorf("商品「%s」须与「%s」同时购买，请一起加入购物车后结算", v.Name, missing)
		}
	}
	return nil
}
