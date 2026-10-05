package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 活动促销（对齐魔方 CBAP 插件 EventPromotion）。
//
// 与优惠券 / 代金券的区别：活动促销不需要用户输入任何码，满足条件的订单在
// 下单时**自动**享受折扣；同一订单只应用排序最靠前的一个命中活动（插件用
// 「置顶 / 置底」排序表达优先级）。percent = 按比例减免，reduce = 满減。

// Promotion 是一个活动定义（对外字段名与插件前端一致）。
type Promotion struct {
	PublicID         string     `json:"id"`
	Name             string     `json:"name"`
	Type             string     `json:"type"`
	PercentValue     float64    `json:"percent_value"`
	FullCents        int64      `json:"full_cents"`
	ReduceCents      int64      `json:"reduce_cents"`
	StartAt          time.Time  `json:"start_at"`
	EndAt            *time.Time `json:"end_at"`
	ProductPublicIDs []string   `json:"products"`
	ClientType       string     `json:"client_type"`
	ClientPublicIDs  []string   `json:"clients"`
	NewUser          bool       `json:"new_user"`
	OldUser          bool       `json:"old_user"`
	SingleUserOnce   bool       `json:"single_user_once"`
	CycleLimit       bool       `json:"cycle_limit"`
	Cycle            []string   `json:"cycle"`
	Notes            string     `json:"notes"`
	Enabled          bool       `json:"enabled"`
	SortOrder        int        `json:"sort_order"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
}

// PromotionInput 是新增 / 修改活动的表单（金额与比例已换算成内部单位）。
type PromotionInput struct {
	Name             string
	Type             string
	PercentValue     float64
	FullCents        int64
	ReduceCents      int64
	StartAt          time.Time
	EndAt            *time.Time
	ProductPublicIDs []string
	ClientType       string
	ClientPublicIDs  []string
	NewUser          bool
	OldUser          bool
	SingleUserOnce   bool
	CycleLimit       bool
	Cycle            []string
	Notes            string
}

// PromotionConfig 对应插件 /event_promotion/config 的唯一配置项。
type PromotionConfig struct {
	DoesNotParticipate bool `json:"addon_event_promotion_does_not_participate"`
}

// promotionStatusOf 计算对外状态（与插件一致：Active/Pending/Expiration/Suspended）。
func promotionStatusOf(enabled bool, start time.Time, end *time.Time, now time.Time) string {
	switch {
	case !enabled:
		return "Suspended"
	case now.Before(start):
		return "Pending"
	case end != nil && now.After(*end):
		return "Expiration"
	default:
		return "Active"
	}
}

const promotionSelect = `SELECT p.public_id::text,p.name,p.type,p.percent_bp,p.full_cents,p.reduce_cents,p.start_at,p.end_at,
array(SELECT pp.public_id::text FROM products pp WHERE pp.id=ANY(p.product_ids) ORDER BY array_position(p.product_ids,pp.id)),
p.client_type,
array(SELECT u.public_id::text FROM users u WHERE u.id=ANY(p.client_ids) ORDER BY array_position(p.client_ids,u.id)),
p.new_user,p.old_user,p.single_user_once,p.cycle_limit,p.cycle,p.notes,p.enabled,p.sort_order,p.created_at
FROM promotions p`

func scanPromotion(row pgx.Row) (Promotion, error) {
	var v Promotion
	var percentBp int
	err := row.Scan(&v.PublicID, &v.Name, &v.Type, &percentBp, &v.FullCents, &v.ReduceCents,
		&v.StartAt, &v.EndAt, &v.ProductPublicIDs, &v.ClientType, &v.ClientPublicIDs,
		&v.NewUser, &v.OldUser, &v.SingleUserOnce, &v.CycleLimit, &v.Cycle, &v.Notes,
		&v.Enabled, &v.SortOrder, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	v.PercentValue = float64(percentBp) / 100
	v.Status = promotionStatusOf(v.Enabled, v.StartAt, v.EndAt, time.Now())
	return v, nil
}

// percentBp 把百分比（9.5）换算成基点（950）。
func percentBp(v float64) int {
	return int(math.Round(v * 100))
}

// normalizePromotionCycles 去重、把插件的 annually 归一为站内 yearly，并校验取值。
func normalizePromotionCycles(cycles []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range cycles {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "annually" {
			c = "yearly"
		}
		if c == "" || seen[c] {
			continue
		}
		if CycleMonths(c) <= 0 {
			return nil, fmt.Errorf("计费周期不合法：%s", c)
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

// normalizePromotionInput 校验表单并准备落库。
func normalizePromotionInput(in *PromotionInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("请填写活动名称")
	}
	switch in.Type {
	case "percent":
		if in.PercentValue <= 0 || in.PercentValue > 100 {
			return fmt.Errorf("折扣比例须在 0~100 之间")
		}
	case "reduce":
		if in.ReduceCents <= 0 {
			return fmt.Errorf("优惠金额必须大于 0")
		}
		if in.FullCents < 0 {
			return fmt.Errorf("达标金额不能为负数")
		}
	default:
		return fmt.Errorf("活动类型不合法")
	}
	if in.StartAt.IsZero() {
		in.StartAt = time.Now()
	}
	if in.EndAt != nil && !in.EndAt.After(in.StartAt) {
		return fmt.Errorf("截止时间必须晚于生效时间")
	}
	if in.ClientType == "" {
		in.ClientType = "all"
	}
	if in.ClientType != "all" && in.ClientType != "appoint" {
		return fmt.Errorf("适用用户类型不合法")
	}
	if in.ClientType == "appoint" && len(in.ClientPublicIDs) == 0 {
		return fmt.Errorf("请选择适用用户")
	}
	cycles, err := normalizePromotionCycles(in.Cycle)
	if err != nil {
		return err
	}
	in.Cycle = cycles
	if in.CycleLimit && len(in.Cycle) == 0 {
		return fmt.Errorf("开启周期限制后请选择计费周期")
	}
	in.Notes = strings.TrimSpace(in.Notes)
	return nil
}

func (s *Store) resolvePromotionProductIDs(ctx context.Context, q rowQuerier, publicIDs []string) ([]int64, error) {
	out := []int64{}
	for _, pid := range publicIDs {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		var id int64
		if err := q.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, pid).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("商品不存在")
			}
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *Store) resolvePromotionClientIDs(ctx context.Context, q rowQuerier, publicIDs []string) ([]int64, error) {
	out := []int64{}
	for _, uid := range publicIDs {
		uid = strings.TrimSpace(uid)
		if uid == "" {
			continue
		}
		var id int64
		if err := q.QueryRow(ctx, `SELECT id FROM users WHERE public_id=$1 AND deleted_at IS NULL`, uid).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("用户不存在")
			}
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// ListPromotions 分页列出活动：keywords 匹配名称 / 备注，status 过滤对外状态，
// at 过滤「活动时间包含该时刻」的活动（插件列表的 time 参数）。
func (s *Store) ListPromotions(ctx context.Context, keywords, status string, at *time.Time, limit, offset int) ([]Promotion, int64, error) {
	keywords = strings.TrimSpace(keywords)
	status = strings.TrimSpace(status)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where := `WHERE ($1='' OR p.name ILIKE '%'||$1||'%' OR p.notes ILIKE '%'||$1||'%')
  AND ($2='' OR (CASE WHEN NOT p.enabled THEN 'Suspended' WHEN now() < p.start_at THEN 'Pending' WHEN p.end_at IS NOT NULL AND now() > p.end_at THEN 'Expiration' ELSE 'Active' END)=$2)`
	args := []any{keywords, status}
	if at != nil {
		args = append(args, *at)
		where += fmt.Sprintf(` AND p.start_at <= $%d AND (p.end_at IS NULL OR p.end_at >= $%d)`, len(args), len(args))
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM promotions p `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.DB.Query(ctx, promotionSelect+` `+where+
		fmt.Sprintf(` ORDER BY p.sort_order, p.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Promotion{}
	for rows.Next() {
		v, err := scanPromotion(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// GetPromotion 按 public_id 读一个活动。
func (s *Store) GetPromotion(ctx context.Context, publicID string) (Promotion, error) {
	v, err := scanPromotion(s.DB.QueryRow(ctx, promotionSelect+` WHERE p.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Promotion{}, ErrNotFound
	}
	return v, err
}

// CreatePromotion 新增活动（排序排到最后）。
func (s *Store) CreatePromotion(ctx context.Context, in PromotionInput) (Promotion, error) {
	if err := normalizePromotionInput(&in); err != nil {
		return Promotion{}, err
	}
	productIDs, err := s.resolvePromotionProductIDs(ctx, s.DB, in.ProductPublicIDs)
	if err != nil {
		return Promotion{}, err
	}
	clientIDs, err := s.resolvePromotionClientIDs(ctx, s.DB, in.ClientPublicIDs)
	if err != nil {
		return Promotion{}, err
	}
	var publicID string
	err = s.DB.QueryRow(ctx, `INSERT INTO promotions(name,type,percent_bp,full_cents,reduce_cents,start_at,end_at,product_ids,client_type,client_ids,new_user,old_user,single_user_once,cycle_limit,cycle,notes,sort_order)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,COALESCE((SELECT max(sort_order)+1 FROM promotions),0)) RETURNING public_id::text`,
		in.Name, in.Type, percentBp(in.PercentValue), in.FullCents, in.ReduceCents, in.StartAt, in.EndAt, productIDs, in.ClientType, clientIDs,
		in.NewUser, in.OldUser, in.SingleUserOnce, in.CycleLimit, in.Cycle, in.Notes).Scan(&publicID)
	if err != nil {
		return Promotion{}, err
	}
	return s.GetPromotion(ctx, publicID)
}

// UpdatePromotion 修改活动（排序由 /order 接口单独维护）。
func (s *Store) UpdatePromotion(ctx context.Context, publicID string, in PromotionInput) error {
	if err := normalizePromotionInput(&in); err != nil {
		return err
	}
	productIDs, err := s.resolvePromotionProductIDs(ctx, s.DB, in.ProductPublicIDs)
	if err != nil {
		return err
	}
	clientIDs, err := s.resolvePromotionClientIDs(ctx, s.DB, in.ClientPublicIDs)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE promotions SET name=$2,type=$3,percent_bp=$4,full_cents=$5,reduce_cents=$6,start_at=$7,end_at=$8,product_ids=$9,client_type=$10,client_ids=$11,new_user=$12,old_user=$13,single_user_once=$14,cycle_limit=$15,cycle=$16,notes=$17,updated_at=now() WHERE public_id=$1`,
		publicID, in.Name, in.Type, percentBp(in.PercentValue), in.FullCents, in.ReduceCents, in.StartAt, in.EndAt, productIDs, in.ClientType, clientIDs,
		in.NewUser, in.OldUser, in.SingleUserOnce, in.CycleLimit, in.Cycle, in.Notes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPromotionEnabled 启用 / 停用活动。
func (s *Store) SetPromotionEnabled(ctx context.Context, publicID string, enabled bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE promotions SET enabled=$2,updated_at=now() WHERE public_id=$1`, publicID, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePromotion 删除活动（历史订单上的 promotion_id 置空保留）。
func (s *Store) DeletePromotion(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM promotions WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListActivePromotions 返回启用中的活动（按排序），供「置顶 / 置底」弹窗使用。
func (s *Store) ListActivePromotions(ctx context.Context) ([]Promotion, error) {
	rows, err := s.DB.Query(ctx, promotionSelect+` WHERE p.enabled=TRUE ORDER BY p.sort_order, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Promotion{}
	for rows.Next() {
		v, err := scanPromotion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReorderPromotions 按传入的 public_id 顺序重排 sort_order。
func (s *Store) ReorderPromotions(ctx context.Context, publicIDs []string) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for i, pid := range publicIDs {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE promotions SET sort_order=$2,updated_at=now() WHERE public_id=$1`, pid, i); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetPromotionConfig 读取插件配置（默认关闭）。
func (s *Store) GetPromotionConfig(ctx context.Context) (PromotionConfig, error) {
	var out PromotionConfig
	err := s.settingGet(ctx, "event_promotion", &out)
	if errors.Is(err, ErrNotFound) {
		return PromotionConfig{}, nil
	}
	return out, err
}

// SavePromotionConfig 保存插件配置。
func (s *Store) SavePromotionConfig(ctx context.Context, v PromotionConfig) error {
	return s.settingSave(ctx, "event_promotion", v)
}

// promotionEligibleUserTx 校验 new_user / old_user 开关。
// 两个开关都开启时两类用户都可参与；都关闭时不限制。
func promotionEligibleUserTx(ctx context.Context, q rowQuerier, userID int64, newUser, oldUser bool) (bool, error) {
	if !newUser && !oldUser {
		return true, nil
	}
	var hasPaid bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND status IN ('paid','processing','completed'))`, userID).Scan(&hasPaid); err != nil {
		return false, err
	}
	if newUser && oldUser {
		return true, nil
	}
	if newUser {
		return !hasPaid, nil
	}
	return hasPaid, nil
}

// promotionDiscountTx 在事务内挑选并计算自动促销折扣：
// 按排序取第一个「商品 / 周期 / 用户 / 次数」全部命中且优惠金额 > 0 的活动。
func (s *Store) promotionDiscountTx(ctx context.Context, tx pgx.Tx, userID int64, productID int64, cycle string, baseCents int64) (discount int64, promotionID int64, err error) {
	if baseCents <= 0 {
		return 0, 0, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,type,percent_bp,full_cents,reduce_cents,product_ids,client_type,client_ids,new_user,old_user,single_user_once,cycle_limit,cycle
FROM promotions WHERE enabled=TRUE AND now() >= start_at AND (end_at IS NULL OR now() <= end_at)
ORDER BY sort_order, id`)
	if err != nil {
		return 0, 0, err
	}
	type candidate struct {
		id                     int64
		ptype                  string
		percentBp              int
		full, reduce           int64
		productIDs, clientIDs  []int64
		clientType             string
		newUser, oldUser, once bool
		cycleLimit             bool
		cycles                 []string
	}
	list := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.ptype, &c.percentBp, &c.full, &c.reduce, &c.productIDs, &c.clientType, &c.clientIDs, &c.newUser, &c.oldUser, &c.once, &c.cycleLimit, &c.cycles); err != nil {
			rows.Close()
			return 0, 0, err
		}
		list = append(list, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, c := range list {
		if len(c.productIDs) > 0 && !int64InSlice(c.productIDs, productID) {
			continue
		}
		if c.cycleLimit && len(c.cycles) > 0 && !stringInSlice(c.cycles, cycle) {
			continue
		}
		if c.clientType == "appoint" && !int64InSlice(c.clientIDs, userID) {
			continue
		}
		ok, err := promotionEligibleUserTx(ctx, tx, userID, c.newUser, c.oldUser)
		if err != nil {
			return 0, 0, err
		}
		if !ok {
			continue
		}
		if c.once {
			var used bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE promotion_id=$1 AND user_id=$2 AND status <> 'cancelled')`, c.id, userID).Scan(&used); err != nil {
				return 0, 0, err
			}
			if used {
				continue
			}
		}
		d := int64(0)
		switch c.ptype {
		case "percent":
			d = int64(math.Round(float64(baseCents) * float64(c.percentBp) / 10000))
		case "reduce":
			if baseCents >= c.full {
				d = c.reduce
			}
		}
		if d <= 0 {
			continue
		}
		if d > baseCents {
			d = baseCents
		}
		return d, c.id, nil
	}
	return 0, 0, nil
}

// ListRunningPromotions 返回当前生效、且对指定用户可见（用户范围 / 新老客 / 单次
// 限制均通过）的活动，供前台展示「进行中的活动」。不做金额计算。
func (s *Store) ListRunningPromotions(ctx context.Context, userID int64) ([]Promotion, error) {
	rows, err := s.DB.Query(ctx, promotionSelect+`
WHERE p.enabled=TRUE AND now() >= p.start_at AND (p.end_at IS NULL OR now() <= p.end_at)
ORDER BY p.sort_order, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Promotion{}
	for rows.Next() {
		v, err := scanPromotion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	filtered := []Promotion{}
	for _, v := range out {
		if v.ClientType == "appoint" {
			hit := false
			for _, uid := range v.ClientPublicIDs {
				var id int64
				if err := s.DB.QueryRow(ctx, `SELECT id FROM users WHERE public_id=$1`, uid).Scan(&id); err != nil {
					continue
				}
				if id == userID {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		ok, err := promotionEligibleUserTx(ctx, s.DB, userID, v.NewUser, v.OldUser)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		filtered = append(filtered, v)
	}
	return filtered, nil
}
