package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 电子合同业务错误集合。
var (
	// ErrEContractDisabled 合同功能未开启。
	ErrEContractDisabled = errors.New("e-contract: disabled")
	// ErrEContractTooOld 订单超出可申请时间窗。
	ErrEContractTooOld = errors.New("e-contract: order too old")
	// ErrEContractExists 该订单已有有效合同。
	ErrEContractExists = errors.New("e-contract: already applied")
	// ErrEContractNoTemplate 没有可用的启用模板。
	ErrEContractNoTemplate = errors.New("e-contract: no active template")
)

// 电子合同（对齐魔方 CBAP EContract 插件）。
//
// 模板（内容支持 {{变量}}，关联商品限定可申请范围）→ 用户对已支付订单申请
// （状态 pending，编号 = 前缀 + 起始编号自动递增）→ 用户签订（签名图 data URL）
// → 后台审核（通过 effective / 驳回 reject / 作废 cancel）→ 邮寄登记 /
// 下载可打印 HTML。插件对接的第三方电子签通道加密不可读，签订为站内流程。

// EContractVar 是模板可用变量。
type EContractVar struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// EContractVars 返回模板内容可用的变量清单（渲染时逐个替换）。
func EContractVars() []EContractVar {
	return []EContractVar{
		{"contract_number", "合同编号"},
		{"my_unit", "我方单位名"},
		{"social_credit_code", "社会信用代码"},
		{"contact", "联系人"},
		{"contact_phone", "联系电话"},
		{"contact_email", "联系邮箱"},
		{"contact_address", "联系地址"},
		{"client_name", "客户名"},
		{"client_email", "客户邮箱"},
		{"order_id", "订单编号"},
		{"order_amount", "订单金额"},
		{"order_created_at", "下单时间"},
		{"product_names", "商品名称"},
		{"contract_created_at", "合同申请时间"},
	}
}

// EContractSettings 是基础设置（system_settings.e_contract）。
type EContractSettings struct {
	Switch               bool   `json:"switch"`
	DayLimit             int    `json:"day_limit"`
	MyUnit               string `json:"my_unit"`
	SocialCreditCode     string `json:"social_credit_code"`
	Contact              string `json:"contact"`
	ContactPhone         string `json:"contact_phone"`
	ContactEmail         string `json:"contact_email"`
	ContactAddress       string `json:"contact_address"`
	Postcode             string `json:"postcode"`
	ContractNumberPrefix string `json:"contract_number_prefix"`
	ContractNumberNext   int64  `json:"contract_number_next"`
	Logo                 string `json:"logo"`
	CompanyChop          string `json:"company_chop"`
}

// GetEContractSettings 读取基础设置。
func (s *Store) GetEContractSettings(ctx context.Context) (EContractSettings, error) {
	out := EContractSettings{ContractNumberNext: 1}
	err := s.settingGet(ctx, "e_contract", &out)
	if errors.Is(err, ErrNotFound) {
		return EContractSettings{ContractNumberNext: 1}, nil
	}
	if err != nil {
		return EContractSettings{}, err
	}
	if out.ContractNumberNext < 1 {
		out.ContractNumberNext = 1
	}
	return out, nil
}

// SaveEContractSettings 保存基础设置；编号只能向前推进。
func (s *Store) SaveEContractSettings(ctx context.Context, in EContractSettings) error {
	old, err := s.GetEContractSettings(ctx)
	if err != nil {
		return err
	}
	if in.ContractNumberNext < old.ContractNumberNext {
		in.ContractNumberNext = old.ContractNumberNext
	}
	return s.settingSave(ctx, "e_contract", in)
}

// ---- 模板 ----

// EContractTemplate 是一个合同模板。
type EContractTemplate struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Detail       string    `json:"detail"`
	ProductIDs   []string  `json:"product_ids"`
	ProductNames []string  `json:"product_names"`
	BaseContract bool      `json:"base_contract"`
	ForceSign    bool      `json:"force_sign"`
	Notes        string    `json:"notes"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
}

// EContractTemplateInput 是模板入参。
type EContractTemplateInput struct {
	Name         string
	Detail       string
	ProductIDs   []string
	BaseContract bool
	ForceSign    bool
	Notes        string
	Active       bool
}

const eContractTemplateCols = `SELECT t.id,t.public_id::text,t.name,t.detail,t.base_contract,t.force_sign,t.notes,t.active,t.created_at
FROM e_contract_templates t`

// ListEContractTemplates 列出模板（active 过滤可选）。
func (s *Store) ListEContractTemplates(ctx context.Context, activeOnly bool) ([]EContractTemplate, error) {
	where := ""
	if activeOnly {
		where = ` WHERE t.active=true`
	}
	rows, err := s.DB.Query(ctx, eContractTemplateCols+where+` ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EContractTemplate{}
	ids := []int64{}
	for rows.Next() {
		var v EContractTemplate
		var id int64
		if err := rows.Scan(&id, &v.ID, &v.Name, &v.Detail, &v.BaseContract, &v.ForceSign, &v.Notes, &v.Active, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.ProductIDs = []string{}
		v.ProductNames = []string{}
		out = append(out, v)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachEContractTemplateProducts(ctx, out, ids); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) attachEContractTemplateProducts(ctx context.Context, out []EContractTemplate, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.DB.Query(ctx, `SELECT tp.template_id,p.public_id::text,p.name FROM e_contract_template_products tp
JOIN products p ON p.id=tp.product_id WHERE tp.template_id=ANY($1::bigint[]) ORDER BY p.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var templateID int64
		var publicID, name string
		if err := rows.Scan(&templateID, &publicID, &name); err != nil {
			return err
		}
		for i := range out {
			if ids[i] == templateID {
				out[i].ProductIDs = append(out[i].ProductIDs, publicID)
				out[i].ProductNames = append(out[i].ProductNames, name)
			}
		}
	}
	return rows.Err()
}

// GetEContractTemplate 模板详情。
func (s *Store) GetEContractTemplate(ctx context.Context, publicID string) (EContractTemplate, error) {
	var v EContractTemplate
	var id int64
	err := s.DB.QueryRow(ctx, eContractTemplateCols+` WHERE t.public_id=$1`, publicID).
		Scan(&id, &v.ID, &v.Name, &v.Detail, &v.BaseContract, &v.ForceSign, &v.Notes, &v.Active, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return EContractTemplate{}, ErrNotFound
	}
	if err != nil {
		return EContractTemplate{}, err
	}
	if err := s.attachEContractTemplateProducts(ctx, []EContractTemplate{v}, []int64{id}); err != nil {
		return EContractTemplate{}, err
	}
	return v, nil
}

// CreateEContractTemplate 新增模板。
func (s *Store) CreateEContractTemplate(ctx context.Context, in EContractTemplateInput) (EContractTemplate, error) {
	if strings.TrimSpace(in.Name) == "" {
		return EContractTemplate{}, ErrInvalidState
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return EContractTemplate{}, err
	}
	defer tx.Rollback(ctx)
	var publicID string
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO e_contract_templates(name,detail,base_contract,force_sign,notes,active) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,public_id::text`,
		strings.TrimSpace(in.Name), in.Detail, in.BaseContract, in.ForceSign, in.Notes, in.Active).Scan(&id, &publicID)
	if err != nil {
		return EContractTemplate{}, err
	}
	if err := saveEContractTemplateProducts(ctx, tx, id, in.ProductIDs); err != nil {
		return EContractTemplate{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EContractTemplate{}, err
	}
	return s.GetEContractTemplate(ctx, publicID)
}

func saveEContractTemplateProducts(ctx context.Context, tx pgx.Tx, templateID int64, productPublicIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM e_contract_template_products WHERE template_id=$1`, templateID); err != nil {
		return err
	}
	for _, pid := range productPublicIDs {
		id, err := wanyunPublicToID(ctx, tx, "products", pid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO e_contract_template_products(template_id,product_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, templateID, id); err != nil {
			return err
		}
	}
	return nil
}

// UpdateEContractTemplate 修改模板。
func (s *Store) UpdateEContractTemplate(ctx context.Context, publicID string, in EContractTemplateInput) (EContractTemplate, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return EContractTemplate{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM e_contract_templates WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return EContractTemplate{}, ErrNotFound
	}
	if err != nil {
		return EContractTemplate{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE e_contract_templates SET name=$2,detail=$3,base_contract=$4,force_sign=$5,notes=$6,active=$7,updated_at=now() WHERE id=$1`,
		id, strings.TrimSpace(in.Name), in.Detail, in.BaseContract, in.ForceSign, in.Notes, in.Active); err != nil {
		return EContractTemplate{}, err
	}
	if err := saveEContractTemplateProducts(ctx, tx, id, in.ProductIDs); err != nil {
		return EContractTemplate{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EContractTemplate{}, err
	}
	return s.GetEContractTemplate(ctx, publicID)
}

// CopyEContractTemplate 复制模板（名称加「- 副本」）。
func (s *Store) CopyEContractTemplate(ctx context.Context, publicID string) (EContractTemplate, error) {
	t, err := s.GetEContractTemplate(ctx, publicID)
	if err != nil {
		return EContractTemplate{}, err
	}
	return s.CreateEContractTemplate(ctx, EContractTemplateInput{
		Name: t.Name + " - 副本", Detail: t.Detail, ProductIDs: t.ProductIDs,
		BaseContract: t.BaseContract, ForceSign: t.ForceSign, Notes: t.Notes, Active: false,
	})
}

// DeleteEContractTemplate 删除模板（已有合同的 template_id 置空，合同保留快照）。
func (s *Store) DeleteEContractTemplate(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM e_contract_templates WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 合同 ----

// EContract 是一份合同。
type EContract struct {
	ID             string     `json:"id"`
	Number         string     `json:"number"`
	TemplateID     string     `json:"template_id"`
	TemplateName   string     `json:"template_name"`
	OrderID        string     `json:"order_id"`
	UserID         int64      `json:"-"`
	UserUID        int64      `json:"user_uid"`
	UserEmail      string     `json:"user_email"`
	Content        string     `json:"content"`
	Status         string     `json:"status"`
	SignImage      string     `json:"-"`
	HasSign        bool       `json:"has_sign"`
	SignedAt       *time.Time `json:"signed_at"`
	Reason         string     `json:"reason"`
	CourierCompany string     `json:"courier_company"`
	CourierNumber  string     `json:"courier_number"`
	CreatedAt      time.Time  `json:"created_at"`
}

const eContractCols = `SELECT c.id,c.public_id::text,c.number,coalesce(t.public_id::text,''),coalesce(t.name,c.template_name),
coalesce(o.public_id::text,''),coalesce(c.user_id,0),coalesce(u.uid,0),coalesce(u.email,''),c.content,c.status,c.sign_image,c.signed_at,
c.reason,c.courier_company,c.courier_number,c.created_at
FROM e_contracts c
LEFT JOIN e_contract_templates t ON t.id=c.template_id
LEFT JOIN orders o ON o.id=c.order_id
LEFT JOIN users u ON u.id=c.user_id`

func scanEContract(row pgx.Row) (EContract, error) {
	var v EContract
	err := row.Scan(new(int64), &v.ID, &v.Number, &v.TemplateID, &v.TemplateName,
		&v.OrderID, &v.UserID, &v.UserUID, &v.UserEmail, &v.Content, &v.Status, &v.SignImage, &v.SignedAt,
		&v.Reason, &v.CourierCompany, &v.CourierNumber, &v.CreatedAt)
	if err == nil {
		v.HasSign = v.SignImage != ""
	}
	return v, err
}

// ListEContracts 后台分页列出合同。
func (s *Store) ListEContracts(ctx context.Context, keyword, status string, limit, offset int) ([]EContract, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := ` WHERE ($1='' OR c.number ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%' OR coalesce(t.name,'') ILIKE '%'||$1||'%')
  AND ($2='' OR c.status=$2)`
	rows, err := s.DB.Query(ctx, eContractCols+where+` ORDER BY c.id DESC LIMIT $3 OFFSET $4`, keyword, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []EContract{}
	for rows.Next() {
		v, err := scanEContract(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM e_contracts c
LEFT JOIN e_contract_templates t ON t.id=c.template_id
LEFT JOIN orders o ON o.id=c.order_id
LEFT JOIN users u ON u.id=c.user_id`+where, keyword, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GetEContract 合同详情。
func (s *Store) GetEContract(ctx context.Context, publicID string) (EContract, error) {
	v, err := scanEContract(s.DB.QueryRow(ctx, eContractCols+` WHERE c.public_id=$1`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return EContract{}, ErrNotFound
	}
	return v, err
}

var eContractVarRe = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// renderEContractContent 用订单与设置快照替换模板变量。
func renderEContractContent(detail string, vars map[string]string) string {
	return eContractVarRe.ReplaceAllStringFunc(detail, func(m string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(m, "{{"), "}}")
		key = strings.Trim(strings.TrimSpace(key), " ")
		if v, ok := vars[key]; ok {
			return v
		}
		return m
	})
}

// ApplyEContract 用户对已支付订单申请合同（一个订单只能有一份有效合同）。
func (s *Store) ApplyEContract(ctx context.Context, userID int64, orderPublicID string) (EContract, error) {
	settings, err := s.GetEContractSettings(ctx)
	if err != nil {
		return EContract{}, err
	}
	if !settings.Switch {
		return EContract{}, ErrEContractDisabled
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return EContract{}, err
	}
	defer tx.Rollback(ctx)
	var orderID, productID int64
	var totalCents int64
	var createdAt time.Time
	err = tx.QueryRow(ctx, `SELECT o.id,o.total_cents,o.created_at,oi.product_id FROM orders o
LEFT JOIN order_items oi ON oi.order_id=o.id AND oi.product_id IS NOT NULL
WHERE o.public_id=$1 AND o.user_id=$2 AND o.status='paid'`, orderPublicID, userID).Scan(&orderID, &totalCents, &createdAt, &productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return EContract{}, ErrInvalidState
	}
	if err != nil {
		return EContract{}, err
	}
	if settings.DayLimit > 0 && time.Since(createdAt) > time.Duration(settings.DayLimit)*24*time.Hour {
		return EContract{}, ErrEContractTooOld
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM e_contracts WHERE order_id=$1 AND status NOT IN ('reject','cancel'))`, orderID).Scan(&active); err != nil {
		return EContract{}, err
	}
	if active {
		return EContract{}, ErrEContractExists
	}
	// 选模板：按订单商品关联的启用模板，回退基础合同
	var templateID int64
	var templateName, detail string
	var tplErr error
	if productID != 0 {
		tplErr = tx.QueryRow(ctx, `SELECT t.id,t.name,t.detail FROM e_contract_templates t
JOIN e_contract_template_products tp ON tp.template_id=t.id AND tp.product_id=$1
WHERE t.active=true ORDER BY t.id LIMIT 1`, productID).Scan(&templateID, &templateName, &detail)
	}
	if productID == 0 || errors.Is(tplErr, pgx.ErrNoRows) {
		tplErr = tx.QueryRow(ctx, `SELECT id,name,detail FROM e_contract_templates WHERE active=true AND base_contract=true ORDER BY id LIMIT 1`).Scan(&templateID, &templateName, &detail)
		if errors.Is(tplErr, pgx.ErrNoRows) {
			return EContract{}, ErrEContractNoTemplate
		}
	} else if tplErr != nil {
		return EContract{}, tplErr
	}
	var productNames string
	if err := tx.QueryRow(ctx, `SELECT string_agg(p.name,', ') FROM order_items oi JOIN products p ON p.id=oi.product_id WHERE oi.order_id=$1 AND oi.product_id IS NOT NULL`, orderID).Scan(&productNames); err != nil {
		return EContract{}, err
	}
	var email string
	if err := tx.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, userID).Scan(&email); err != nil {
		return EContract{}, err
	}
	vars := map[string]string{
		"my_unit":             settings.MyUnit,
		"social_credit_code":  settings.SocialCreditCode,
		"contact":             settings.Contact,
		"contact_phone":       settings.ContactPhone,
		"contact_email":       settings.ContactEmail,
		"contact_address":     settings.ContactAddress,
		"client_name":         email,
		"client_email":        email,
		"order_id":            orderPublicID,
		"order_amount":        fmt.Sprintf("%.2f", float64(totalCents)/100),
		"order_created_at":    createdAt.Format("2006-01-02 15:04:05"),
		"product_names":       productNames,
		"contract_created_at": time.Now().Format("2006-01-02 15:04:05"),
	}
	// 合同编号：前缀 + 递增号（行锁防并发重号）
	var next int64
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT value FROM system_settings WHERE key='e_contract' FOR UPDATE`).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		next = 1
	case err != nil:
		return EContract{}, err
	default:
		if err := json.Unmarshal(raw, &settings); err != nil {
			return EContract{}, err
		}
		next = settings.ContractNumberNext
		if next < 1 {
			next = 1
		}
	}
	number := fmt.Sprintf("%s%06d", settings.ContractNumberPrefix, next)
	vars["contract_number"] = number
	content := renderEContractContent(detail, vars)
	settings.ContractNumberNext = next + 1
	blob, err := json.Marshal(settings)
	if err != nil {
		return EContract{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO system_settings(key,value,updated_at) VALUES('e_contract',$2,now())
ON CONFLICT (key) DO UPDATE SET value=excluded.value,updated_at=now()`, "e_contract", blob); err != nil {
		return EContract{}, err
	}
	var publicID string
	if err := tx.QueryRow(ctx, `INSERT INTO e_contracts(number,template_id,template_name,order_id,user_id,content,status)
VALUES($1,$2,$3,$4,$5,$6,'pending') RETURNING public_id::text`,
		number, templateID, templateName, orderID, userID, content).Scan(&publicID); err != nil {
		return EContract{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EContract{}, err
	}
	return s.GetEContract(ctx, publicID)
}

// ListUserEContractOrders 用户端列出可申请合同的已支付订单（无有效合同 + 时间窗内）。
type EContractEligibleOrder struct {
	ID          string    `json:"id"`
	TotalCents  int64     `json:"total_cents"`
	CreatedAt   time.Time `json:"created_at"`
	ProductID   string    `json:"product_id"`
	ProductName string    `json:"product_name"`
	Applied     bool      `json:"applied"`
}

func (s *Store) ListUserEContractOrders(ctx context.Context, userID int64) ([]EContractEligibleOrder, error) {
	settings, err := s.GetEContractSettings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT o.public_id::text,o.total_cents,o.created_at,coalesce(p.public_id::text,''),coalesce(p.name,''),
EXISTS(SELECT 1 FROM e_contracts c WHERE c.order_id=o.id AND c.status NOT IN ('reject','cancel'))
FROM orders o
LEFT JOIN order_items oi ON oi.order_id=o.id AND oi.product_id IS NOT NULL
LEFT JOIN products p ON p.id=oi.product_id
WHERE o.user_id=$1 AND o.status='paid' AND ($2=0 OR o.created_at>=now()-($2::text||' days')::interval)
ORDER BY o.id DESC LIMIT 100`, userID, settings.DayLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EContractEligibleOrder{}
	for rows.Next() {
		var v EContractEligibleOrder
		if err := rows.Scan(&v.ID, &v.TotalCents, &v.CreatedAt, &v.ProductID, &v.ProductName, &v.Applied); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SignEContract 用户签订合同（签名图 data URL，≤200KB）。
func (s *Store) SignEContract(ctx context.Context, userID int64, publicID, signImage string) (EContract, error) {
	if !strings.HasPrefix(signImage, "data:image/") || len(signImage) > 200*1024 {
		return EContract{}, ErrInvalidState
	}
	tag, err := s.DB.Exec(ctx, `UPDATE e_contracts SET sign_image=$3,signed_at=now(),status='signed',updated_at=now()
WHERE public_id=$1 AND user_id=$2 AND status='pending'`, publicID, userID, signImage)
	if err != nil {
		return EContract{}, err
	}
	if tag.RowsAffected() == 0 {
		return EContract{}, ErrInvalidState
	}
	return s.GetEContract(ctx, publicID)
}

// GetUserEContracts 用户自己的合同列表。
func (s *Store) GetUserEContracts(ctx context.Context, userID int64) ([]EContract, error) {
	rows, err := s.DB.Query(ctx, eContractCols+` WHERE c.user_id=$1 ORDER BY c.id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EContract{}
	for rows.Next() {
		v, err := scanEContract(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReviewEContract 后台审核：complete（通过）/ reject（驳回）/ cancel（作废）。
func (s *Store) ReviewEContract(ctx context.Context, publicID, action, reason string) (EContract, error) {
	var status string
	switch action {
	case "complete":
		status = "effective"
		reason = ""
	case "reject":
		status = "reject"
		if strings.TrimSpace(reason) == "" {
			return EContract{}, ErrInvalidState
		}
	case "cancel":
		status = "cancel"
		if strings.TrimSpace(reason) == "" {
			return EContract{}, ErrInvalidState
		}
	default:
		return EContract{}, ErrInvalidState
	}
	tag, err := s.DB.Exec(ctx, `UPDATE e_contracts SET status=$2,reason=$3,updated_at=now() WHERE public_id=$1 AND status IN ('pending','signed')`, publicID, status, reason)
	if err != nil {
		return EContract{}, err
	}
	if tag.RowsAffected() == 0 {
		return EContract{}, ErrInvalidState
	}
	return s.GetEContract(ctx, publicID)
}

// MailEContract 邮寄登记（快递公司 + 快递单号，仅已生效合同）。
func (s *Store) MailEContract(ctx context.Context, publicID, company, number string) (EContract, error) {
	if strings.TrimSpace(company) == "" || strings.TrimSpace(number) == "" {
		return EContract{}, ErrInvalidState
	}
	tag, err := s.DB.Exec(ctx, `UPDATE e_contracts SET courier_company=$2,courier_number=$3,updated_at=now() WHERE public_id=$1 AND status='effective'`, publicID, strings.TrimSpace(company), strings.TrimSpace(number))
	if err != nil {
		return EContract{}, err
	}
	if tag.RowsAffected() == 0 {
		return EContract{}, ErrInvalidState
	}
	return s.GetEContract(ctx, publicID)
}

// EContractHTML 渲染一份可打印的合同 HTML（替代插件的 PDF 生成；风格保持正式版式）。
func (v EContract) EContractHTML(chop, logo string) string {
	chopHTML := ""
	if chop != "" {
		chopHTML = `<img src="` + chop + `" alt="印章" style="width:110px" />`
	}
	logoHTML := ""
	if logo != "" {
		logoHTML = `<img src="` + logo + `" alt="logo" style="height:48px" />`
	}
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>合同 ` + v.Number + `</title>
<style>body{font-family:SimSun,serif;max-width:820px;margin:24px auto;padding:0 24px;line-height:1.9;color:#111}
h1{text-align:center;font-size:22px}.meta{color:#555;font-size:13px;border-bottom:1px solid #ddd;padding-bottom:12px;margin-bottom:20px}
.sign{margin-top:48px;display:flex;justify-content:space-between;align-items:flex-end}
@media print{body{margin:0}}</style></head><body>
<div style="text-align:center">` + logoHTML + `</div>
<h1>电子合同</h1>
<div class="meta">合同编号：` + v.Number + ` ｜ 模板：` + v.TemplateName + ` ｜ 签订时间：` + v.CreatedAt.Format("2006-01-02") + `</div>
<div class="content">` + v.Content + `</div>
<div class="sign"><div>客户签字：<br>` + func() string {
		if v.HasSign {
			return `<img src="` + v.SignImage + `" alt="签名" style="height:56px" />`
		}
		return `（未签订）`
	}() + `</div><div>单位盖章：<br>` + chopHTML + `</div></div>
</body></html>`
}
