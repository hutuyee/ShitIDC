package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 代金券（对齐魔方 CBAP 插件 IdcsmartVoucher）。
//
// 与站内「优惠券」（码核销式折扣码）不同：代金券是发放 / 领取式的定额抵扣券，
// 公开券可被用户在前台领取，私有券只能由后台按用户发放；下单 / 续费 / 升降级时
// 按订单金额核销（抵扣金额不超过应付金额），并在 voucher_grants 里留下使用记录。
//
// 券码规则与插件一致：8 位且同时包含大写字母、小写字母与数字。
// 核销与站内优惠券保持同一口径：下单即核销（订单取消不返还）。

// Voucher 是一张代金券定义。
type Voucher struct {
	PublicID             string     `json:"id"`
	Code                 string     `json:"code"`
	PriceCents           int64      `json:"price_cents"`
	Type                 string     `json:"type"`
	Num                  int64      `json:"num"`
	StartAt              time.Time  `json:"start_at"`
	EndAt                *time.Time `json:"end_at"`
	ProductPublicIDs     []string   `json:"product"`
	ProductNeedPublicIDs []string   `json:"product_need"`
	MinAmountCents       int64      `json:"min_amount_cents"`
	UserType             string     `json:"user_type"`
	Onetime              bool       `json:"onetime"`
	UpgradeUse           bool       `json:"upgrade_use"`
	RenewUse             bool       `json:"renew_use"`
	Cycle                []string   `json:"cycle"`
	Notes                string     `json:"notes"`
	Enabled              bool       `json:"enabled"`
	Status               string     `json:"status"`
	ClaimCount           int64      `json:"claim_count"`
	UsedCount            int64      `json:"used_count"`
	CreatedAt            time.Time  `json:"created_at"`
}

// VoucherInput 是新增 / 修改代金券的表单。
type VoucherInput struct {
	Code                 string
	PriceCents           int64
	Type                 string
	Num                  int64
	StartAt              time.Time
	EndAt                *time.Time
	ProductPublicIDs     []string
	ProductNeedPublicIDs []string
	MinAmountCents       int64
	UserType             string
	Onetime              bool
	UpgradeUse           bool
	RenewUse             bool
	Cycle                []string
	Notes                string
}

// MyVoucher 是用户视角的一张代金券（一条领取 / 发放记录）。
type MyVoucher struct {
	PublicID       string     `json:"id"`
	VoucherID      string     `json:"voucher_id"`
	Code           string     `json:"code"`
	PriceCents     int64      `json:"price_cents"`
	MinAmountCents int64      `json:"min_amount_cents"`
	StartAt        time.Time  `json:"start_at"`
	EndAt          *time.Time `json:"end_at"`
	Notes          string     `json:"notes"`
	Source         string     `json:"source"`
	Used           bool       `json:"used"`
	OrderPublicID  string     `json:"order_id,omitempty"`
	UsedAt         *time.Time `json:"used_at,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
}

// ClaimableVoucher 是前台可领取的公开券。
type ClaimableVoucher struct {
	PublicID       string     `json:"id"`
	Code           string     `json:"code"`
	PriceCents     int64      `json:"price_cents"`
	MinAmountCents int64      `json:"min_amount_cents"`
	StartAt        time.Time  `json:"start_at"`
	EndAt          *time.Time `json:"end_at"`
	Notes          string     `json:"notes"`
	Remaining      int64      `json:"remaining"`
}

// VoucherRecord 是一条代金券领取 / 使用记录（后台使用记录列表）。
type VoucherRecord struct {
	PublicID      string     `json:"id"`
	VoucherID     string     `json:"voucher_id"`
	VoucherCode   string     `json:"voucher_code"`
	UserPublicID  string     `json:"user_id"`
	Username      string     `json:"username"`
	Phone         string     `json:"phone"`
	Email         string     `json:"email"`
	Source        string     `json:"source"`
	Used          bool       `json:"used"`
	OrderPublicID string     `json:"order_id"`
	UsedAt        *time.Time `json:"used_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

// VoucherTime 是某用户在一张券上的已发放次数（发放弹窗展示用）。
type VoucherTime struct {
	UserPublicID string `json:"client_id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	Num          int64  `json:"num"`
}

// ValidVoucherCode 校验券码格式（与插件一致：8 位且包含大写、小写与数字）。
func ValidVoucherCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	var upper, lower, digit bool
	for _, r := range code {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			return false
		}
	}
	return upper && lower && digit
}

// voucherStatusOf 计算对外状态：disable / wait_effect / expired / enable。
func voucherStatusOf(enabled bool, start time.Time, end *time.Time, now time.Time) string {
	switch {
	case !enabled:
		return "disable"
	case now.Before(start):
		return "wait_effect"
	case end != nil && now.After(*end):
		return "expired"
	default:
		return "enable"
	}
}

const voucherSelect = `SELECT v.public_id::text,v.code,v.price_cents,v.type,v.num,v.start_at,v.end_at,
array(SELECT p.public_id::text FROM products p WHERE p.id=ANY(v.product_ids) ORDER BY array_position(v.product_ids,p.id)),
array(SELECT p.public_id::text FROM products p WHERE p.id=ANY(v.product_need_ids) ORDER BY array_position(v.product_need_ids,p.id)),
v.min_amount_cents,v.user_type,v.onetime,v.upgrade_use,v.renew_use,v.cycle,v.notes,v.enabled,
(SELECT count(*) FROM voucher_grants g WHERE g.voucher_id=v.id),
(SELECT count(*) FROM voucher_grants g WHERE g.voucher_id=v.id AND g.used),
v.created_at
FROM vouchers v`

func scanVoucher(row pgx.Row) (Voucher, error) {
	var v Voucher
	err := row.Scan(&v.PublicID, &v.Code, &v.PriceCents, &v.Type, &v.Num, &v.StartAt, &v.EndAt,
		&v.ProductPublicIDs, &v.ProductNeedPublicIDs, &v.MinAmountCents, &v.UserType, &v.Onetime,
		&v.UpgradeUse, &v.RenewUse, &v.Cycle, &v.Notes, &v.Enabled, &v.ClaimCount, &v.UsedCount, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	v.Status = voucherStatusOf(v.Enabled, v.StartAt, v.EndAt, time.Now())
	return v, nil
}

// ListVouchers 列出代金券，支持券码模糊搜索与状态过滤。
func (s *Store) ListVouchers(ctx context.Context, code, status string) ([]Voucher, error) {
	code = strings.TrimSpace(code)
	status = strings.TrimSpace(status)
	rows, err := s.DB.Query(ctx, voucherSelect+`
WHERE ($1='' OR v.code ILIKE '%'||$1||'%')
  AND ($2='' OR (CASE WHEN NOT v.enabled THEN 'disable' WHEN now() < v.start_at THEN 'wait_effect' WHEN v.end_at IS NOT NULL AND now() > v.end_at THEN 'expired' ELSE 'enable' END)=$2)
ORDER BY v.id DESC`, code, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Voucher{}
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVoucher 按 public_id 读一张券。
func (s *Store) GetVoucher(ctx context.Context, publicID string) (Voucher, error) {
	v, err := scanVoucher(s.DB.QueryRow(ctx, voucherSelect+` WHERE v.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Voucher{}, ErrNotFound
	}
	return v, err
}

// CheckVoucherCode 报告券码是否已存在（新增时的重复校验）。
func (s *Store) CheckVoucherCode(ctx context.Context, code string) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vouchers WHERE code=$1)`, strings.TrimSpace(code)).Scan(&exists)
	return exists, err
}

// resolveVoucherProductIDs 把商品 public_id 列表解析成内部 ID（保持输入顺序）。
func (s *Store) resolveVoucherProductIDs(ctx context.Context, q rowQuerier, publicIDs []string) ([]int64, error) {
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

// normalizeVoucherCycles 去重并校验计费周期取值。
func normalizeVoucherCycles(cycles []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range cycles {
		c = strings.ToLower(strings.TrimSpace(c))
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

func normalizeVoucherInput(in *VoucherInput) error {
	in.Code = strings.TrimSpace(in.Code)
	if !ValidVoucherCode(in.Code) {
		return fmt.Errorf("券码须为 8 位且同时包含大写字母、小写字母与数字")
	}
	if in.Type == "" {
		in.Type = "private"
	}
	if in.Type != "private" && in.Type != "public" {
		return fmt.Errorf("代金券类型不合法")
	}
	if in.PriceCents < 0 {
		return fmt.Errorf("代金券价值不能为负数")
	}
	if in.Num < 0 {
		return fmt.Errorf("代金券数量不能为负数")
	}
	if in.MinAmountCents < 0 {
		return fmt.Errorf("最低使用金额不能为负数")
	}
	if in.UserType == "" {
		in.UserType = "no_limit"
	}
	switch in.UserType {
	case "no_limit", "no_host", "need_active":
	default:
		return fmt.Errorf("用户类型限制不合法")
	}
	if in.StartAt.IsZero() {
		in.StartAt = time.Now()
	}
	if in.EndAt != nil && in.EndAt.Before(in.StartAt) {
		return fmt.Errorf("截止时间不能早于生效时间")
	}
	cycles, err := normalizeVoucherCycles(in.Cycle)
	if err != nil {
		return err
	}
	in.Cycle = cycles
	return nil
}

// CreateVoucher 新增一张代金券。
func (s *Store) CreateVoucher(ctx context.Context, in VoucherInput) (Voucher, error) {
	if err := normalizeVoucherInput(&in); err != nil {
		return Voucher{}, err
	}
	productIDs, err := s.resolveVoucherProductIDs(ctx, s.DB, in.ProductPublicIDs)
	if err != nil {
		return Voucher{}, err
	}
	needIDs, err := s.resolveVoucherProductIDs(ctx, s.DB, in.ProductNeedPublicIDs)
	if err != nil {
		return Voucher{}, err
	}
	var publicID string
	err = s.DB.QueryRow(ctx, `INSERT INTO vouchers(code,price_cents,type,num,start_at,end_at,product_ids,product_need_ids,min_amount_cents,user_type,onetime,upgrade_use,renew_use,cycle,notes)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING public_id::text`,
		in.Code, in.PriceCents, in.Type, in.Num, in.StartAt, in.EndAt, productIDs, needIDs, in.MinAmountCents, in.UserType, in.Onetime, in.UpgradeUse, in.RenewUse, in.Cycle, in.Notes).Scan(&publicID)
	if err != nil {
		if isUniqueViolation(err) {
			return Voucher{}, fmt.Errorf("代金券码重复")
		}
		return Voucher{}, err
	}
	return s.GetVoucher(ctx, publicID)
}

// UpdateVoucher 修改代金券（与插件一致：券码 / 类型 / 价值不可改）。
func (s *Store) UpdateVoucher(ctx context.Context, publicID string, in VoucherInput) error {
	if in.PriceCents < 0 || in.Num < 0 || in.MinAmountCents < 0 {
		return fmt.Errorf("金额与数量不能为负数")
	}
	if in.UserType == "" {
		in.UserType = "no_limit"
	}
	switch in.UserType {
	case "no_limit", "no_host", "need_active":
	default:
		return fmt.Errorf("用户类型限制不合法")
	}
	if in.StartAt.IsZero() {
		in.StartAt = time.Now()
	}
	if in.EndAt != nil && in.EndAt.Before(in.StartAt) {
		return fmt.Errorf("截止时间不能早于生效时间")
	}
	cycles, err := normalizeVoucherCycles(in.Cycle)
	if err != nil {
		return err
	}
	productIDs, err := s.resolveVoucherProductIDs(ctx, s.DB, in.ProductPublicIDs)
	if err != nil {
		return err
	}
	needIDs, err := s.resolveVoucherProductIDs(ctx, s.DB, in.ProductNeedPublicIDs)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE vouchers SET start_at=$2,end_at=$3,product_ids=$4,product_need_ids=$5,min_amount_cents=$6,user_type=$7,onetime=$8,upgrade_use=$9,renew_use=$10,cycle=$11,notes=$12,num=$13,updated_at=now() WHERE public_id=$1`,
		publicID, in.StartAt, in.EndAt, productIDs, needIDs, in.MinAmountCents, in.UserType, in.Onetime, in.UpgradeUse, in.RenewUse, cycles, in.Notes, in.Num)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetVoucherEnabled 启用 / 停用代金券。
func (s *Store) SetVoucherEnabled(ctx context.Context, publicID string, enabled bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE vouchers SET enabled=$2,updated_at=now() WHERE public_id=$1`, publicID, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteVoucher 删除代金券（领取与使用记录随之删除）。
func (s *Store) DeleteVoucher(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM vouchers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// voucherCheckUserType 校验用户类型限制。
func voucherCheckUserType(ctx context.Context, q rowQuerier, userID int64, userType string) error {
	switch userType {
	case "no_host":
		var has bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND status NOT IN ('terminated','failed'))`, userID).Scan(&has); err != nil {
			return err
		}
		if has {
			return fmt.Errorf("该代金券仅限无产品用户使用")
		}
	case "need_active":
		var has bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND status='active')`, userID).Scan(&has); err != nil {
			return err
		}
		if !has {
			return fmt.Errorf("该代金券需账户中存在激活中的产品才能使用")
		}
	}
	return nil
}

// voucherCheck 校验代金券对一笔金额（下单 / 续费 / 升级）的可用性，
// 返回抵扣金额与命中的领取记录 ID；不消费。lock=true 时锁住领取记录行。
// kind：new 新购 / renew 续费 / upgrade 升降级。
func voucherCheck(ctx context.Context, q rowQuerier, userID int64, code string, productID int64, cycle string, amountCents int64, kind string, lock bool) (discount int64, grantID int64, err error) {
	code = strings.TrimSpace(code)
	var (
		voucherID  int64
		price      int64
		startAt    time.Time
		endAt      *time.Time
		enabled    bool
		minAmount  int64
		userType   string
		onetime    bool
		upgradeUse bool
		renewUse   bool
		cycles     []string
		productIDs []int64
		needIDs    []int64
	)
	err = q.QueryRow(ctx, `SELECT id,price_cents,start_at,end_at,enabled,min_amount_cents,user_type,onetime,upgrade_use,renew_use,cycle,product_ids,product_need_ids
FROM vouchers WHERE code=$1`, code).Scan(&voucherID, &price, &startAt, &endAt, &enabled, &minAmount, &userType, &onetime, &upgradeUse, &renewUse, &cycles, &productIDs, &needIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, fmt.Errorf("代金券不存在")
	}
	if err != nil {
		return 0, 0, err
	}
	now := time.Now()
	switch {
	case !enabled:
		return 0, 0, fmt.Errorf("代金券已停用")
	case now.Before(startAt):
		return 0, 0, fmt.Errorf("代金券尚未生效")
	case endAt != nil && now.After(*endAt):
		return 0, 0, fmt.Errorf("代金券已过期")
	}
	switch kind {
	case "upgrade":
		if !upgradeUse {
			return 0, 0, fmt.Errorf("该代金券不可用于升降级订单")
		}
	case "renew":
		if !renewUse {
			return 0, 0, fmt.Errorf("该代金券不可用于续费订单")
		}
	}
	if err := voucherCheckUserType(ctx, q, userID, userType); err != nil {
		return 0, 0, err
	}
	if len(productIDs) > 0 && !int64InSlice(productIDs, productID) {
		return 0, 0, fmt.Errorf("该代金券不适用于当前商品")
	}
	if len(cycles) > 0 && !stringInSlice(cycles, cycle) {
		return 0, 0, fmt.Errorf("该代金券不适用于当前计费周期")
	}
	if amountCents < minAmount {
		return 0, 0, fmt.Errorf("订单金额需满 ¥%.2f 才能使用该代金券", float64(minAmount)/100)
	}
	var usedAny bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM voucher_grants WHERE voucher_id=$1 AND user_id=$2 AND used)`, voucherID, userID).Scan(&usedAny); err != nil {
		return 0, 0, err
	}
	if usedAny && onetime {
		return 0, 0, fmt.Errorf("该代金券每人只能使用一次")
	}
	gq := `SELECT id FROM voucher_grants WHERE voucher_id=$1 AND user_id=$2 AND NOT used ORDER BY id LIMIT 1`
	if lock {
		gq += ` FOR UPDATE`
	}
	if err := q.QueryRow(ctx, gq, voucherID, userID).Scan(&grantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, fmt.Errorf("你没有这张代金券，请先领取或等待发放")
		}
		return 0, 0, err
	}
	if len(needIDs) > 0 {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND status='active' AND product_id=ANY($2))`, userID, needIDs).Scan(&ok); err != nil {
			return 0, 0, err
		}
		if !ok {
			return 0, 0, fmt.Errorf("使用该代金券需先拥有指定的激活产品")
		}
	}
	discount = price
	if discount > amountCents {
		discount = amountCents
	}
	if discount <= 0 {
		return 0, 0, fmt.Errorf("代金券抵扣金额为 0")
	}
	return discount, grantID, nil
}

func int64InSlice(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func stringInSlice(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// markVoucherUsedTx 在订单创建成功后把领取记录标记为已使用。
func markVoucherUsedTx(ctx context.Context, tx pgx.Tx, grantID, orderID int64) error {
	if grantID == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE voucher_grants SET used=TRUE,order_id=$2,used_at=now() WHERE id=$1`, grantID, orderID)
	return err
}

// PreviewVoucher 预览代金券对某商品新购订单的抵扣金额（不消费、不加锁）。
// amountCents 由调用方按「列表价 × 数量」给出，仅供前台展示。
func (s *Store) PreviewVoucher(ctx context.Context, userID int64, code, productPublicID, cycle string, amountCents int64) (int64, error) {
	var productID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, strings.TrimSpace(productPublicID)).Scan(&productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	discount, _, err := voucherCheck(ctx, s.DB, userID, code, productID, cycle, amountCents, "new", false)
	return discount, err
}

// VoucherLineMatches 只检查「商品 / 周期范围」，用于购物车结算时挑选第一条
// 可以尝试使用代金券的明细（金额、领取状态等在真正核销时再校验）。
func (s *Store) VoucherLineMatches(ctx context.Context, code, productPublicID, cycle string) (bool, error) {
	var (
		startAt    time.Time
		endAt      *time.Time
		enabled    bool
		cycles     []string
		productIDs []int64
	)
	err := s.DB.QueryRow(ctx, `SELECT start_at,end_at,enabled,cycle,product_ids FROM vouchers WHERE code=$1`, strings.TrimSpace(code)).
		Scan(&startAt, &endAt, &enabled, &cycles, &productIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := time.Now()
	if !enabled || now.Before(startAt) || (endAt != nil && now.After(*endAt)) {
		return false, nil
	}
	if len(cycles) > 0 && !stringInSlice(cycles, cycle) {
		return false, nil
	}
	if len(productIDs) > 0 {
		var productID int64
		if err := s.DB.QueryRow(ctx, `SELECT id FROM products WHERE public_id=$1 AND deleted_at IS NULL`, strings.TrimSpace(productPublicID)).Scan(&productID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if !int64InSlice(productIDs, productID) {
			return false, nil
		}
	}
	return true, nil
}

// ListClaimableVouchers 返回当前可领取的公开券（有效期内、未领完、用户尚未领取）。
func (s *Store) ListClaimableVouchers(ctx context.Context, userID int64) ([]ClaimableVoucher, error) {
	rows, err := s.DB.Query(ctx, `SELECT v.public_id::text,v.code,v.price_cents,v.min_amount_cents,v.start_at,v.end_at,v.notes,
CASE WHEN v.num=0 THEN -1 ELSE v.num-(SELECT count(*) FROM voucher_grants g WHERE g.voucher_id=v.id) END
FROM vouchers v
WHERE v.type='public' AND v.enabled=TRUE AND now() >= v.start_at AND (v.end_at IS NULL OR now() <= v.end_at)
  AND (v.num=0 OR (SELECT count(*) FROM voucher_grants g WHERE g.voucher_id=v.id) < v.num)
  AND NOT EXISTS(SELECT 1 FROM voucher_grants g WHERE g.voucher_id=v.id AND g.user_id=$1)
ORDER BY v.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClaimableVoucher{}
	for rows.Next() {
		var v ClaimableVoucher
		if err := rows.Scan(&v.PublicID, &v.Code, &v.PriceCents, &v.MinAmountCents, &v.StartAt, &v.EndAt, &v.Notes, &v.Remaining); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MyVouchers 返回用户名下的代金券（按未使用、未过期优先排序）。
func (s *Store) MyVouchers(ctx context.Context, userID int64) ([]MyVoucher, error) {
	rows, err := s.DB.Query(ctx, `SELECT g.public_id::text,v.public_id::text,v.code,v.price_cents,v.min_amount_cents,v.start_at,v.end_at,v.notes,g.source,g.used,
coalesce(o.public_id::text,''),g.used_at,g.created_at,v.enabled
FROM voucher_grants g JOIN vouchers v ON v.id=g.voucher_id
LEFT JOIN orders o ON o.id=g.order_id
WHERE g.user_id=$1
ORDER BY g.used, (v.end_at IS NULL OR v.end_at >= now()) DESC, g.id DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MyVoucher{}
	for rows.Next() {
		var v MyVoucher
		var enabled bool
		if err := rows.Scan(&v.PublicID, &v.VoucherID, &v.Code, &v.PriceCents, &v.MinAmountCents, &v.StartAt, &v.EndAt, &v.Notes, &v.Source, &v.Used, &v.OrderPublicID, &v.UsedAt, &v.CreatedAt, &enabled); err != nil {
			return nil, err
		}
		v.Status = voucherStatusOf(enabled, v.StartAt, v.EndAt, time.Now())
		if v.Used {
			v.Status = "used"
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ClaimVoucher 用户领取一张公开券（总量与用户类型限制在事务内校验）。
func (s *Store) ClaimVoucher(ctx context.Context, userID int64, voucherPublicID string) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var (
		voucherID int64
		vtype     string
		num       int64
		startAt   time.Time
		endAt     *time.Time
		enabled   bool
		userType  string
		onetime   bool
	)
	err = tx.QueryRow(ctx, `SELECT id,type,num,start_at,end_at,enabled,user_type,onetime FROM vouchers WHERE public_id=$1 FOR UPDATE`, voucherPublicID).
		Scan(&voucherID, &vtype, &num, &startAt, &endAt, &enabled, &userType, &onetime)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	now := time.Now()
	switch {
	case vtype != "public":
		return fmt.Errorf("该代金券不支持前台领取")
	case !enabled:
		return fmt.Errorf("代金券已停用")
	case now.Before(startAt):
		return fmt.Errorf("代金券尚未生效")
	case endAt != nil && now.After(*endAt):
		return fmt.Errorf("代金券已过期")
	}
	if err := voucherCheckUserType(ctx, tx, userID, userType); err != nil {
		return err
	}
	var total, mine, mineUnused int64
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE user_id=$2),count(*) FILTER (WHERE user_id=$2 AND NOT used) FROM voucher_grants WHERE voucher_id=$1`, voucherID, userID).Scan(&total, &mine, &mineUnused); err != nil {
		return err
	}
	if num > 0 && total >= num {
		return fmt.Errorf("代金券已被领完")
	}
	if mineUnused > 0 {
		return fmt.Errorf("你已领取过该代金券")
	}
	if mine > 0 && onetime {
		return fmt.Errorf("该代金券每人只能使用一次")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO voucher_grants(voucher_id,user_id,source) VALUES($1,$2,'claim')`, voucherID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GrantVoucher 后台发放：all=true 发放给全部有效用户，否则按 public_id 列表发放；
// num>0 时按总量限制顺序发放，发完即止（返回实际发放数与被跳过数）。
func (s *Store) GrantVoucher(ctx context.Context, voucherPublicID string, userPublicIDs []string, all bool) (granted, skipped int, err error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	var voucherID, num int64
	err = tx.QueryRow(ctx, `SELECT id,num FROM vouchers WHERE public_id=$1 FOR UPDATE`, voucherPublicID).Scan(&voucherID, &num)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, err
	}
	targets := []int64{}
	if all {
		rows, err := tx.Query(ctx, `SELECT id FROM users WHERE deleted_at IS NULL AND status='active' ORDER BY id`)
		if err != nil {
			return 0, 0, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, 0, err
			}
			targets = append(targets, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, 0, err
		}
	} else {
		seen := map[int64]bool{}
		for _, uid := range userPublicIDs {
			uid = strings.TrimSpace(uid)
			if uid == "" {
				continue
			}
			var id int64
			if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE public_id=$1 AND deleted_at IS NULL`, uid).Scan(&id); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return 0, 0, fmt.Errorf("用户不存在")
				}
				return 0, 0, err
			}
			if !seen[id] {
				seen[id] = true
				targets = append(targets, id)
			}
		}
	}
	if len(targets) == 0 {
		return 0, 0, fmt.Errorf("请选择发放对象")
	}
	if num > 0 {
		var already int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM voucher_grants WHERE voucher_id=$1`, voucherID).Scan(&already); err != nil {
			return 0, 0, err
		}
		room := num - already
		if room <= 0 {
			return 0, 0, fmt.Errorf("代金券数量已发完")
		}
		if int64(len(targets)) > room {
			skipped = len(targets) - int(room)
			targets = targets[:room]
		}
	}
	for _, uid := range targets {
		if _, err := tx.Exec(ctx, `INSERT INTO voucher_grants(voucher_id,user_id,source) VALUES($1,$2,'grant')`, voucherID, uid); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return len(targets), skipped, nil
}

// VoucherTimes 返回一张券按用户的发放次数（发放弹窗展示）。
func (s *Store) VoucherTimes(ctx context.Context, voucherPublicID string) ([]VoucherTime, error) {
	rows, err := s.DB.Query(ctx, `SELECT u.public_id::text,coalesce(nullif(up.nickname,''),nullif(up.real_name,''),u.email),u.email,count(*)
FROM voucher_grants g JOIN users u ON u.id=g.user_id
LEFT JOIN user_profiles up ON up.user_id=u.id
WHERE g.voucher_id=(SELECT id FROM vouchers WHERE public_id=$1)
GROUP BY u.id,up.nickname,up.real_name,u.email ORDER BY count(*) DESC,u.id LIMIT 500`, voucherPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VoucherTime{}
	for rows.Next() {
		var v VoucherTime
		if err := rows.Scan(&v.UserPublicID, &v.Username, &v.Email, &v.Num); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListVoucherRecords 分页列出某张券的领取 / 使用记录。
// use："" 全部 / "0" 未使用 / "1" 已使用；keyword 匹配用户名、邮箱、手机号。
func (s *Store) ListVoucherRecords(ctx context.Context, voucherPublicID, keyword, use string, limit, offset int) ([]VoucherRecord, int64, error) {
	keyword = strings.TrimSpace(keyword)
	use = strings.TrimSpace(use)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where := `WHERE v.public_id=$1`
	args := []any{voucherPublicID}
	if keyword != "" {
		args = append(args, "%"+keyword+"%")
		idx := len(args)
		where += fmt.Sprintf(` AND (u.email ILIKE $%d OR coalesce(up.nickname,'') ILIKE $%d OR coalesce(up.real_name,'') ILIKE $%d OR coalesce(up.phone,'') ILIKE $%d)`, idx, idx, idx, idx)
	}
	if use == "0" || use == "1" {
		args = append(args, use == "1")
		where += fmt.Sprintf(` AND g.used=$%d`, len(args))
	}
	var total int64
	countQ := `SELECT count(*) FROM voucher_grants g JOIN vouchers v ON v.id=g.voucher_id JOIN users u ON u.id=g.user_id LEFT JOIN user_profiles up ON up.user_id=u.id ` + where
	if err := s.DB.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	listQ := `SELECT g.public_id::text,coalesce(v.public_id::text,''),v.code,u.public_id::text,coalesce(nullif(up.nickname,''),nullif(up.real_name,''),u.email),coalesce(up.phone,''),u.email,g.source,g.used,coalesce(o.public_id::text,''),g.used_at,g.created_at
FROM voucher_grants g JOIN vouchers v ON v.id=g.voucher_id JOIN users u ON u.id=g.user_id
LEFT JOIN user_profiles up ON up.user_id=u.id LEFT JOIN orders o ON o.id=g.order_id ` + where +
		fmt.Sprintf(` ORDER BY g.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	rows, err := s.DB.Query(ctx, listQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []VoucherRecord{}
	for rows.Next() {
		var v VoucherRecord
		if err := rows.Scan(&v.PublicID, &v.VoucherID, &v.VoucherCode, &v.UserPublicID, &v.Username, &v.Phone, &v.Email, &v.Source, &v.Used, &v.OrderPublicID, &v.UsedAt, &v.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// DeleteVoucherGrant 删除一条领取 / 使用记录。
func (s *Store) DeleteVoucherGrant(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM voucher_grants WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
