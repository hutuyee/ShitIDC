package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 业务经理（对齐魔方 CBAP IdcsmartSale 插件）。
//
// 销售成员挂在后台账号上（admin_id 唯一），客户绑定后该客户名下已支付订单
// 归属该销售；支付成功按提成规则记提成（新购 / 续费 / 复购 / 升降级 + 大额
// 订单奖励），确认天数内为 pending、到期转 active（惰性计算），管理员可置
// invalid。比例一律落基点（100 = 1%）。
//
// 插件的「任务奖励」与「充值提成」只有加密实现，前端契约不足，
// 本实现不编造：提成类型覆盖 new / renew / repurchase / upgrade / big_order。

// ---- 销售成员 ----

// Sale 是一个销售成员。
type Sale struct {
	ID        string    `json:"id"`
	AdminUID  int64     `json:"admin_uid"`
	AdminName string    `json:"admin_name"`
	Name      string    `json:"name"`
	Num       string    `json:"num"`
	Email     string    `json:"email"`
	Active    bool      `json:"active"`
	ClientNum int64     `json:"client_num"`
	CreatedAt time.Time `json:"created_at"`
}

const saleCols = `SELECT s.id,s.public_id::text,u.uid,u.email,s.name,s.num,s.email,s.active,
(SELECT count(*) FROM sale_clients sc WHERE sc.sale_id=s.id),s.created_at
FROM sales s JOIN users u ON u.id=s.admin_id`

// ListSales 列出销售成员（keyword 匹配姓名 / 编号 / 邮箱）。
func (s *Store) ListSales(ctx context.Context, keyword string) ([]Sale, error) {
	keyword = strings.TrimSpace(keyword)
	rows, err := s.DB.Query(ctx, saleCols+` WHERE ($1='' OR s.name ILIKE '%'||$1||'%' OR s.num ILIKE '%'||$1||'%' OR s.email ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%') ORDER BY s.id`, keyword)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sale{}
	for rows.Next() {
		var v Sale
		if err := rows.Scan(new(int64), &v.ID, &v.AdminUID, &v.AdminName, &v.Name, &v.Num, &v.Email, &v.Active, &v.ClientNum, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaleInput 是销售成员入参。
type SaleInput struct {
	AdminUID int64
	Name     string
	Num      string
	Email    string
}

// CreateSale 新增销售成员；同一后台账号只能是一个销售。
func (s *Store) CreateSale(ctx context.Context, in SaleInput) (Sale, error) {
	if strings.TrimSpace(in.Name) == "" || in.AdminUID <= 0 {
		return Sale{}, ErrInvalidState
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Sale{}, err
	}
	defer tx.Rollback(ctx)
	var adminID int64
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE uid=$1`, in.AdminUID).Scan(&adminID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sale{}, ErrNotFound
	}
	if err != nil {
		return Sale{}, err
	}
	var publicID string
	err = tx.QueryRow(ctx, `INSERT INTO sales(admin_id,name,num,email) VALUES($1,$2,$3,$4) RETURNING public_id::text`,
		adminID, strings.TrimSpace(in.Name), strings.TrimSpace(in.Num), strings.TrimSpace(in.Email)).Scan(&publicID)
	if err != nil {
		return Sale{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Sale{}, err
	}
	return s.getSale(ctx, publicID)
}

func (s *Store) getSale(ctx context.Context, publicID string) (Sale, error) {
	var v Sale
	err := s.DB.QueryRow(ctx, saleCols+` WHERE s.public_id=$1`, publicID).
		Scan(new(int64), &v.ID, &v.AdminUID, &v.AdminName, &v.Name, &v.Num, &v.Email, &v.Active, &v.ClientNum, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sale{}, ErrNotFound
	}
	return v, err
}

// UpdateSale 修改销售成员。
func (s *Store) UpdateSale(ctx context.Context, publicID string, in SaleInput) (Sale, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE sales SET name=$2,num=$3,email=$4,updated_at=now() WHERE public_id=$1`,
		publicID, strings.TrimSpace(in.Name), strings.TrimSpace(in.Num), strings.TrimSpace(in.Email))
	if err != nil {
		return Sale{}, err
	}
	if tag.RowsAffected() == 0 {
		return Sale{}, ErrNotFound
	}
	return s.getSale(ctx, publicID)
}

// SetSaleActive 启停销售。
func (s *Store) SetSaleActive(ctx context.Context, publicID string, active bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE sales SET active=$2,updated_at=now() WHERE public_id=$1`, publicID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSale 删除销售（绑定与提成记录级联）。
func (s *Store) DeleteSale(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sales WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 客户绑定 ----

// SaleClient 是一条客户绑定。
type SaleClient struct {
	SaleID    string    `json:"sale_id"`
	SaleName  string    `json:"sale_name"`
	UserUID   int64     `json:"user_uid"`
	UserEmail string    `json:"user_email"`
	Notes     string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// ListSaleClients 列出绑定（可按销售过滤，keyword 匹配用户邮箱）。
func (s *Store) ListSaleClients(ctx context.Context, saleID, keyword string) ([]SaleClient, error) {
	keyword = strings.TrimSpace(keyword)
	where := ` WHERE ($1='' OR s.public_id::text=$1) AND ($2='' OR u.email ILIKE '%'||$2||'%' OR u.uid::text=$2)`
	rows, err := s.DB.Query(ctx, `SELECT s.public_id::text,s.name,u.uid,u.email,sc.created_at
FROM sale_clients sc JOIN sales s ON s.id=sc.sale_id JOIN users u ON u.id=sc.user_id`+where+` ORDER BY sc.created_at DESC`, saleID, keyword)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SaleClient{}
	for rows.Next() {
		var v SaleClient
		if err := rows.Scan(&v.SaleID, &v.SaleName, &v.UserUID, &v.UserEmail, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// BindSaleClient 把客户绑到销售；已绑定其它销售时先解除（一人只归一个销售）。
func (s *Store) BindSaleClient(ctx context.Context, salePublicID string, userUID int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var saleID int64
	err = tx.QueryRow(ctx, `SELECT id FROM sales WHERE public_id=$1`, salePublicID).Scan(&saleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var userID int64
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE uid=$1`, userUID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidState
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sale_clients WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sale_clients(sale_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, saleID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UnbindSaleClient 解除绑定。
func (s *Store) UnbindSaleClient(ctx context.Context, userUID int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sale_clients sc USING users u WHERE u.id=sc.user_id AND u.uid=$1`, userUID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 提成规则 ----

// SaleCommissionConfig 是一条提成规则（global 一条兜底；product 按商品覆盖）。
type SaleCommissionConfig struct {
	ID              string `json:"id"`
	Scope           string `json:"scope"`
	ProductID       string `json:"product_id"`
	ProductName     string `json:"product_name"`
	NewMode         string `json:"new_mode"`
	NewValue        int64  `json:"new_value"`
	RenewMode       string `json:"renew_mode"`
	RenewValue      int64  `json:"renew_value"`
	RepurchaseMode  string `json:"repurchase_mode"`
	RepurchaseValue int64  `json:"repurchase_value"`
	UpgradeMode     string `json:"upgrade_mode"`
	UpgradeValue    int64  `json:"upgrade_value"`
	Active          bool   `json:"active"`
}

const saleConfigCols = `SELECT c.id,c.public_id::text,c.scope,coalesce(p.public_id::text,''),coalesce(p.name,''),
c.new_mode,c.new_value,c.renew_mode,c.renew_value,c.repurchase_mode,c.repurchase_value,c.upgrade_mode,c.upgrade_value,c.active
FROM sale_commission_configs c LEFT JOIN products p ON p.id=c.product_id`

// ListSaleCommissionConfigs 列出提成规则。
func (s *Store) ListSaleCommissionConfigs(ctx context.Context) ([]SaleCommissionConfig, error) {
	rows, err := s.DB.Query(ctx, saleConfigCols+` ORDER BY (c.scope='global') DESC, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SaleCommissionConfig{}
	for rows.Next() {
		var v SaleCommissionConfig
		if err := rows.Scan(new(int64), &v.ID, &v.Scope, &v.ProductID, &v.ProductName,
			&v.NewMode, &v.NewValue, &v.RenewMode, &v.RenewValue, &v.RepurchaseMode, &v.RepurchaseValue, &v.UpgradeMode, &v.UpgradeValue, &v.Active); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaleCommissionConfigInput 是提成规则入参。
type SaleCommissionConfigInput struct {
	Scope           string
	ProductID       string
	NewMode         string
	NewValue        int64
	RenewMode       string
	RenewValue      int64
	RepurchaseMode  string
	RepurchaseValue int64
	UpgradeMode     string
	UpgradeValue    int64
	Active          bool
}

// SaveSaleCommissionConfig 新增 / 更新一条规则（global 与 product 各自唯一，覆盖保存）。
func (s *Store) SaveSaleCommissionConfig(ctx context.Context, in SaleCommissionConfigInput) (SaleCommissionConfig, error) {
	if in.Scope != "global" && in.Scope != "product" {
		return SaleCommissionConfig{}, ErrInvalidState
	}
	if in.Scope == "product" && in.ProductID == "" {
		return SaleCommissionConfig{}, ErrInvalidState
	}
	if err := validateSaleModes(in); err != nil {
		return SaleCommissionConfig{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return SaleCommissionConfig{}, err
	}
	defer tx.Rollback(ctx)
	var productID *int64
	if in.ProductID != "" {
		id, err := wanyunPublicToID(ctx, tx, "products", in.ProductID)
		if err != nil {
			return SaleCommissionConfig{}, ErrNotFound
		}
		productID = &id
	}
	var publicID string
	err = tx.QueryRow(ctx, `INSERT INTO sale_commission_configs(scope,product_id,new_mode,new_value,renew_mode,renew_value,repurchase_mode,repurchase_value,upgrade_mode,upgrade_value,active)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (scope,product_id) DO UPDATE SET new_mode=excluded.new_mode,new_value=excluded.new_value,
renew_mode=excluded.renew_mode,renew_value=excluded.renew_value,repurchase_mode=excluded.repurchase_mode,
repurchase_value=excluded.repurchase_value,upgrade_mode=excluded.upgrade_mode,upgrade_value=excluded.upgrade_value,
active=excluded.active,updated_at=now()
RETURNING public_id::text`,
		in.Scope, productID, in.NewMode, in.NewValue, in.RenewMode, in.RenewValue, in.RepurchaseMode, in.RepurchaseValue, in.UpgradeMode, in.UpgradeValue, in.Active).Scan(&publicID)
	if err != nil {
		return SaleCommissionConfig{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SaleCommissionConfig{}, err
	}
	var v SaleCommissionConfig
	err = s.DB.QueryRow(ctx, saleConfigCols+` WHERE c.public_id=$1`, publicID).
		Scan(new(int64), &v.ID, &v.Scope, &v.ProductID, &v.ProductName,
			&v.NewMode, &v.NewValue, &v.RenewMode, &v.RenewValue, &v.RepurchaseMode, &v.RepurchaseValue, &v.UpgradeMode, &v.UpgradeValue, &v.Active)
	return v, err
}

// DeleteSaleCommissionConfig 删除规则。
func (s *Store) DeleteSaleCommissionConfig(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sale_commission_configs WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func validateSaleModes(in SaleCommissionConfigInput) error {
	for _, m := range [][2]any{{in.NewMode, in.NewValue}, {in.RenewMode, in.RenewValue}, {in.RepurchaseMode, in.RepurchaseValue}, {in.UpgradeMode, in.UpgradeValue}} {
		mode := m[0].(string)
		value := m[1].(int64)
		if mode != "fixed" && mode != "percent" {
			return ErrInvalidState
		}
		if mode == "percent" && value > 10000 {
			return ErrInvalidState
		}
	}
	return nil
}

// ---- 销售设置（system_settings.sale） ----

// SaleSettings 是销售设置：确认天数 + 大额订单奖励。
type SaleSettings struct {
	ConfirmWaitDay int    `json:"confirm_wait_day"`
	BigMinCents    int64  `json:"big_min_cents"`
	BigMaxCents    int64  `json:"big_max_cents"`
	BigMode        string `json:"big_mode"`
	BigValue       int64  `json:"big_value"`
}

// GetSaleSettings 读取销售设置（默认立即确认）。
func (s *Store) GetSaleSettings(ctx context.Context) (SaleSettings, error) {
	out := SaleSettings{BigMode: "fixed"}
	err := s.settingGet(ctx, "sale", &out)
	if errors.Is(err, ErrNotFound) {
		return SaleSettings{BigMode: "fixed"}, nil
	}
	if err != nil {
		return SaleSettings{}, err
	}
	if out.ConfirmWaitDay < 0 {
		out.ConfirmWaitDay = 0
	}
	if out.BigMode != "fixed" && out.BigMode != "percent" {
		out.BigMode = "fixed"
	}
	return out, nil
}

// SaveSaleSettings 保存销售设置。
func (s *Store) SaveSaleSettings(ctx context.Context, in SaleSettings) error {
	if in.ConfirmWaitDay < 0 || in.BigMinCents < 0 || in.BigMaxCents < 0 {
		return ErrInvalidState
	}
	if in.BigMode != "fixed" && in.BigMode != "percent" {
		return ErrInvalidState
	}
	if in.BigValue < 0 || (in.BigMode == "percent" && in.BigValue > 10000) {
		return ErrInvalidState
	}
	return s.settingSave(ctx, "sale", in)
}

// ---- 提成入账 ----

// AccrueSaleCommission 在支付成功后为绑定销售的客户订单记提成（幂等）。
// 订单类型：renewal → renew；upgrade → upgrade；new → 按该客户是否已买过同商品
// 区分 new / repurchase。大额订单奖励按订单合计另行记一条。
// 返回生成的提成条数。
func (s *Store) AccrueSaleCommission(ctx context.Context, orderPublicID string) (int, error) {
	settings, err := s.GetSaleSettings(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var orderID, userID int64
	var kind string
	var totalCents int64
	err = tx.QueryRow(ctx, `SELECT id,user_id,kind,total_cents FROM orders WHERE public_id=$1 AND status='paid'`, orderPublicID).Scan(&orderID, &userID, &kind, &totalCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var saleID int64
	err = tx.QueryRow(ctx, `SELECT sc.sale_id FROM sale_clients sc JOIN sales sl ON sl.id=sc.sale_id WHERE sc.user_id=$1 AND sl.active=true`, userID).Scan(&saleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	confirmAt := time.Now().AddDate(0, 0, settings.ConfirmWaitDay)
	type itemRow struct {
		itemID    int64
		productID int64
		subtotal  int64
	}
	items := []itemRow{}
	rows, err := tx.Query(ctx, `SELECT oi.id,oi.product_id,oi.quantity*oi.unit_price_cents+oi.config_cents+oi.setup_cents
FROM order_items oi WHERE oi.order_id=$1 AND oi.product_id IS NOT NULL`, orderID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var it itemRow
		if err := rows.Scan(&it.itemID, &it.productID, &it.subtotal); err != nil {
			return 0, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	type draft struct {
		idem   string
		typ    string
		base   int64
		mode   string
		value  int64
		amount int64
	}
	drafts := []draft{}
	for _, it := range items {
		var typ string
		switch kind {
		case "renewal":
			typ = "renew"
		case "upgrade":
			typ = "upgrade"
		default:
			var seen bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders o JOIN order_items oi ON oi.order_id=o.id
WHERE o.user_id=$1 AND o.status='paid' AND o.id<>$2 AND oi.product_id=$3)`, userID, orderID, it.productID).Scan(&seen); err != nil {
				return 0, err
			}
			if seen {
				typ = "repurchase"
			} else {
				typ = "new"
			}
		}
		mode, value, ok, err := saleCommissionFor(ctx, tx, it.productID, typ)
		if err != nil {
			return 0, err
		}
		if !ok || value <= 0 {
			continue
		}
		amount := value
		if mode == "percent" {
			amount = it.subtotal * value / 10000
		}
		if amount <= 0 {
			continue
		}
		drafts = append(drafts, draft{
			idem:   orderPublicID + ":" + itoa64(it.itemID) + ":" + typ,
			typ:    typ,
			base:   it.subtotal,
			mode:   mode,
			value:  value,
			amount: amount,
		})
	}

	// 大额订单奖励：订单合计落在 [min, max]（max=0 表示不限）时记一条。
	if settings.BigValue > 0 && totalCents >= settings.BigMinCents && (settings.BigMaxCents == 0 || totalCents <= settings.BigMaxCents) {
		amount := settings.BigValue
		if settings.BigMode == "percent" {
			amount = totalCents * settings.BigValue / 10000
		}
		if amount > 0 {
			drafts = append(drafts, draft{
				idem:   orderPublicID + ":big_order",
				typ:    "big_order",
				base:   totalCents,
				mode:   settings.BigMode,
				value:  settings.BigValue,
				amount: amount,
			})
		}
	}

	count := 0
	for _, d := range drafts {
		tag, err := tx.Exec(ctx, `INSERT INTO sale_commissions(idem_key,sale_id,order_id,user_id,type,base_cents,mode,value,amount_cents,status,confirm_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10) ON CONFLICT (idem_key) DO NOTHING`,
			d.idem, saleID, orderID, userID, d.typ, d.base, d.mode, d.value, d.amount, confirmAt)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() > 0 {
			count++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

// saleCommissionFor 取一条对商品 / 类型生效的提成规则（商品级优先，回退全局）。
func saleCommissionFor(ctx context.Context, tx pgx.Tx, productID int64, typ string) (string, int64, bool, error) {
	rows, err := tx.Query(ctx, `SELECT new_mode,new_value,renew_mode,renew_value,repurchase_mode,repurchase_value,upgrade_mode,upgrade_value
FROM sale_commission_configs c
WHERE c.active=true AND ((c.scope='product' AND c.product_id=$1) OR (c.scope='global' AND c.product_id IS NULL))
ORDER BY (c.scope='product') DESC`, productID)
	if err != nil {
		return "", 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var mNew, mRenew, mRep, mUp string
		var vNew, vRenew, vRep, vUp *int64
		if err := rows.Scan(&mNew, &vNew, &mRenew, &vRenew, &mRep, &vRep, &mUp, &vUp); err != nil {
			return "", 0, false, err
		}
		mode, value := "", int64(0)
		switch typ {
		case "new":
			mode, value = mNew, deref64(vNew)
		case "renew":
			mode, value = mRenew, deref64(vRenew)
		case "repurchase":
			mode, value = mRep, deref64(vRep)
		case "upgrade":
			mode, value = mUp, deref64(vUp)
		}
		if value > 0 {
			return mode, value, true, rows.Err()
		}
	}
	return "", 0, false, rows.Err()
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ---- 提成记录与统计 ----

// SaleCommission 是一条提成记录。status 是惰性计算的展示口径：
// pending 记录到达 confirm_at 后按 active 展示与汇总（库里保持 pending）。
type SaleCommission struct {
	ID          string     `json:"id"`
	OrderID     string     `json:"order_id"`
	UserUID     int64      `json:"user_uid"`
	UserEmail   string     `json:"user_email"`
	SaleID      string     `json:"sale_id"`
	SaleName    string     `json:"sale_name"`
	Type        string     `json:"type"`
	BaseCents   int64      `json:"base_cents"`
	Mode        string     `json:"mode"`
	Value       int64      `json:"value"`
	AmountCents int64      `json:"amount_cents"`
	Status      string     `json:"status"`
	ConfirmAt   *time.Time `json:"confirm_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

const saleCommissionCols = `SELECT sc.id,sc.public_id::text,coalesce(o.public_id::text,''),coalesce(u.uid,0),coalesce(u.email,''),
s.public_id::text,s.name,sc.type,sc.base_cents,sc.mode,sc.value,sc.amount_cents,sc.status,sc.confirm_at,sc.created_at
FROM sale_commissions sc
JOIN sales s ON s.id=sc.sale_id
LEFT JOIN orders o ON o.id=sc.order_id
LEFT JOIN users u ON u.id=sc.user_id`

// ListSaleCommissions 列提成记录（sale_id / type / status 过滤 + 时间范围）。
func (s *Store) ListSaleCommissions(ctx context.Context, saleID, typ, status, start, end string, limit, offset int) ([]SaleCommission, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where, args := saleCommissionWhere(saleID, typ, status, start, end)
	rows, err := s.DB.Query(ctx, saleCommissionCols+where+` ORDER BY sc.id DESC LIMIT `+itoa64(int64(limit))+` OFFSET `+itoa64(int64(offset)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []SaleCommission{}
	for rows.Next() {
		var v SaleCommission
		var rawStatus string
		if err := rows.Scan(new(int64), &v.ID, &v.OrderID, &v.UserUID, &v.UserEmail, &v.SaleID, &v.SaleName,
			&v.Type, &v.BaseCents, &v.Mode, &v.Value, &v.AmountCents, &rawStatus, &v.ConfirmAt, &v.CreatedAt); err != nil {
			return nil, 0, err
		}
		v.Status = effectiveSaleStatus(rawStatus, v.ConfirmAt)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM sale_commissions sc LEFT JOIN orders o ON o.id=sc.order_id LEFT JOIN users u ON u.id=sc.user_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func saleCommissionWhere(saleID, typ, status, start, end string) (string, []any) {
	where := ` WHERE 1=1`
	args := []any{}
	if saleID != "" {
		args = append(args, saleID)
		where += ` AND s.public_id::text=$` + itoa64(int64(len(args)))
	}
	if typ != "" {
		args = append(args, typ)
		where += ` AND sc.type=$` + itoa64(int64(len(args)))
	}
	if status == "pending" || status == "active" {
		// pending：到期时间未过且未作废；active：已确认或已到期的未作废记录
		if status == "active" {
			where += ` AND sc.status<>'invalid' AND (sc.status='active' OR sc.confirm_at IS NULL OR sc.confirm_at<=now())`
		} else {
			where += ` AND sc.status='pending' AND sc.confirm_at>now()`
		}
	} else if status == "invalid" {
		where += ` AND sc.status='invalid'`
	}
	if start != "" {
		args = append(args, start)
		where += ` AND sc.created_at>=$` + itoa64(int64(len(args))) + `::timestamptz`
	}
	if end != "" {
		args = append(args, end)
		where += ` AND sc.created_at<$` + itoa64(int64(len(args))) + `::timestamptz`
	}
	return where, args
}

func effectiveSaleStatus(raw string, confirmAt *time.Time) string {
	if raw == "invalid" {
		return "invalid"
	}
	if raw == "active" || confirmAt == nil || !confirmAt.After(time.Now()) {
		return "active"
	}
	return "pending"
}

// InvalidateSaleCommission 管理员把一条提成置为无效。
func (s *Store) InvalidateSaleCommission(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE sale_commissions SET status='invalid' WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SaleStatisticsRow 是一个销售的统计汇总。
type SaleStatisticsRow struct {
	SaleID       string `json:"sale_id"`
	SaleName     string `json:"sale_name"`
	OrderCount   int64  `json:"order_count"`
	SalesCents   int64  `json:"sales_cents"`
	ActiveCents  int64  `json:"active_cents"`
	PendingCents int64  `json:"pending_cents"`
}

// SaleStatistics 按销售汇总时间范围内的订单数、销售额与提成。
func (s *Store) SaleStatistics(ctx context.Context, start, end string) ([]SaleStatisticsRow, error) {
	rangeCond := ` AND o.status='paid' AND o.paid_at>=$1::timestamptz AND o.paid_at<$2::timestamptz`
	rows, err := s.DB.Query(ctx, `SELECT s.public_id::text,s.name,
count(o.id) FILTER (WHERE o.id IS NOT NULL),
coalesce(sum(o.total_cents) FILTER (WHERE o.id IS NOT NULL),0),
coalesce((SELECT sum(sc.amount_cents) FROM sale_commissions sc WHERE sc.sale_id=s.id AND sc.type<>'big_order'
  AND sc.status<>'invalid' AND (sc.status='active' OR sc.confirm_at IS NULL OR sc.confirm_at<=now())
  AND sc.created_at>=$1::timestamptz AND sc.created_at<$2::timestamptz),0),
coalesce((SELECT sum(sc.amount_cents) FROM sale_commissions sc WHERE sc.sale_id=s.id AND sc.status='pending' AND sc.confirm_at>now()
  AND sc.created_at>=$1::timestamptz AND sc.created_at<$2::timestamptz),0)
FROM sales s
LEFT JOIN sale_clients sc2 ON sc2.sale_id=s.id
LEFT JOIN orders o ON o.user_id=sc2.user_id`+rangeCond+`
GROUP BY s.id ORDER BY coalesce(sum(o.total_cents),0) DESC`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SaleStatisticsRow{}
	for rows.Next() {
		var v SaleStatisticsRow
		if err := rows.Scan(&v.SaleID, &v.SaleName, &v.OrderCount, &v.SalesCents, &v.ActiveCents, &v.PendingCents); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaleClientRanking 是消费总金额排名（按销售汇总其名下客户的消费）。
type SaleClientRankingRow struct {
	SaleID      string `json:"sale_id"`
	SaleName    string `json:"sale_name"`
	SalesCents  int64  `json:"sales_cents"`
	ClientCount int64  `json:"client_count"`
}

// SaleClientRanking 时间范围内名下客户的消费总金额排名。
func (s *Store) SaleClientRanking(ctx context.Context, start, end string) ([]SaleClientRankingRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT s.public_id::text,s.name,coalesce(sum(o.total_cents),0),count(DISTINCT sc.user_id)
FROM sales s
LEFT JOIN sale_clients sc ON sc.sale_id=s.id
LEFT JOIN orders o ON o.user_id=sc.user_id AND o.status='paid' AND o.paid_at>=$1::timestamptz AND o.paid_at<$2::timestamptz
GROUP BY s.id ORDER BY coalesce(sum(o.total_cents),0) DESC`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SaleClientRankingRow{}
	for rows.Next() {
		var v SaleClientRankingRow
		if err := rows.Scan(&v.SaleID, &v.SaleName, &v.SalesCents, &v.ClientCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
