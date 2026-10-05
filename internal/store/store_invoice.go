package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 发票申请（对齐魔方 CBAP 插件 IdcsmartInvoice）。
//
// 用户维护发票抬头 / 收件地址，选择已支付订单（开启 pre_invoice 后也支持未支付
// 订单预开票）发起申请；系统按发票项目的收税比例与快递方式算出需另行支付的
// 税金 / 快递费，生成一张 kind='artificial'、kind_detail='invoice_fee' 的人工
// 订单承载费用。费用支付完成后申请进入「待审核」，后台审核通过后发出。
//
// 状态流转：pending 待审核 → wait_send 待发出 → sent 已发出；另有 unpaid
// 待支付（费用未结清）、reject 已驳回、cancel 作废、flushed 已冲红。

// ---- 发票抬头 ----

// InvoiceTitle 是用户维护的发票抬头（插件 invoice_title）。
type InvoiceTitle struct {
	ID             string    `json:"id"`
	TitleType      string    `json:"title_type"` // company / person
	Title          string    `json:"title"`
	InvoiceType    string    `json:"invoice_type"` // normal 普票 / special 专票
	CompanyAddress string    `json:"company_address"`
	Tax            string    `json:"tax"`
	Bank           string    `json:"bank"`
	BankUser       string    `json:"bank_user"`
	CreatedAt      time.Time `json:"created_at"`
}

// InvoiceTitleInput 是抬头新增 / 修改表单。
type InvoiceTitleInput struct {
	TitleType      string `json:"title_type"`
	Title          string `json:"title"`
	InvoiceType    string `json:"invoice_type"`
	CompanyAddress string `json:"company_address"`
	Tax            string `json:"tax"`
	Bank           string `json:"bank"`
	BankUser       string `json:"bank_user"`
}

const invoiceTitleSelect = `SELECT public_id::text,title_type,title,invoice_type,company_address,tax,bank,bank_user,created_at FROM invoice_titles`

func scanInvoiceTitle(row pgx.Row) (InvoiceTitle, error) {
	var v InvoiceTitle
	err := row.Scan(&v.ID, &v.TitleType, &v.Title, &v.InvoiceType, &v.CompanyAddress, &v.Tax, &v.Bank, &v.BankUser, &v.CreatedAt)
	return v, err
}

func normalizeInvoiceTitle(in *InvoiceTitleInput) error {
	in.TitleType = strings.ToLower(strings.TrimSpace(in.TitleType))
	in.Title = strings.TrimSpace(in.Title)
	in.InvoiceType = strings.ToLower(strings.TrimSpace(in.InvoiceType))
	if in.TitleType != "company" && in.TitleType != "person" {
		return errors.New("抬头类型无效")
	}
	if in.Title == "" {
		return errors.New("请填写发票抬头")
	}
	if in.InvoiceType != "normal" && in.InvoiceType != "special" {
		return errors.New("发票类型无效")
	}
	in.CompanyAddress = strings.TrimSpace(in.CompanyAddress)
	in.Tax = strings.TrimSpace(in.Tax)
	in.Bank = strings.TrimSpace(in.Bank)
	in.BankUser = strings.TrimSpace(in.BankUser)
	if in.InvoiceType == "special" && in.Tax == "" {
		return errors.New("开具增值税专用发票需要填写税务登记号")
	}
	return nil
}

// ListInvoiceTitles 返回用户名下的全部抬头（新的在前）。
func (s *Store) ListInvoiceTitles(ctx context.Context, userID int64) ([]InvoiceTitle, error) {
	rows, err := s.DB.Query(ctx, invoiceTitleSelect+` WHERE user_id=$1 ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InvoiceTitle{}
	for rows.Next() {
		v, err := scanInvoiceTitle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateInvoiceTitle 新增抬头。
func (s *Store) CreateInvoiceTitle(ctx context.Context, userID int64, in InvoiceTitleInput) (InvoiceTitle, error) {
	if err := normalizeInvoiceTitle(&in); err != nil {
		return InvoiceTitle{}, err
	}
	return scanInvoiceTitle(s.DB.QueryRow(ctx, `INSERT INTO invoice_titles(user_id,title_type,title,invoice_type,company_address,tax,bank,bank_user)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING public_id::text,title_type,title,invoice_type,company_address,tax,bank,bank_user,created_at`,
		userID, in.TitleType, in.Title, in.InvoiceType, in.CompanyAddress, in.Tax, in.Bank, in.BankUser))
}

// UpdateInvoiceTitle 修改抬头（仅限本人）。
func (s *Store) UpdateInvoiceTitle(ctx context.Context, userID int64, publicID string, in InvoiceTitleInput) error {
	if err := normalizeInvoiceTitle(&in); err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE invoice_titles SET title_type=$3,title=$4,invoice_type=$5,company_address=$6,tax=$7,bank=$8,bank_user=$9,updated_at=now()
WHERE public_id=$1 AND user_id=$2`, publicID, userID, in.TitleType, in.Title, in.InvoiceType, in.CompanyAddress, in.Tax, in.Bank, in.BankUser)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInvoiceTitle 删除抬头（仅限本人；申请记录保存的是快照，不受影响）。
func (s *Store) DeleteInvoiceTitle(ctx context.Context, userID int64, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM invoice_titles WHERE public_id=$1 AND user_id=$2`, publicID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 收件地址 ----

// InvoiceAddress 是发票收件方式（纸质邮寄 / 电子邮箱），插件 invoice_address。
type InvoiceAddress struct {
	ID        string    `json:"id"`
	RecType   string    `json:"rec_type"` // paper / email
	RecName   string    `json:"rec_name"`
	Province  string    `json:"province"`
	City      string    `json:"city"`
	Region    string    `json:"region"`
	Address   string    `json:"address"`
	Phone     string    `json:"phone"`
	IsDefault bool      `json:"is_default"`
	Email     string    `json:"email"`
	RecURL    string    `json:"rec_url"`
	Notes     string    `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
}

// InvoiceAddressInput 是地址新增 / 修改表单。
type InvoiceAddressInput struct {
	RecType   string `json:"rec_type"`
	RecName   string `json:"rec_name"`
	Province  string `json:"province"`
	City      string `json:"city"`
	Region    string `json:"region"`
	Address   string `json:"address"`
	Phone     string `json:"phone"`
	IsDefault bool   `json:"is_default"`
	Email     string `json:"email"`
	RecURL    string `json:"rec_url"`
	Notes     string `json:"notes"`
}

const invoiceAddressSelect = `SELECT public_id::text,rec_type,rec_name,province,city,region,address,phone,is_default,email,rec_url,notes,created_at FROM invoice_addresses`

func scanInvoiceAddress(row pgx.Row) (InvoiceAddress, error) {
	var v InvoiceAddress
	err := row.Scan(&v.ID, &v.RecType, &v.RecName, &v.Province, &v.City, &v.Region, &v.Address, &v.Phone, &v.IsDefault, &v.Email, &v.RecURL, &v.Notes, &v.CreatedAt)
	return v, err
}

func normalizeInvoiceAddress(in *InvoiceAddressInput) error {
	in.RecType = strings.ToLower(strings.TrimSpace(in.RecType))
	in.RecName = strings.TrimSpace(in.RecName)
	in.Province = strings.TrimSpace(in.Province)
	in.City = strings.TrimSpace(in.City)
	in.Region = strings.TrimSpace(in.Region)
	in.Address = strings.TrimSpace(in.Address)
	in.Phone = strings.TrimSpace(in.Phone)
	in.Email = strings.TrimSpace(in.Email)
	in.RecURL = strings.TrimSpace(in.RecURL)
	in.Notes = strings.TrimSpace(in.Notes)
	if in.RecType != "paper" && in.RecType != "email" {
		return errors.New("收件方式无效")
	}
	if in.RecName == "" {
		return errors.New("请填写收件人")
	}
	if in.RecType == "paper" {
		if in.Address == "" {
			return errors.New("请填写详细地址")
		}
		if in.Phone == "" {
			return errors.New("请填写联系电话")
		}
	} else if in.Email == "" && in.RecURL == "" {
		return errors.New("请填写接收发票的邮箱或网址")
	}
	return nil
}

// ListInvoiceAddresses 返回用户名下的收件地址（默认地址在前）。
func (s *Store) ListInvoiceAddresses(ctx context.Context, userID int64) ([]InvoiceAddress, error) {
	rows, err := s.DB.Query(ctx, invoiceAddressSelect+` WHERE user_id=$1 ORDER BY is_default DESC,id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InvoiceAddress{}
	for rows.Next() {
		v, err := scanInvoiceAddress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateInvoiceAddress 新增收件地址；设为默认时清除原默认。
func (s *Store) CreateInvoiceAddress(ctx context.Context, userID int64, in InvoiceAddressInput) (InvoiceAddress, error) {
	if err := normalizeInvoiceAddress(&in); err != nil {
		return InvoiceAddress{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return InvoiceAddress{}, err
	}
	defer tx.Rollback(ctx)
	if in.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE invoice_addresses SET is_default=false WHERE user_id=$1 AND is_default=true`, userID); err != nil {
			return InvoiceAddress{}, err
		}
	}
	v, err := scanInvoiceAddress(tx.QueryRow(ctx, `INSERT INTO invoice_addresses(user_id,rec_type,rec_name,province,city,region,address,phone,is_default,email,rec_url,notes)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING public_id::text,rec_type,rec_name,province,city,region,address,phone,is_default,email,rec_url,notes,created_at`,
		userID, in.RecType, in.RecName, in.Province, in.City, in.Region, in.Address, in.Phone, in.IsDefault, in.Email, in.RecURL, in.Notes))
	if err != nil {
		return InvoiceAddress{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceAddress{}, err
	}
	return v, nil
}

// UpdateInvoiceAddress 修改收件地址（仅限本人）。
func (s *Store) UpdateInvoiceAddress(ctx context.Context, userID int64, publicID string, in InvoiceAddressInput) error {
	if err := normalizeInvoiceAddress(&in); err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if in.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE invoice_addresses SET is_default=false WHERE user_id=$1 AND is_default=true`, userID); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE invoice_addresses SET rec_type=$3,rec_name=$4,province=$5,city=$6,region=$7,address=$8,phone=$9,is_default=$10,email=$11,rec_url=$12,notes=$13,updated_at=now()
WHERE public_id=$1 AND user_id=$2`, publicID, userID, in.RecType, in.RecName, in.Province, in.City, in.Region, in.Address, in.Phone, in.IsDefault, in.Email, in.RecURL, in.Notes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// DeleteInvoiceAddress 删除收件地址（仅限本人）。
func (s *Store) DeleteInvoiceAddress(ctx context.Context, userID int64, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM invoice_addresses WHERE public_id=$1 AND user_id=$2`, publicID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 发票项目 ----

// InvoiceProject 是发票项目（票面税率与收税比例），插件 invoice_project。
// 对外的税率 / 收税比例单位是百分比（6 = 6%），存储用基点（100 = 1%）。
type InvoiceProject struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	NormalTaxRate    float64   `json:"normal_tax_rate"`
	NormalTaxPrice   float64   `json:"normal_tax_price"`
	SpecialTaxSwitch bool      `json:"special_tax_switch"`
	SpecialTaxRate   float64   `json:"special_tax_rate"`
	SpecialTaxPrice  float64   `json:"special_tax_price"`
	CreatedAt        time.Time `json:"created_at"`
}

// InvoiceProjectInput 是项目新增 / 修改表单。
type InvoiceProjectInput struct {
	Name             string  `json:"name"`
	NormalTaxRate    float64 `json:"normal_tax_rate"`
	NormalTaxPrice   float64 `json:"normal_tax_price"`
	SpecialTaxSwitch bool    `json:"special_tax_switch"`
	SpecialTaxRate   float64 `json:"special_tax_rate"`
	SpecialTaxPrice  float64 `json:"special_tax_price"`
}

const invoiceProjectSelect = `SELECT public_id::text,name,normal_tax_rate_bp,normal_tax_fee_bp,special_tax_switch,special_tax_rate_bp,special_tax_fee_bp,created_at FROM invoice_projects`

// invoicePercentBp 把百分比换算成基点（100 = 1%），并夹取到 [0,100] 百分比。
func invoicePercentBp(v float64) int {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return int(math.Round(v * 100))
}

// invoiceBpPercent 把基点还原成百分比。
func invoiceBpPercent(bp int) float64 {
	return float64(bp) / 100
}

func scanInvoiceProject(row pgx.Row) (InvoiceProject, error) {
	var v InvoiceProject
	var normalRate, normalFee, specialRate, specialFee int
	err := row.Scan(&v.ID, &v.Name, &normalRate, &normalFee, &v.SpecialTaxSwitch, &specialRate, &specialFee, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	v.NormalTaxRate = invoiceBpPercent(normalRate)
	v.NormalTaxPrice = invoiceBpPercent(normalFee)
	v.SpecialTaxRate = invoiceBpPercent(specialRate)
	v.SpecialTaxPrice = invoiceBpPercent(specialFee)
	return v, nil
}

func normalizeInvoiceProject(in *InvoiceProjectInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return errors.New("请填写发票项目名称")
	}
	return nil
}

// ListInvoiceProjects 返回全部发票项目（新的在前）。
func (s *Store) ListInvoiceProjects(ctx context.Context) ([]InvoiceProject, error) {
	rows, err := s.DB.Query(ctx, invoiceProjectSelect+` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InvoiceProject{}
	for rows.Next() {
		v, err := scanInvoiceProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateInvoiceProject 新增发票项目。
func (s *Store) CreateInvoiceProject(ctx context.Context, in InvoiceProjectInput) (InvoiceProject, error) {
	if err := normalizeInvoiceProject(&in); err != nil {
		return InvoiceProject{}, err
	}
	return scanInvoiceProject(s.DB.QueryRow(ctx, `INSERT INTO invoice_projects(name,normal_tax_rate_bp,normal_tax_fee_bp,special_tax_switch,special_tax_rate_bp,special_tax_fee_bp)
VALUES($1,$2,$3,$4,$5,$6) RETURNING public_id::text,name,normal_tax_rate_bp,normal_tax_fee_bp,special_tax_switch,special_tax_rate_bp,special_tax_fee_bp,created_at`,
		in.Name, invoicePercentBp(in.NormalTaxRate), invoicePercentBp(in.NormalTaxPrice), in.SpecialTaxSwitch, invoicePercentBp(in.SpecialTaxRate), invoicePercentBp(in.SpecialTaxPrice)))
}

// UpdateInvoiceProject 修改发票项目。
func (s *Store) UpdateInvoiceProject(ctx context.Context, publicID string, in InvoiceProjectInput) error {
	if err := normalizeInvoiceProject(&in); err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE invoice_projects SET name=$2,normal_tax_rate_bp=$3,normal_tax_fee_bp=$4,special_tax_switch=$5,special_tax_rate_bp=$6,special_tax_fee_bp=$7,updated_at=now()
WHERE public_id=$1`, publicID, in.Name, invoicePercentBp(in.NormalTaxRate), invoicePercentBp(in.NormalTaxPrice), in.SpecialTaxSwitch, invoicePercentBp(in.SpecialTaxRate), invoicePercentBp(in.SpecialTaxPrice))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInvoiceProject 删除发票项目（申请记录保存名称与比例快照，不受影响）。
func (s *Store) DeleteInvoiceProject(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM invoice_projects WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 发票设置 ----

// InvoiceParcel 是快递方式（名称 + 价格，价格落分），插件配置的 parcel 数组。
type InvoiceParcel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
}

// InvoiceConfig 对应插件 /invoice_config。
type InvoiceConfig struct {
	Manage     bool            `json:"invoice_manage"`
	PreInvoice bool            `json:"pre_invoice"`
	AcrossYear bool            `json:"across_year_invoice"`
	Parcel     []InvoiceParcel `json:"parcel"`
}

const invoiceConfigKey = "idcsmart_invoice"

// GetInvoiceConfig 读取发票设置；未配置时返回默认值（功能关闭、无快递方式）。
func (s *Store) GetInvoiceConfig(ctx context.Context) (InvoiceConfig, error) {
	out := InvoiceConfig{}
	err := s.settingGet(ctx, invoiceConfigKey, &out)
	if errors.Is(err, ErrNotFound) {
		return InvoiceConfig{}, nil
	}
	if err != nil {
		return InvoiceConfig{}, err
	}
	if out.Parcel == nil {
		out.Parcel = []InvoiceParcel{}
	}
	return out, nil
}

// SaveInvoiceConfig 保存发票设置（清洗快递方式：忽略空名称，价格不为负，补全编号）。
func (s *Store) SaveInvoiceConfig(ctx context.Context, cfg InvoiceConfig) error {
	parcel := make([]InvoiceParcel, 0, len(cfg.Parcel))
	seen := map[string]bool{}
	for _, p := range cfg.Parcel {
		p.ID = strings.TrimSpace(p.ID)
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			continue
		}
		if p.PriceCents < 0 {
			p.PriceCents = 0
		}
		if p.ID == "" || seen[p.ID] {
			p.ID = fmt.Sprintf("p%d", len(parcel)+1)
		}
		seen[p.ID] = true
		parcel = append(parcel, p)
	}
	cfg.Parcel = parcel
	return s.settingSave(ctx, invoiceConfigKey, cfg)
}

// ---- 可开票订单 ----

// InvoiceOrderItem 是订单明细行（只读展示）。
type InvoiceOrderItem struct {
	ProductName    string `json:"product_name"`
	BillingCycle   string `json:"billing_cycle"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	Quantity       int    `json:"quantity"`
	SubtotalCents  int64  `json:"subtotal_cents"`
}

// InvoiceOrderRow 是可开票订单行；invoice_id 非空表示已有有效申请（不可再选）。
type InvoiceOrderRow struct {
	ID            string             `json:"id"`
	Status        string             `json:"status"`
	TotalCents    int64              `json:"total_cents"`
	Currency      string             `json:"currency"`
	CreatedAt     time.Time          `json:"created_at"`
	PaidAt        *time.Time         `json:"paid_at"`
	Items         []InvoiceOrderItem `json:"items"`
	InvoiceID     string             `json:"invoice_id"`
	InvoiceStatus string             `json:"invoice_status"`
	IsCrossYear   bool               `json:"is_cross_year"`
}

// invoiceActiveStatuses 是占用订单的有效申请状态；cancel / reject / flushed 放开重开。
const invoiceActiveStatuses = `('pending','unpaid','wait_send','sent')`

// InvoiceRequestableOrders 返回可申请发票的订单。
// status=Paid（默认）列出已支付订单；status=Unpaid 列出未支付订单（需开启预开票）。
func (s *Store) InvoiceRequestableOrders(ctx context.Context, userID int64, status string) ([]InvoiceOrderRow, error) {
	cfg, err := s.GetInvoiceConfig(ctx)
	if err != nil {
		return nil, err
	}
	paid := !strings.EqualFold(strings.TrimSpace(status), "Unpaid")
	if !paid && !cfg.PreInvoice {
		return []InvoiceOrderRow{}, nil
	}
	cond := `o.user_id=$1 AND o.kind_detail <> 'invoice_fee'`
	if paid {
		cond += ` AND o.status IN ('paid','processing','completed')`
	} else {
		cond += ` AND o.status='unpaid'`
	}
	rows, err := s.DB.Query(ctx, `SELECT o.public_id::text,o.status,o.total_cents,o.currency,o.created_at,o.paid_at,
COALESCE((SELECT json_agg(json_build_object('product_name',oi.product_name,'billing_cycle',oi.billing_cycle,'unit_price_cents',oi.unit_price_cents,'quantity',oi.quantity,'subtotal_cents',oi.subtotal_cents) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id),'[]'::json),
COALESCE((SELECT r2.public_id::text FROM invoice_request_orders ro JOIN invoice_requests r2 ON r2.id=ro.invoice_id
 WHERE ro.order_id=o.id AND r2.status IN `+invoiceActiveStatuses+` ORDER BY r2.id DESC LIMIT 1),''),
COALESCE((SELECT r3.status FROM invoice_request_orders ro2 JOIN invoice_requests r3 ON r3.id=ro2.invoice_id
 WHERE ro2.order_id=o.id AND r3.status IN `+invoiceActiveStatuses+` ORDER BY r3.id DESC LIMIT 1),'')
FROM orders o WHERE `+cond+` ORDER BY o.created_at DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InvoiceOrderRow{}
	now := time.Now()
	for rows.Next() {
		var v InvoiceOrderRow
		var items []byte
		if err := rows.Scan(&v.ID, &v.Status, &v.TotalCents, &v.Currency, &v.CreatedAt, &v.PaidAt, &items, &v.InvoiceID, &v.InvoiceStatus); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(items, &v.Items)
		v.IsCrossYear = v.CreatedAt.Year() < now.Year()
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- 金额计算 ----

// invoiceAmounts 是一次申请 / 试算的金额拆分（单位：分）。
type invoiceAmounts struct {
	AmountCents int64  // 票面金额（订单合计）
	TaxRateBp   int    // 票面税率（基点）
	TaxFeeBp    int    // 收税比例（基点）
	TaxCents    int64  // 向客户收取的税金
	ParcelName  string // 快递方式名称
	ParcelPrice int64  // 快递费
	TotalCents  int64  // 票面合计 = 订单金额 + 税金 + 快递费
	FeeCents    int64  // 需另行支付 = 税金 + 快递费
}

// invoiceParcelByID 从设置里按编号找快递方式。
func invoiceParcelByID(cfg InvoiceConfig, id string) (InvoiceParcel, bool) {
	id = strings.TrimSpace(id)
	for _, p := range cfg.Parcel {
		if p.ID == id {
			return p, true
		}
	}
	return InvoiceParcel{}, false
}

// invoiceAmountsOf 按发票项目与收件方式计算金额拆分；专票需项目开启专票开关。
func invoiceAmountsOf(project InvoiceProject, invoiceType, recType, parcelID string, cfg InvoiceConfig, amountCents int64) (invoiceAmounts, error) {
	out := invoiceAmounts{AmountCents: amountCents}
	switch invoiceType {
	case "normal":
		out.TaxRateBp = invoicePercentBp(project.NormalTaxRate)
		out.TaxFeeBp = invoicePercentBp(project.NormalTaxPrice)
	case "special":
		if !project.SpecialTaxSwitch {
			return invoiceAmounts{}, errors.New("该发票项目未开启增值税专用发票")
		}
		out.TaxRateBp = invoicePercentBp(project.SpecialTaxRate)
		out.TaxFeeBp = invoicePercentBp(project.SpecialTaxPrice)
	default:
		return invoiceAmounts{}, errors.New("发票类型无效")
	}
	if recType == "paper" {
		p, ok := invoiceParcelByID(cfg, parcelID)
		if !ok {
			return invoiceAmounts{}, errors.New("请选择快递方式")
		}
		out.ParcelName = p.Name
		out.ParcelPrice = p.PriceCents
	}
	out.TaxCents = amountCents * int64(out.TaxFeeBp) / 10000
	out.FeeCents = out.TaxCents + out.ParcelPrice
	out.TotalCents = amountCents + out.TaxCents + out.ParcelPrice
	return out, nil
}

// ---- 开票试算 ----

// InvoiceQuoteInput 是试算 / 申请共用的选择项（字段名与插件一致）。
type InvoiceQuoteInput struct {
	OrderPublicIDs  []string `json:"order_ids"`
	ProjectPublicID string   `json:"project_id"`
	InvoiceType     string   `json:"invoice_type"`
	RecType         string   `json:"rec_type"`
	ParcelPublicID  string   `json:"parcel_id"`
}

// InvoiceQuoteHost 是试算返回的明细行。
type InvoiceQuoteHost struct {
	OrderID      string `json:"order_id"`
	ProductName  string `json:"product_name"`
	BillingCycle string `json:"billing_cycle"`
	AmountCents  int64  `json:"amount_cents"`
}

// InvoiceQuoteResult 是试算结果，字段名与插件 /invoice/price 对齐。
type InvoiceQuoteResult struct {
	PriceCents       int64              `json:"price"`
	TaxRate          float64            `json:"tax_rate"`
	TaxFee           float64            `json:"tax_fee"`
	TaxPriceCents    int64              `json:"tax_price"`
	ParcelName       string             `json:"parcel_name"`
	ParcelPriceCents int64              `json:"parcel_price"`
	TotalCents       int64              `json:"total"`
	FeeCents         int64              `json:"fee"`
	Host             []InvoiceQuoteHost `json:"host"`
}

// invoiceOrdersForApply 校验所选订单可开票并汇总金额 / 明细（单据事务内也可调用）。
// 返回：订单金额合计、明细行、订单内部 id、币种。
func (s *Store) invoiceOrdersForApply(ctx context.Context, q rowQuerier, userID int64, publicIDs []string, cfg InvoiceConfig) (int64, []InvoiceQuoteHost, []int64, string, error) {
	ids := make([]string, 0, len(publicIDs))
	seen := map[string]bool{}
	for _, id := range publicIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return 0, nil, nil, "", errors.New("请选择要开票的订单")
	}
	if len(ids) > 50 {
		return 0, nil, nil, "", errors.New("一次最多为 50 笔订单申请发票")
	}
	rows, err := q.Query(ctx, `SELECT o.id,o.public_id::text,o.status,o.total_cents,o.currency,o.created_at,o.kind_detail
FROM orders o WHERE o.public_id=ANY($1) AND o.user_id=$2`, ids, userID)
	if err != nil {
		return 0, nil, nil, "", err
	}
	defer rows.Close()
	type invoiceOrderInfo struct {
		id         int64
		publicID   string
		status     string
		total      int64
		currency   string
		createdAt  time.Time
		kindDetail string
	}
	found := map[string]invoiceOrderInfo{}
	for rows.Next() {
		var oi invoiceOrderInfo
		if err := rows.Scan(&oi.id, &oi.publicID, &oi.status, &oi.total, &oi.currency, &oi.createdAt, &oi.kindDetail); err != nil {
			return 0, nil, nil, "", err
		}
		found[oi.publicID] = oi
	}
	if err := rows.Err(); err != nil {
		return 0, nil, nil, "", err
	}
	now := time.Now()
	order := make([]invoiceOrderInfo, 0, len(ids))
	amount := int64(0)
	currency := ""
	for _, id := range ids {
		oi, ok := found[id]
		if !ok {
			return 0, nil, nil, "", ErrNotFound
		}
		if oi.kindDetail == "invoice_fee" {
			return 0, nil, nil, "", errors.New("发票费用单不可再开票")
		}
		switch oi.status {
		case "unpaid":
			if !cfg.PreInvoice {
				return 0, nil, nil, "", errors.New("该订单尚未支付，不允许预开票")
			}
		case "paid", "processing", "completed":
		default:
			return 0, nil, nil, "", errors.New("订单状态不支持开具发票")
		}
		if oi.createdAt.Year() < now.Year() && !cfg.AcrossYear {
			return 0, nil, nil, "", errors.New("跨年发票无法自动开具，请联系管理员开启跨年开票")
		}
		if currency == "" {
			currency = oi.currency
		} else if currency != oi.currency {
			return 0, nil, nil, "", errors.New("所选订单币种不一致")
		}
		amount += oi.total
		order = append(order, oi)
	}
	orderIDs := make([]int64, 0, len(order))
	for _, oi := range order {
		orderIDs = append(orderIDs, oi.id)
	}
	var occupied int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM invoice_request_orders ro JOIN invoice_requests r ON r.id=ro.invoice_id
WHERE ro.order_id=ANY($1) AND r.status IN `+invoiceActiveStatuses, orderIDs).Scan(&occupied); err != nil {
		return 0, nil, nil, "", err
	}
	if occupied > 0 {
		return 0, nil, nil, "", errors.New("所选订单已有关联的发票申请")
	}
	itemRows, err := q.Query(ctx, `SELECT order_id,product_name,billing_cycle,subtotal_cents FROM order_items WHERE order_id=ANY($1) ORDER BY id`, orderIDs)
	if err != nil {
		return 0, nil, nil, "", err
	}
	defer itemRows.Close()
	itemsByOrder := map[int64][]InvoiceQuoteHost{}
	for itemRows.Next() {
		var oid int64
		var h InvoiceQuoteHost
		if err := itemRows.Scan(&oid, &h.ProductName, &h.BillingCycle, &h.AmountCents); err != nil {
			return 0, nil, nil, "", err
		}
		itemsByOrder[oid] = append(itemsByOrder[oid], h)
	}
	if err := itemRows.Err(); err != nil {
		return 0, nil, nil, "", err
	}
	host := []InvoiceQuoteHost{}
	for _, oi := range order {
		items := itemsByOrder[oi.id]
		if len(items) == 0 {
			items = []InvoiceQuoteHost{{ProductName: "订单金额", AmountCents: oi.total}}
		}
		for i := range items {
			items[i].OrderID = oi.publicID
		}
		host = append(host, items...)
	}
	return amount, host, orderIDs, currency, nil
}

// InvoiceQuote 试算需支付的税金 / 快递费，不产生申请。
func (s *Store) InvoiceQuote(ctx context.Context, userID int64, in InvoiceQuoteInput) (InvoiceQuoteResult, error) {
	cfg, err := s.GetInvoiceConfig(ctx)
	if err != nil {
		return InvoiceQuoteResult{}, err
	}
	amount, host, _, _, err := s.invoiceOrdersForApply(ctx, s.DB, userID, in.OrderPublicIDs, cfg)
	if err != nil {
		return InvoiceQuoteResult{}, err
	}
	project, err := scanInvoiceProject(s.DB.QueryRow(ctx, invoiceProjectSelect+` WHERE public_id=$1`, strings.TrimSpace(in.ProjectPublicID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceQuoteResult{}, ErrNotFound
	}
	if err != nil {
		return InvoiceQuoteResult{}, err
	}
	invoiceType := strings.ToLower(strings.TrimSpace(in.InvoiceType))
	if invoiceType == "" {
		invoiceType = "normal"
	}
	recType := strings.ToLower(strings.TrimSpace(in.RecType))
	if recType != "paper" {
		recType = "email"
	}
	amounts, err := invoiceAmountsOf(project, invoiceType, recType, in.ParcelPublicID, cfg, amount)
	if err != nil {
		return InvoiceQuoteResult{}, err
	}
	return InvoiceQuoteResult{
		PriceCents:       amounts.AmountCents,
		TaxRate:          invoiceBpPercent(amounts.TaxRateBp),
		TaxFee:           invoiceBpPercent(amounts.TaxFeeBp),
		TaxPriceCents:    amounts.TaxCents,
		ParcelName:       amounts.ParcelName,
		ParcelPriceCents: amounts.ParcelPrice,
		TotalCents:       amounts.TotalCents,
		FeeCents:         amounts.FeeCents,
		Host:             host,
	}, nil
}

// ---- 发票申请 ----

// InvoiceRequestOrder 是申请关联的订单快照。
type InvoiceRequestOrder struct {
	ID         string             `json:"id"`
	Status     string             `json:"status"`
	TotalCents int64              `json:"total_cents"`
	Currency   string             `json:"currency"`
	CreatedAt  time.Time          `json:"created_at"`
	Items      []InvoiceOrderItem `json:"items"`
}

// InvoiceRequest 是一条发票申请（用户端 / 后台共用）。
type InvoiceRequest struct {
	ID               string                `json:"id"`
	Status           string                `json:"status"`
	UserID           string                `json:"user_id,omitempty"`
	UserEmail        string                `json:"user_email,omitempty"`
	TitleType        string                `json:"title_type"`
	Title            string                `json:"title"`
	InvoiceType      string                `json:"invoice_type"`
	Tax              string                `json:"tax"`
	CompanyAddress   string                `json:"company_address"`
	Bank             string                `json:"bank"`
	BankUser         string                `json:"bank_user"`
	RecType          string                `json:"rec_type"`
	RecName          string                `json:"rec_name"`
	RecAddress       string                `json:"rec_address"`
	RecPhone         string                `json:"rec_phone"`
	RecEmail         string                `json:"rec_email"`
	RecURL           string                `json:"rec_url"`
	InvoiceFormat    string                `json:"invoice_format"`
	InvoiceProject   string                `json:"invoice_project"`
	TaxRate          float64               `json:"tax_rate"`
	TaxFee           float64               `json:"tax_fee"`
	AmountCents      int64                 `json:"amount_cents"`
	TaxCents         int64                 `json:"tax_cents"`
	ParcelName       string                `json:"parcel_name"`
	ParcelPriceCents int64                 `json:"parcel_price_cents"`
	TotalCents       int64                 `json:"total_cents"`
	FeeCents         int64                 `json:"fee_cents"`
	FeeOrderID       string                `json:"fee_order_id"`
	FeeOrderStatus   string                `json:"fee_order_status"`
	ParcelNumber     string                `json:"parcel_number"`
	ReviewNotes      string                `json:"review_notes"`
	RejectReason     string                `json:"reject_reason"`
	InvoiceFilename  string                `json:"invoice_filename"`
	HasFile          bool                  `json:"has_file"`
	SentAt           *time.Time            `json:"sent_at"`
	FlushedAt        *time.Time            `json:"flushed_at"`
	CreatedAt        time.Time             `json:"created_at"`
	Orders           []InvoiceRequestOrder `json:"orders"`
}

const invoiceRequestSelect = `SELECT r.public_id::text,r.status,r.title_type,r.title,r.invoice_type,r.tax,r.company_address,r.bank,r.bank_user,
r.rec_type,r.rec_name,r.rec_address,r.rec_phone,r.rec_email,r.rec_url,r.invoice_format,r.invoice_project,
r.tax_rate_bp,r.tax_fee_bp,r.amount_cents,r.tax_cents,r.parcel_name,r.parcel_price_cents,r.total_cents,r.fee_cents,
COALESCE(fo.public_id::text,''),COALESCE(fo.status,''),
r.parcel_number,r.review_notes,r.reject_reason,r.invoice_filename,r.sent_at,r.flushed_at,r.created_at,
u.public_id::text,u.email,
COALESCE((SELECT json_agg(json_build_object('id',o.public_id::text,'status',o.status,'total_cents',o.total_cents,'currency',o.currency,'created_at',o.created_at,
'items',COALESCE((SELECT json_agg(json_build_object('product_name',oi.product_name,'billing_cycle',oi.billing_cycle,'unit_price_cents',oi.unit_price_cents,'quantity',oi.quantity,'subtotal_cents',oi.subtotal_cents) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id),'[]'::json)) ORDER BY o.id)
FROM invoice_request_orders ro JOIN orders o ON o.id=ro.order_id WHERE ro.invoice_id=r.id),'[]'::json)
FROM invoice_requests r JOIN users u ON u.id=r.user_id LEFT JOIN orders fo ON fo.id=r.fee_order_id`

func scanInvoiceRequest(row pgx.Row) (InvoiceRequest, error) {
	var v InvoiceRequest
	var orders []byte
	var taxRateBp, taxFeeBp int
	err := row.Scan(&v.ID, &v.Status, &v.TitleType, &v.Title, &v.InvoiceType, &v.Tax, &v.CompanyAddress, &v.Bank, &v.BankUser,
		&v.RecType, &v.RecName, &v.RecAddress, &v.RecPhone, &v.RecEmail, &v.RecURL, &v.InvoiceFormat, &v.InvoiceProject,
		&taxRateBp, &taxFeeBp, &v.AmountCents, &v.TaxCents, &v.ParcelName, &v.ParcelPriceCents, &v.TotalCents, &v.FeeCents,
		&v.FeeOrderID, &v.FeeOrderStatus,
		&v.ParcelNumber, &v.ReviewNotes, &v.RejectReason, &v.InvoiceFilename, &v.SentAt, &v.FlushedAt, &v.CreatedAt,
		&v.UserID, &v.UserEmail, &orders)
	if err != nil {
		return v, err
	}
	v.TaxRate = invoiceBpPercent(taxRateBp)
	v.TaxFee = invoiceBpPercent(taxFeeBp)
	v.HasFile = strings.TrimSpace(v.InvoiceFilename) != ""
	_ = json.Unmarshal(orders, &v.Orders)
	if v.Orders == nil {
		v.Orders = []InvoiceRequestOrder{}
	}
	return v, nil
}

// GetInvoiceRequest 读取一条申请；admin=false 时仅限本人。
func (s *Store) GetInvoiceRequest(ctx context.Context, userID int64, publicID string, admin bool) (InvoiceRequest, error) {
	where := ` WHERE r.public_id=$1`
	args := []any{publicID}
	if !admin {
		where += ` AND r.user_id=$2`
		args = append(args, userID)
	}
	v, err := scanInvoiceRequest(s.DB.QueryRow(ctx, invoiceRequestSelect+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceRequest{}, ErrNotFound
	}
	return v, err
}

// invoiceRequestWhere 组装列表筛选条件（含占位符编号），返回值以 WHERE 开头。
func invoiceRequestWhere(userID int64, admin bool, status, keyword string, args *[]any) string {
	conds := []string{}
	if !admin {
		*args = append(*args, userID)
		conds = append(conds, fmt.Sprintf("r.user_id=$%d", len(*args)))
	}
	if status = strings.TrimSpace(status); status != "" && status != "all" {
		*args = append(*args, status)
		conds = append(conds, fmt.Sprintf("r.status=$%d", len(*args)))
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		*args = append(*args, "%"+keyword+"%")
		n := len(*args)
		conds = append(conds, fmt.Sprintf("(r.title ILIKE $%d OR r.rec_name ILIKE $%d OR u.email ILIKE $%d)", n, n, n))
	}
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// ListUserInvoiceRequests 返回用户名下的申请（新的在前）。
func (s *Store) ListUserInvoiceRequests(ctx context.Context, userID int64, status string, limit, offset int) ([]InvoiceRequest, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	args := []any{}
	where := invoiceRequestWhere(userID, false, status, "", &args)
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM invoice_requests r JOIN users u ON u.id=r.user_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.DB.Query(ctx, invoiceRequestSelect+where+fmt.Sprintf(` ORDER BY r.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []InvoiceRequest{}
	for rows.Next() {
		v, err := scanInvoiceRequest(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// ListAdminInvoiceRequests 后台列表：状态筛选 + 关键字（抬头 / 收件人 / 邮箱）。
func (s *Store) ListAdminInvoiceRequests(ctx context.Context, status, keyword string, limit, offset int) ([]InvoiceRequest, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	args := []any{}
	where := invoiceRequestWhere(0, true, status, keyword, &args)
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM invoice_requests r JOIN users u ON u.id=r.user_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.DB.Query(ctx, invoiceRequestSelect+where+fmt.Sprintf(` ORDER BY r.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []InvoiceRequest{}
	for rows.Next() {
		v, err := scanInvoiceRequest(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// InvoiceCreateInput 是创建申请的表单（字段名与插件一致）。
type InvoiceCreateInput struct {
	OrderPublicIDs  []string `json:"order_ids"`
	TitlePublicID   string   `json:"title_id"`
	AddressPublicID string   `json:"address_id"`
	ProjectPublicID string   `json:"project_id"`
	ParcelPublicID  string   `json:"parcel_id"`
	InvoiceFormat   string   `json:"invoice_format"`
}

// invoiceFeeDescription 生成费用单摘要。
func invoiceFeeDescription(a invoiceAmounts) string {
	parts := []string{}
	if a.TaxCents > 0 {
		parts = append(parts, fmt.Sprintf("税金 %.2f 元", float64(a.TaxCents)/100))
	}
	if a.ParcelPrice > 0 {
		parts = append(parts, fmt.Sprintf("快递费 %.2f 元", float64(a.ParcelPrice)/100))
	}
	if len(parts) == 0 {
		return "发票费用"
	}
	return "发票费用（" + strings.Join(parts, "、") + "）"
}

// CreateInvoiceRequest 创建发票申请：校验订单 / 抬头 / 地址 / 项目，计算税金与
// 快递费；需支付的费用生成一张 kind='artificial'、kind_detail='invoice_fee' 的
// 人工订单，申请置为 unpaid 待支付，否则直接进入 pending 待审核。
func (s *Store) CreateInvoiceRequest(ctx context.Context, userID int64, in InvoiceCreateInput) (InvoiceRequest, error) {
	cfg, err := s.GetInvoiceConfig(ctx)
	if err != nil {
		return InvoiceRequest{}, err
	}
	format := strings.ToLower(strings.TrimSpace(in.InvoiceFormat))
	if format == "" {
		format = "pdf"
	}
	if format != "pdf" && format != "ofd" && format != "xml" {
		return InvoiceRequest{}, errors.New("发票格式无效")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return InvoiceRequest{}, err
	}
	defer tx.Rollback(ctx)
	title, err := scanInvoiceTitle(tx.QueryRow(ctx, invoiceTitleSelect+` WHERE public_id=$1 AND user_id=$2 FOR SHARE`, strings.TrimSpace(in.TitlePublicID), userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceRequest{}, ErrNotFound
	}
	if err != nil {
		return InvoiceRequest{}, err
	}
	address, err := scanInvoiceAddress(tx.QueryRow(ctx, invoiceAddressSelect+` WHERE public_id=$1 AND user_id=$2 FOR SHARE`, strings.TrimSpace(in.AddressPublicID), userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceRequest{}, ErrNotFound
	}
	if err != nil {
		return InvoiceRequest{}, err
	}
	if format != "pdf" && address.RecType != "email" {
		return InvoiceRequest{}, errors.New("OFD / XML 格式仅支持电子发票")
	}
	project, err := scanInvoiceProject(tx.QueryRow(ctx, invoiceProjectSelect+` WHERE public_id=$1`, strings.TrimSpace(in.ProjectPublicID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceRequest{}, ErrNotFound
	}
	if err != nil {
		return InvoiceRequest{}, err
	}
	amount, _, orderIDs, currency, err := s.invoiceOrdersForApply(ctx, tx, userID, in.OrderPublicIDs, cfg)
	if err != nil {
		return InvoiceRequest{}, err
	}
	amounts, err := invoiceAmountsOf(project, title.InvoiceType, address.RecType, in.ParcelPublicID, cfg, amount)
	if err != nil {
		return InvoiceRequest{}, err
	}
	if currency == "" {
		currency = "CNY"
	}
	recAddress := strings.TrimSpace(strings.Join([]string{address.Province, address.City, address.Region, address.Address}, ""))
	var requestID int64
	var publicID string
	if err := tx.QueryRow(ctx, `INSERT INTO invoice_requests(user_id,status,title_type,title,invoice_type,tax,company_address,bank,bank_user,
rec_type,rec_name,rec_address,rec_phone,rec_email,rec_url,invoice_format,invoice_project,tax_rate_bp,tax_fee_bp,
amount_cents,tax_cents,parcel_name,parcel_price_cents,total_cents,fee_cents)
VALUES($1,'pending',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
RETURNING id,public_id::text`,
		userID, title.TitleType, title.Title, title.InvoiceType, title.Tax, title.CompanyAddress, title.Bank, title.BankUser,
		address.RecType, address.RecName, recAddress, address.Phone, address.Email, address.RecURL, format, project.Name,
		amounts.TaxRateBp, amounts.TaxFeeBp, amounts.AmountCents, amounts.TaxCents, amounts.ParcelName, amounts.ParcelPrice,
		amounts.TotalCents, amounts.FeeCents).Scan(&requestID, &publicID); err != nil {
		return InvoiceRequest{}, err
	}
	for _, oid := range orderIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO invoice_request_orders(invoice_id,order_id) VALUES($1,$2)`, requestID, oid); err != nil {
			return InvoiceRequest{}, err
		}
	}
	if amounts.FeeCents > 0 {
		desc := invoiceFeeDescription(amounts)
		var feeOrderID int64
		if err := tx.QueryRow(ctx, `INSERT INTO orders(user_id,status,kind,total_cents,currency,kind_detail,pay_method,created_at,updated_at)
VALUES($1,'unpaid','artificial',$2,$3,'invoice_fee','prepaid',now(),now()) RETURNING id`,
			userID, amounts.FeeCents, currency).Scan(&feeOrderID); err != nil {
			return InvoiceRequest{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,unit_price_cents,quantity,subtotal_cents,provider_type)
VALUES($1,NULL,$2,'onetime',$3,1,$3,'manual')`, feeOrderID, desc, amounts.FeeCents); err != nil {
			return InvoiceRequest{}, err
		}
		var billingInvoiceID int64
		if err := tx.QueryRow(ctx, `INSERT INTO invoices(order_id,user_id,status,total_cents,currency,due_at)
VALUES($1,$2,'unpaid',$3,$4,now()+interval '7 days') RETURNING id`, feeOrderID, userID, amounts.FeeCents, currency).Scan(&billingInvoiceID); err != nil {
			return InvoiceRequest{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,amount_cents) VALUES($1,$2,$3)`, billingInvoiceID, desc, amounts.FeeCents); err != nil {
			return InvoiceRequest{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE invoice_requests SET status='unpaid',fee_order_id=$2,updated_at=now() WHERE id=$1`, requestID, feeOrderID); err != nil {
			return InvoiceRequest{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceRequest{}, err
	}
	return s.GetInvoiceRequest(ctx, userID, publicID, false)
}

// CancelInvoiceRequest 用户作废申请（待审核 / 待支付 / 已驳回可作废）；
// 关联的未支付费用单一并作废。
func (s *Store) CancelInvoiceRequest(ctx context.Context, userID int64, publicID string) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var requestID int64
	var status string
	var feeOrderID *int64
	err = tx.QueryRow(ctx, `SELECT id,status,fee_order_id FROM invoice_requests WHERE public_id=$1 AND user_id=$2 FOR UPDATE`, publicID, userID).
		Scan(&requestID, &status, &feeOrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	switch status {
	case "pending", "unpaid", "reject":
	default:
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `UPDATE invoice_requests SET status='cancel',updated_at=now() WHERE id=$1`, requestID); err != nil {
		return err
	}
	if feeOrderID != nil {
		if _, err := tx.Exec(ctx, `UPDATE orders SET status='cancelled',cancelled_at=now(),updated_at=now() WHERE id=$1 AND status='unpaid'`, *feeOrderID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invoices SET status='void' WHERE order_id=$1 AND status='unpaid'`, *feeOrderID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// invoiceRequestExistsOr 状态更新未命中时区分「不存在」与「状态不允许」。
func (s *Store) invoiceRequestExistsOr(ctx context.Context, publicID string, fallback error) error {
	var one int
	err := s.DB.QueryRow(ctx, `SELECT 1 FROM invoice_requests WHERE public_id=$1`, publicID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fallback
}

// ConfirmInvoiceRequest 后台审核通过：pending → wait_send。
func (s *Store) ConfirmInvoiceRequest(ctx context.Context, publicID, notes string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE invoice_requests SET status='wait_send',review_notes=$2,updated_at=now() WHERE public_id=$1 AND status='pending'`, publicID, strings.TrimSpace(notes))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.invoiceRequestExistsOr(ctx, publicID, ErrInvalidState)
	}
	return nil
}

// RejectInvoiceRequest 后台驳回：pending / wait_send → reject，需填写原因。
func (s *Store) RejectInvoiceRequest(ctx context.Context, publicID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("请填写驳回原因")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE invoice_requests SET status='reject',reject_reason=$2,updated_at=now() WHERE public_id=$1 AND status IN ('pending','wait_send')`, publicID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.invoiceRequestExistsOr(ctx, publicID, ErrInvalidState)
	}
	return nil
}

// SendInvoiceRequest 后台发出：wait_send → sent。
// 纸质发票需填快递单号；电子发票需先上传发票文件。
func (s *Store) SendInvoiceRequest(ctx context.Context, publicID, parcelNumber string) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var requestID int64
	var status, recType, filename string
	err = tx.QueryRow(ctx, `SELECT id,status,rec_type,invoice_filename FROM invoice_requests WHERE public_id=$1 FOR UPDATE`, publicID).
		Scan(&requestID, &status, &recType, &filename)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "wait_send" {
		return ErrInvalidState
	}
	parcelNumber = strings.TrimSpace(parcelNumber)
	if recType == "paper" && parcelNumber == "" {
		return errors.New("请填写快递单号")
	}
	if recType == "email" && strings.TrimSpace(filename) == "" {
		return errors.New("请先上传发票文件")
	}
	if _, err := tx.Exec(ctx, `UPDATE invoice_requests SET status='sent',parcel_number=$2,sent_at=now(),updated_at=now() WHERE id=$1`, requestID, parcelNumber); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FlushInvoiceRequest 后台冲红：sent → flushed。
func (s *Store) FlushInvoiceRequest(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE invoice_requests SET status='flushed',flushed_at=now(),updated_at=now() WHERE public_id=$1 AND status='sent'`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.invoiceRequestExistsOr(ctx, publicID, ErrInvalidState)
	}
	return nil
}

// SetInvoiceRequestFile 记录后台上传的发票文件（本机相对路径或 OSS 地址）。
func (s *Store) SetInvoiceRequestFile(ctx context.Context, publicID, filename string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE invoice_requests SET invoice_filename=$2,updated_at=now() WHERE public_id=$1 AND status<>'cancel'`, publicID, strings.TrimSpace(filename))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.invoiceRequestExistsOr(ctx, publicID, ErrInvalidState)
	}
	return nil
}

// DeleteInvoiceRequestFile 删除发票文件记录。
func (s *Store) DeleteInvoiceRequestFile(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE invoice_requests SET invoice_filename='',updated_at=now() WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InvoiceRequestFilename 读取发票文件地址；admin=false 时仅限本人，文件不存在返回 ErrNotFound。
func (s *Store) InvoiceRequestFilename(ctx context.Context, userID int64, publicID string, admin bool) (string, error) {
	where := ` WHERE public_id=$1`
	args := []any{publicID}
	if !admin {
		where += ` AND user_id=$2`
		args = append(args, userID)
	}
	var filename string
	err := s.DB.QueryRow(ctx, `SELECT invoice_filename FROM invoice_requests`+where, args...).Scan(&filename)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(filename) == "" {
		return "", ErrNotFound
	}
	return filename, nil
}

// advanceInvoiceFeeOrderTx 在发票费用单支付完成的同一事务里，把关联申请推进到「待审核」。
func advanceInvoiceFeeOrderTx(ctx context.Context, tx pgx.Tx, orderID int64) error {
	var kindDetail string
	if err := tx.QueryRow(ctx, `SELECT kind_detail FROM orders WHERE id=$1`, orderID).Scan(&kindDetail); err != nil {
		return err
	}
	if kindDetail != "invoice_fee" {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE invoice_requests SET status='pending',updated_at=now() WHERE fee_order_id=$1 AND status='unpaid'`, orderID)
	return err
}
