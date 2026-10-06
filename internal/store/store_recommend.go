package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 推介计划（对齐魔方 CBAP IdcsmartRecommend 插件）。
//
// 与旧「推广返佣」（store_v2.go 的 referral_commissions，支付后按订单总额即时
// 返到余额）并存：当被推介用户的推荐人已开启推介计划时，走本模块的奖励记录
// （待确认 → 已确认 → 可提现），并跳过旧的即时返佣，避免同一订单重复发放。

var (
	// ErrRecommendNotPromoter 未开启推介计划。
	ErrRecommendNotPromoter = errors.New("推介计划未开启")
	// ErrRecommendWithdrawMin 未达到最低提现金额。
	ErrRecommendWithdrawMin = errors.New("未达到最低提现金额")
	// ErrRecommendInsufficient 可提现金额不足。
	ErrRecommendInsufficient = errors.New("可提现金额不足")
)

// ---- 配置 ----

// RecommendConfig 推介计划标量配置（金额单位：分）。
type RecommendConfig struct {
	AwardsCents         int64    `json:"awards_cents"`
	ConfirmDays         int      `json:"confirm_days"`
	WithdrawMinCents    int64    `json:"withdraw_min_cents"`
	WithdrawHandlingFee int      `json:"withdraw_handling_fee"`
	SystemURLs          []string `json:"system_urls"`
	DefaultURL          string   `json:"default_url"`
}

func defaultRecommendConfig() RecommendConfig {
	return RecommendConfig{AwardsCents: 0, ConfirmDays: 14}
}

// GetRecommendConfig 读取推介计划配置；缺省确认天数 14 天（0 表示即刻确认）。
func (s *Store) GetRecommendConfig(ctx context.Context) (RecommendConfig, error) {
	out := defaultRecommendConfig()
	err := s.settingGet(ctx, "recommend", &out)
	if errors.Is(err, ErrNotFound) {
		return defaultRecommendConfig(), nil
	}
	if err != nil {
		return defaultRecommendConfig(), err
	}
	if out.ConfirmDays < 0 {
		out.ConfirmDays = 0
	}
	if out.ConfirmDays > 365 {
		out.ConfirmDays = 365
	}
	if out.WithdrawMinCents < 0 {
		out.WithdrawMinCents = 0
	}
	if out.WithdrawHandlingFee < 0 {
		out.WithdrawHandlingFee = 0
	}
	if out.WithdrawHandlingFee > 100 {
		out.WithdrawHandlingFee = 100
	}
	return out, nil
}

// SaveRecommendConfig 保存推介计划配置。
func (s *Store) SaveRecommendConfig(ctx context.Context, in RecommendConfig) error {
	if in.AwardsCents < 0 {
		in.AwardsCents = 0
	}
	if in.ConfirmDays < 0 {
		in.ConfirmDays = 0
	}
	if in.ConfirmDays > 365 {
		in.ConfirmDays = 365
	}
	if in.WithdrawMinCents < 0 {
		in.WithdrawMinCents = 0
	}
	if in.WithdrawHandlingFee < 0 {
		in.WithdrawHandlingFee = 0
	}
	if in.WithdrawHandlingFee > 100 {
		in.WithdrawHandlingFee = 100
	}
	urls := make([]string, 0, len(in.SystemURLs))
	for _, u := range in.SystemURLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	in.SystemURLs = urls
	in.DefaultURL = strings.TrimSpace(in.DefaultURL)
	return s.settingSave(ctx, "recommend", in)
}

// ---- 商品奖励比例 ----

// RecommendRatio 单个商品的推介奖励规则：新购 / 续费两套比例与触发最低金额。
type RecommendRatio struct {
	ProductID        int64   `json:"product_id"`
	ProductName      string  `json:"product_name"`
	Ratio            float64 `json:"ratio"`
	AmountCents      int64   `json:"amount_cents"`
	RenewRatio       float64 `json:"renew_ratio"`
	RenewAmountCents int64   `json:"renew_amount_cents"`
}

func clampRecommendPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ListRecommendRatios 列出全部商品奖励比例（带商品名）。
func (s *Store) ListRecommendRatios(ctx context.Context) ([]RecommendRatio, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.product_id,p.name,r.ratio::float8,r.amount_cents,r.renew_ratio::float8,r.renew_amount_cents
FROM recommend_ratios r JOIN products p ON p.id=r.product_id ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecommendRatio{}
	for rows.Next() {
		var v RecommendRatio
		if err := rows.Scan(&v.ProductID, &v.ProductName, &v.Ratio, &v.AmountCents, &v.RenewRatio, &v.RenewAmountCents); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReplaceRecommendRatios 整体覆盖商品奖励比例：提交的商品集合之外的规则删除，
// 比例与最低金额均为 0 的行不落库（等于移除该商品）。
func (s *Store) ReplaceRecommendRatios(ctx context.Context, ratios []RecommendRatio) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ids := make([]int64, 0, len(ratios))
	for _, r := range ratios {
		if r.ProductID <= 0 || (r.Ratio <= 0 && r.RenewRatio <= 0) {
			continue
		}
		ids = append(ids, r.ProductID)
	}
	if len(ids) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM recommend_ratios`); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM recommend_ratios WHERE NOT (product_id = ANY($1))`, ids); err != nil {
		return err
	}
	for _, r := range ratios {
		if r.ProductID <= 0 || (r.Ratio <= 0 && r.RenewRatio <= 0) {
			continue
		}
		amount := r.AmountCents
		if amount < 0 {
			amount = 0
		}
		renewAmount := r.RenewAmountCents
		if renewAmount < 0 {
			renewAmount = 0
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recommend_ratios(product_id,ratio,amount_cents,renew_ratio,renew_amount_cents)
VALUES($1,$2,$3,$4,$5)
ON CONFLICT (product_id) DO UPDATE SET ratio=excluded.ratio,amount_cents=excluded.amount_cents,
  renew_ratio=excluded.renew_ratio,renew_amount_cents=excluded.renew_amount_cents,updated_at=now()`,
			r.ProductID, clampRecommendPercent(r.Ratio), amount, clampRecommendPercent(r.RenewRatio), renewAmount); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- 主打推介产品 ----

// RecommendProduct 主打推介产品（会员中心「建议推介产品」）。
type RecommendProduct struct {
	ProductID   int64   `json:"product_id"`
	ProductName string  `json:"product_name"`
	Ratio       float64 `json:"ratio"`
	Sort        int     `json:"sort"`
}

// ListRecommendProducts 列出主打推介产品（按后台排序，带新购奖励比例）。
func (s *Store) ListRecommendProducts(ctx context.Context) ([]RecommendProduct, error) {
	rows, err := s.DB.Query(ctx, `SELECT rp.product_id,p.name,COALESCE(r.ratio,0)::float8,rp.sort
FROM recommend_products rp JOIN products p ON p.id=rp.product_id
LEFT JOIN recommend_ratios r ON r.product_id=rp.product_id
ORDER BY rp.sort,rp.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecommendProduct{}
	for rows.Next() {
		var v RecommendProduct
		if err := rows.Scan(&v.ProductID, &v.ProductName, &v.Ratio, &v.Sort); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReplaceRecommendProducts 整体覆盖主打推介产品（按传入顺序写入 sort）。
func (s *Store) ReplaceRecommendProducts(ctx context.Context, productIDs []int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	seen := map[int64]bool{}
	clean := make([]int64, 0, len(productIDs))
	for _, id := range productIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	if len(clean) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM recommend_products`); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM recommend_products WHERE NOT (product_id = ANY($1))`, clean); err != nil {
		return err
	}
	for i, id := range clean {
		if _, err := tx.Exec(ctx, `INSERT INTO recommend_products(product_id,sort) VALUES($1,$2)
ON CONFLICT (product_id) DO UPDATE SET sort=excluded.sort`, id, i+1); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- 推介人 ----

// RecommendPromoter 推介人基础信息（金额单位：分；UserID 为 0 表示未开启）。
type RecommendPromoter struct {
	UserID            int64  `json:"user_id"`
	URL               string `json:"url"`
	WithdrawableCents int64  `json:"withdrawable_cents"`
	WithdrawnCents    int64  `json:"withdrawn_cents"`
	PendingCents      int64  `json:"pending_cents"`
	ActiveCents       int64  `json:"active_cents"`
	FrozenCents       int64  `json:"frozen_cents"`
}

// AutoConfirmRecommendAwards 把到期的待确认奖励转为已确认（确认天数 0 即创建时已确认）。
func (s *Store) AutoConfirmRecommendAwards(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `UPDATE recommend_awards SET status='Active',active_at=now(),updated_at=now()
WHERE status='Pending' AND confirm_at IS NOT NULL AND confirm_at <= now()`)
	return err
}

// EnableRecommendPromoter 开启推介计划；首次开启按配置发放初始奖励存款（幂等）。
// 返回是否首次开启。
func (s *Store) EnableRecommendPromoter(ctx context.Context, userID int64) (bool, error) {
	cfg, err := s.GetRecommendConfig(ctx)
	if err != nil {
		return false, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var existed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recommend_promoters WHERE user_id=$1)`, userID).Scan(&existed); err != nil {
		return false, err
	}
	if existed {
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recommend_promoters(user_id) VALUES($1)`, userID); err != nil {
		return false, err
	}
	if cfg.AwardsCents > 0 {
		status := "Pending"
		confirmAt := time.Now().Add(time.Duration(cfg.ConfirmDays) * 24 * time.Hour)
		var activeAt any
		if cfg.ConfirmDays <= 0 {
			status = "Active"
			confirmAt = time.Now()
			activeAt = time.Now()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recommend_awards(promoter_id,user_id,product_name,type,buy_amount_cents,awards_amount_cents,status,confirm_at,active_at,idempotency_key)
VALUES($1,$1,'初始奖励存款','init',0,$2,$3,$4,$5,$6)
ON CONFLICT (idempotency_key) DO NOTHING`, userID, cfg.AwardsCents, status, confirmAt, activeAt, fmt.Sprintf("recommend-init:%d", userID)); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RecommendPromoter 返回推介人概览；未开启时返回零值（UserID=0）。
// linkBase 为推介链接跳转页（配置 default_url，缺省由调用方给站内登录页）。
func (s *Store) RecommendPromoter(ctx context.Context, userID int64, linkBase string) (RecommendPromoter, error) {
	var opened bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recommend_promoters WHERE user_id=$1)`, userID).Scan(&opened); err != nil {
		return RecommendPromoter{}, err
	}
	if !opened {
		return RecommendPromoter{}, nil
	}
	if err := s.AutoConfirmRecommendAwards(ctx); err != nil {
		return RecommendPromoter{}, err
	}
	code, err := s.EnsureReferralCode(ctx, userID)
	if err != nil {
		return RecommendPromoter{}, err
	}
	out := RecommendPromoter{UserID: userID}
	if err := s.DB.QueryRow(ctx, `SELECT
 COALESCE(SUM(CASE WHEN status='Active' THEN awards_amount_cents ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status='Pending' THEN awards_amount_cents ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status='Frozen' THEN awards_amount_cents ELSE 0 END),0)
FROM recommend_awards WHERE promoter_id=$1`, userID).Scan(&out.ActiveCents, &out.PendingCents, &out.FrozenCents); err != nil {
		return RecommendPromoter{}, err
	}
	var withdrawn, locked int64
	if err := s.DB.QueryRow(ctx, `SELECT
 COALESCE(SUM(CASE WHEN status=3 THEN amount_cents ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status IN (0,1) THEN amount_cents ELSE 0 END),0)
FROM recommend_withdrawals WHERE promoter_id=$1`, userID).Scan(&withdrawn, &locked); err != nil {
		return RecommendPromoter{}, err
	}
	out.WithdrawnCents = withdrawn
	out.URL = RecommendLinkURL(linkBase, code, "")
	out.WithdrawableCents = out.ActiveCents - withdrawn - locked
	if out.WithdrawableCents < 0 {
		out.WithdrawableCents = 0
	}
	return out, nil
}

// ---- 自定义推介链接 ----

// RecommendLink 推介人自定义链接。
type RecommendLink struct {
	ID        int64     `json:"id"`
	SystemURL string    `json:"system_url"`
	CustomURL string    `json:"custom_url"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// RecommendLinkURL 拼接带推介码的链接；自定义后缀仅作来源标记（from）。
func RecommendLinkURL(base, code, custom string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "/login"
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	out := base + sep + "ref=" + url.QueryEscape(strings.ToLower(strings.TrimSpace(code)))
	if c := strings.TrimSpace(custom); c != "" {
		out += "&from=" + url.QueryEscape(c)
	}
	return out
}

// ListRecommendLinks 列出推介人的自定义链接（含拼好的完整链接）。
func (s *Store) ListRecommendLinks(ctx context.Context, userID int64, linkBase string) ([]RecommendLink, error) {
	code, err := s.EnsureReferralCode(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id,system_url,custom_url,created_at FROM recommend_links WHERE promoter_id=$1 ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecommendLink{}
	for rows.Next() {
		var v RecommendLink
		if err := rows.Scan(&v.ID, &v.SystemURL, &v.CustomURL, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.URL = RecommendLinkURL(v.SystemURL, code, v.CustomURL)
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateRecommendLink 生成自定义链接（同一系统页面 + 后缀重复时只刷新时间）。
func (s *Store) CreateRecommendLink(ctx context.Context, userID int64, systemURL, customURL string) (RecommendLink, error) {
	code, err := s.EnsureReferralCode(ctx, userID)
	if err != nil {
		return RecommendLink{}, err
	}
	var out RecommendLink
	err = s.DB.QueryRow(ctx, `INSERT INTO recommend_links(promoter_id,system_url,custom_url) VALUES($1,$2,$3)
ON CONFLICT (promoter_id,system_url,custom_url) DO UPDATE SET created_at=now()
RETURNING id,system_url,custom_url,created_at`, userID, systemURL, customURL).Scan(&out.ID, &out.SystemURL, &out.CustomURL, &out.CreatedAt)
	if err != nil {
		return RecommendLink{}, err
	}
	out.URL = RecommendLinkURL(out.SystemURL, code, out.CustomURL)
	return out, nil
}

// DeleteRecommendLink 删除自己的自定义链接。
func (s *Store) DeleteRecommendLink(ctx context.Context, userID, id int64) (bool, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM recommend_links WHERE id=$1 AND promoter_id=$2`, id, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- 奖励记录 ----

// RecommendAward 奖励记录（SurplusSeconds 为待确认剩余秒数，已确认/冻结/无效为 0）。
type RecommendAward struct {
	ID                int64     `json:"id"`
	PublicID          string    `json:"public_id"`
	PromoterID        int64     `json:"promoter_id"`
	Promoter          string    `json:"promoter"`
	Username          string    `json:"username"`
	ProductID         int64     `json:"product_id"`
	ProductName       string    `json:"product_name"`
	Type              string    `json:"type"`
	BuyAmountCents    int64     `json:"buy_amount_cents"`
	Ratio             float64   `json:"ratio"`
	AwardsAmountCents int64     `json:"awards_amount_cents"`
	Status            string    `json:"status"`
	InvalidReason     string    `json:"invalid_reason"`
	SurplusSeconds    int64     `json:"surplus_seconds"`
	CreatedAt         time.Time `json:"created_at"`
}

// RecommendAwardFilter 奖励记录筛选（后台）。
type RecommendAwardFilter struct {
	PromoterID int64
	ClientID   int64
	ProductID  int64
	Status     string
	Query      string
	Page       int
	Limit      int
	OrderBy    string
	Sort       string
}

const recommendAwardSelect = `SELECT a.id,a.public_id::text,a.promoter_id,u1.email,COALESCE(u2.email,''),
 a.product_id,a.product_name,a.type,a.buy_amount_cents,a.ratio::float8,a.awards_amount_cents,
 a.status,a.invalid_reason,a.confirm_at,a.created_at
FROM recommend_awards a
JOIN users u1 ON u1.id=a.promoter_id
LEFT JOIN users u2 ON u2.id=a.user_id`

func scanRecommendAwardRow(scan func(dest ...any) error) (RecommendAward, error) {
	var v RecommendAward
	var confirmAt *time.Time
	if err := scan(&v.ID, &v.PublicID, &v.PromoterID, &v.Promoter, &v.Username, &v.ProductID, &v.ProductName,
		&v.Type, &v.BuyAmountCents, &v.Ratio, &v.AwardsAmountCents, &v.Status, &v.InvalidReason, &confirmAt, &v.CreatedAt); err != nil {
		return RecommendAward{}, err
	}
	if v.Status == "Pending" && confirmAt != nil {
		if secs := int64(time.Until(*confirmAt).Seconds()); secs > 0 {
			v.SurplusSeconds = secs
		}
	}
	return v, nil
}

// ListPromoterRecommendAwards 推介人自己的奖励记录（分页）。
func (s *Store) ListPromoterRecommendAwards(ctx context.Context, promoterID int64, status string, page, limit int) ([]RecommendAward, int, error) {
	if err := s.AutoConfirmRecommendAwards(ctx); err != nil {
		return nil, 0, err
	}
	where := `WHERE a.promoter_id=$1`
	args := []any{promoterID}
	if status != "" {
		args = append(args, status)
		where += fmt.Sprintf(" AND a.status=$%d", len(args))
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM recommend_awards a `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, (page-1)*limit)
	rows, err := s.DB.Query(ctx, recommendAwardSelect+` `+where+fmt.Sprintf(" ORDER BY a.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RecommendAward{}
	for rows.Next() {
		v, err := scanRecommendAwardRow(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// ListAdminRecommendAwards 后台奖励记录（推介人 / 被推介人 / 商品 / 状态 / 关键字筛选）。
func (s *Store) ListAdminRecommendAwards(ctx context.Context, f RecommendAwardFilter) ([]RecommendAward, int, error) {
	if err := s.AutoConfirmRecommendAwards(ctx); err != nil {
		return nil, 0, err
	}
	conds := []string{"TRUE"}
	args := []any{}
	if f.PromoterID > 0 {
		args = append(args, f.PromoterID)
		conds = append(conds, fmt.Sprintf("a.promoter_id=$%d", len(args)))
	}
	if f.ClientID > 0 {
		args = append(args, f.ClientID)
		conds = append(conds, fmt.Sprintf("(a.promoter_id=$%d OR a.user_id=$%d)", len(args), len(args)))
	}
	if f.ProductID > 0 {
		args = append(args, f.ProductID)
		conds = append(conds, fmt.Sprintf("a.product_id=$%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("a.status=$%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+q+"%")
		conds = append(conds, fmt.Sprintf("(u1.email ILIKE $%d OR COALESCE(u2.email,'') ILIKE $%d)", len(args), len(args)))
	}
	where := "WHERE " + strings.Join(conds, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM recommend_awards a JOIN users u1 ON u1.id=a.promoter_id LEFT JOIN users u2 ON u2.id=a.user_id `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "a.id DESC"
	switch f.OrderBy {
	case "id", "created_at", "awards_amount", "buy_amount":
		col := map[string]string{"id": "a.id", "created_at": "a.created_at", "awards_amount": "a.awards_amount_cents", "buy_amount": "a.buy_amount_cents"}[f.OrderBy]
		dir := "DESC"
		if strings.EqualFold(f.Sort, "asc") {
			dir = "ASC"
		}
		order = col + " " + dir
	}
	args = append(args, f.Limit, (f.Page-1)*f.Limit)
	rows, err := s.DB.Query(ctx, recommendAwardSelect+` `+where+fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RecommendAward{}
	for rows.Next() {
		v, err := scanRecommendAwardRow(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// AdminSetRecommendAwardAmount 后台修改奖励金额（分）。
func (s *Store) AdminSetRecommendAwardAmount(ctx context.Context, id, amountCents int64) (bool, error) {
	if amountCents < 0 {
		amountCents = 0
	}
	tag, err := s.DB.Exec(ctx, `UPDATE recommend_awards SET awards_amount_cents=$2,updated_at=now() WHERE id=$1`, id, amountCents)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// AdminRecommendAwardOp 后台状态操作：confirm 确认 / frozen 冻结 / unfrozen 解冻 /
// invalid 置无效（需理由）。解冻时未到确认时间的记录回到待确认。
func (s *Store) AdminRecommendAwardOp(ctx context.Context, id int64, op, reason string) (bool, error) {
	var (
		tag interface{ RowsAffected() int64 }
		err error
	)
	switch op {
	case "confirm":
		tag, err = s.DB.Exec(ctx, `UPDATE recommend_awards SET status='Active',active_at=now(),updated_at=now() WHERE id=$1 AND status<>'Invalid'`, id)
	case "frozen":
		tag, err = s.DB.Exec(ctx, `UPDATE recommend_awards SET status='Frozen',updated_at=now() WHERE id=$1 AND status IN ('Pending','Active')`, id)
	case "unfrozen":
		tag, err = s.DB.Exec(ctx, `UPDATE recommend_awards SET
 status=CASE WHEN active_at IS NULL AND confirm_at IS NOT NULL AND confirm_at > now() THEN 'Pending' ELSE 'Active' END,
 active_at=CASE WHEN active_at IS NULL AND confirm_at IS NOT NULL AND confirm_at > now() THEN NULL ELSE COALESCE(active_at,now()) END,
 updated_at=now()
WHERE id=$1 AND status='Frozen'`, id)
	case "invalid":
		tag, err = s.DB.Exec(ctx, `UPDATE recommend_awards SET status='Invalid',invalid_reason=$2,updated_at=now() WHERE id=$1`, id, reason)
	default:
		return false, ErrInvalidState
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// AdminDeleteRecommendAward 后台删除奖励记录。
func (s *Store) AdminDeleteRecommendAward(ctx context.Context, id int64) (bool, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM recommend_awards WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- 奖励入账（支付成功后调用） ----

// AccrueRecommendAwards 为已支付订单生成推介奖励记录（幂等：同一订单 + 商品 + 类型只记一次）。
// 返回值 promoterID 非 0 表示被推介用户的推荐人已开启推介计划（调用方据此跳过旧的
// 即时返佣，避免重复发放）；created 为本轮新增的奖励条数。
func (s *Store) AccrueRecommendAwards(ctx context.Context, orderPublicID string) (int64, int64, error) {
	cfg, err := s.GetRecommendConfig(ctx)
	if err != nil {
		return 0, 0, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	var orderID, userID int64
	var kind string
	err = tx.QueryRow(ctx, `SELECT id,user_id,kind FROM orders WHERE public_id=$1 AND status IN ('paid','processing','completed')`, orderPublicID).Scan(&orderID, &userID, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if kind == "artificial" {
		// 人工订单（周期人工订单 / 发票费用）不产生推介奖励，也不改变旧的返佣口径。
		return 0, 0, nil
	}
	var referrer int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(referred_by,0) FROM users WHERE id=$1`, userID).Scan(&referrer); err != nil {
		return 0, 0, err
	}
	if referrer == 0 {
		return 0, 0, nil
	}
	var isPromoter bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recommend_promoters WHERE user_id=$1)`, referrer).Scan(&isPromoter); err != nil {
		return 0, 0, err
	}
	if !isPromoter {
		return 0, 0, nil
	}
	awardType := "new"
	if kind == "renewal" {
		awardType = "renew"
	}
	status := "Pending"
	var confirmAt, activeAt any
	if cfg.ConfirmDays <= 0 {
		status = "Active"
		activeAt = time.Now()
		confirmAt = time.Now()
	} else {
		confirmAt = time.Now().Add(time.Duration(cfg.ConfirmDays) * 24 * time.Hour)
	}
	type orderProduct struct {
		productID   int64
		productName string
		amountCents int64
	}
	items := []orderProduct{}
	rows, err := tx.Query(ctx, `SELECT product_id,MIN(product_name),SUM(subtotal_cents) FROM order_items WHERE order_id=$1 GROUP BY product_id`, orderID)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var it orderProduct
		if err := rows.Scan(&it.productID, &it.productName, &it.amountCents); err != nil {
			rows.Close()
			return 0, 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	var created int64
	for _, it := range items {
		var ratio, renewRatio float64
		var minAmount, renewMinAmount int64
		err := tx.QueryRow(ctx, `SELECT ratio::float8,amount_cents,renew_ratio::float8,renew_amount_cents FROM recommend_ratios WHERE product_id=$1`, it.productID).
			Scan(&ratio, &minAmount, &renewRatio, &renewMinAmount)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		useRatio, useMin := ratio, minAmount
		if awardType == "renew" {
			useRatio, useMin = renewRatio, renewMinAmount
		}
		if useRatio <= 0 || it.amountCents < useMin {
			continue
		}
		award := int64(math.Round(float64(it.amountCents) * useRatio / 100))
		if award <= 0 {
			continue
		}
		key := fmt.Sprintf("recommend:%s:%d:%s", orderPublicID, it.productID, awardType)
		tag, err := tx.Exec(ctx, `INSERT INTO recommend_awards(promoter_id,user_id,order_id,product_id,product_name,type,buy_amount_cents,ratio,awards_amount_cents,status,confirm_at,active_at,idempotency_key)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (idempotency_key) DO NOTHING`,
			referrer, userID, orderID, it.productID, it.productName, awardType, it.amountCents, useRatio, award, status, confirmAt, activeAt, key)
		if err != nil {
			return 0, 0, err
		}
		if tag.RowsAffected() > 0 {
			created++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return referrer, created, nil
}

// ---- 提现 ----

// ErrRecommendAmount 提现金额不合法。
var ErrRecommendAmount = errors.New("提现金额不合法")

// RecommendWithdrawal 提现记录：0 待审核 / 1 待打款 / 2 审核驳回 / 3 已打款。
type RecommendWithdrawal struct {
	ID          int64     `json:"id"`
	PublicID    string    `json:"public_id"`
	PromoterID  int64     `json:"promoter_id"`
	Promoter    string    `json:"promoter"`
	AmountCents int64     `json:"amount_cents"`
	FeeCents    int64     `json:"fee_cents"`
	Method      string    `json:"method"`
	Status      int       `json:"status"`
	Reason      string    `json:"reason"`
	CreatedAt   time.Time `json:"created_at"`
}

// ApplyRecommendWithdraw 申请提现：可提现金额 = 已确认奖励 - 已申请（含已打款）。
func (s *Store) ApplyRecommendWithdraw(ctx context.Context, userID, amountCents int64, method string) (RecommendWithdrawal, error) {
	if amountCents <= 0 {
		return RecommendWithdrawal{}, ErrRecommendAmount
	}
	cfg, err := s.GetRecommendConfig(ctx)
	if err != nil {
		return RecommendWithdrawal{}, err
	}
	if amountCents < cfg.WithdrawMinCents {
		return RecommendWithdrawal{}, ErrRecommendWithdrawMin
	}
	if err := s.AutoConfirmRecommendAwards(ctx); err != nil {
		return RecommendWithdrawal{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return RecommendWithdrawal{}, err
	}
	defer tx.Rollback(ctx)
	var lockedUser int64
	if err := tx.QueryRow(ctx, `SELECT user_id FROM recommend_promoters WHERE user_id=$1 FOR UPDATE`, userID).Scan(&lockedUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RecommendWithdrawal{}, ErrRecommendNotPromoter
		}
		return RecommendWithdrawal{}, err
	}
	var active, withdrawn, locked int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN status='Active' THEN awards_amount_cents ELSE 0 END),0)
FROM recommend_awards WHERE promoter_id=$1`, userID).Scan(&active); err != nil {
		return RecommendWithdrawal{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT
 COALESCE(SUM(CASE WHEN status=3 THEN amount_cents ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status IN (0,1) THEN amount_cents ELSE 0 END),0)
FROM recommend_withdrawals WHERE promoter_id=$1`, userID).Scan(&withdrawn, &locked); err != nil {
		return RecommendWithdrawal{}, err
	}
	available := active - withdrawn - locked
	if available < 0 {
		available = 0
	}
	if amountCents > available {
		return RecommendWithdrawal{}, ErrRecommendInsufficient
	}
	fee := amountCents * int64(cfg.WithdrawHandlingFee) / 100
	out := RecommendWithdrawal{AmountCents: amountCents, FeeCents: fee, Method: method, Status: 0}
	err = tx.QueryRow(ctx, `INSERT INTO recommend_withdrawals(promoter_id,amount_cents,fee_cents,method)
VALUES($1,$2,$3,$4) RETURNING id,public_id::text,created_at`, userID, amountCents, fee, method).Scan(&out.ID, &out.PublicID, &out.CreatedAt)
	if err != nil {
		return RecommendWithdrawal{}, err
	}
	out.PromoterID = userID
	if err := tx.Commit(ctx); err != nil {
		return RecommendWithdrawal{}, err
	}
	return out, nil
}

const recommendWithdrawalSelect = `SELECT w.id,w.public_id::text,w.promoter_id,u.email,w.amount_cents,w.fee_cents,w.method,w.status,w.reason,w.created_at
FROM recommend_withdrawals w JOIN users u ON u.id=w.promoter_id`

func (s *Store) queryRecommendWithdrawals(ctx context.Context, where string, args []any) ([]RecommendWithdrawal, error) {
	rows, err := s.DB.Query(ctx, recommendWithdrawalSelect+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecommendWithdrawal{}
	for rows.Next() {
		var v RecommendWithdrawal
		if err := rows.Scan(&v.ID, &v.PublicID, &v.PromoterID, &v.Promoter, &v.AmountCents, &v.FeeCents, &v.Method, &v.Status, &v.Reason, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListPromoterRecommendWithdrawals 推介人自己的提现记录。
func (s *Store) ListPromoterRecommendWithdrawals(ctx context.Context, promoterID int64, status string, page, limit int) ([]RecommendWithdrawal, int, error) {
	conds := []string{"w.promoter_id=$1"}
	args := []any{promoterID}
	if status != "" {
		n := recommendStatusParam(status)
		if n >= 0 {
			args = append(args, n)
			conds = append(conds, fmt.Sprintf("w.status=$%d", len(args)))
		}
	}
	where := "WHERE " + strings.Join(conds, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM recommend_withdrawals w `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, (page-1)*limit)
	items, err := s.queryRecommendWithdrawals(ctx, where+fmt.Sprintf(" ORDER BY w.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListAdminRecommendWithdrawals 后台提现记录（可按状态筛选）。
func (s *Store) ListAdminRecommendWithdrawals(ctx context.Context, status string, page, limit int) ([]RecommendWithdrawal, int, error) {
	conds := []string{"TRUE"}
	args := []any{}
	if status != "" {
		n := recommendStatusParam(status)
		if n >= 0 {
			args = append(args, n)
			conds = append(conds, fmt.Sprintf("w.status=$%d", len(args)))
		}
	}
	where := "WHERE " + strings.Join(conds, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM recommend_withdrawals w `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, (page-1)*limit)
	items, err := s.queryRecommendWithdrawals(ctx, where+fmt.Sprintf(" ORDER BY w.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// AdminSetRecommendWithdrawal 后台审核：1 通过（待打款）/ 2 驳回（释放金额）/ 3 已打款。
func (s *Store) AdminSetRecommendWithdrawal(ctx context.Context, id int64, status int, reason string) (bool, error) {
	if status != 1 && status != 2 && status != 3 {
		return false, ErrInvalidState
	}
	tag, err := s.DB.Exec(ctx, `UPDATE recommend_withdrawals SET status=$2,reason=$3,updated_at=now()
WHERE id=$1 AND status IN (0,1) AND status<>$2`, id, status, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- 预设无效回复 ----

// RecommendPrereply 预设无效回复；系统内置项（Status=Active）不可编辑 / 删除。
type RecommendPrereply struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	System    bool      `json:"system"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) ListRecommendPrereplies(ctx context.Context) ([]RecommendPrereply, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,content,system,created_at FROM recommend_prereplies ORDER BY system DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecommendPrereply{}
	for rows.Next() {
		var v RecommendPrereply
		if err := rows.Scan(&v.ID, &v.Content, &v.System, &v.CreatedAt); err != nil {
			return nil, err
		}
		if v.System {
			v.Status = "Active"
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateRecommendPrereply(ctx context.Context, content string) (RecommendPrereply, error) {
	var out RecommendPrereply
	err := s.DB.QueryRow(ctx, `INSERT INTO recommend_prereplies(content) VALUES($1) RETURNING id,content,system,created_at`, content).
		Scan(&out.ID, &out.Content, &out.System, &out.CreatedAt)
	return out, err
}

// UpdateRecommendPrereply 修改预设回复；内置项拒绝。
func (s *Store) UpdateRecommendPrereply(ctx context.Context, id int64, content string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE recommend_prereplies SET content=$2,updated_at=now() WHERE id=$1 AND system=FALSE`, id, content)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteRecommendPrereply 删除预设回复；内置项拒绝。
func (s *Store) DeleteRecommendPrereply(ctx context.Context, id int64) (bool, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM recommend_prereplies WHERE id=$1 AND system=FALSE`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// recommendStatusParam 解析状态筛选参数；空 / 非法返回 -1。
func recommendStatusParam(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// UserEmail 读取用户邮箱（推介奖励通知邮件用）。
func (s *Store) UserEmail(ctx context.Context, userID int64) (string, error) {
	var email string
	err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, userID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return email, err
}
