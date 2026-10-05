package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 客户自定义字段（对齐魔方 CBAP 插件 client_custom_field）。
//
// 后台定义字段（名称/类型/下拉值/描述/正则/必填/订购前必填/管理员可见/注册时显示/
// 显示状态/排序），用户在个人中心填写、注册时可一并提交，管理员在用户详情查看。
// password 类型不回显明文：读取时只给 HasValue。

// ClientCustomField 是一个自定义字段定义。
type ClientCustomField struct {
	PublicID     string    `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Options      string    `json:"options"`
	Description  string    `json:"description"`
	Regexpr      string    `json:"regexpr"`
	AdminOnly    bool      `json:"admin_only"`
	Required     bool      `json:"required"`
	BeforeSettle bool      `json:"before_settle"`
	ShowRegister bool      `json:"show_register"`
	Status       bool      `json:"status"`
	SortWeight   int64     `json:"sort_weight"`
	ValueCount   int64     `json:"value_count"`
	CreatedAt    time.Time `json:"created_at"`
}

// ClientCustomFieldValue 是某用户视角下的一个字段及其值。
type ClientCustomFieldValue struct {
	ClientCustomField
	Value    string `json:"value"`
	HasValue bool   `json:"has_value"`
}

// ClientCustomFieldInput 是字段的新增/修改表单。
type ClientCustomFieldInput struct {
	Name         string
	Type         string
	Options      string
	Description  string
	Regexpr      string
	AdminOnly    bool
	Required     bool
	BeforeSettle bool
	ShowRegister bool
}

// ValidClientCustomFieldTypes 是插件前端可见的全部字段类型。
var ValidClientCustomFieldTypes = map[string]bool{
	"text": true, "dropdown": true, "link": true, "password": true,
	"tickbox": true, "textarea": true, "dropdown_text": true,
}

const clientFieldSelect = `SELECT f.public_id::text,f.name,f.type,f.options,f.description,f.regexpr,f.admin_only,f.required,f.before_settle,f.show_register,f.status,f.sort_weight,
(SELECT count(*) FROM client_custom_field_values v WHERE v.field_id=f.id),f.created_at FROM client_custom_fields f`

func scanClientField(row pgx.Row) (ClientCustomField, error) {
	var v ClientCustomField
	err := row.Scan(&v.PublicID, &v.Name, &v.Type, &v.Options, &v.Description, &v.Regexpr, &v.AdminOnly,
		&v.Required, &v.BeforeSettle, &v.ShowRegister, &v.Status, &v.SortWeight, &v.ValueCount, &v.CreatedAt)
	return v, err
}

// ListClientCustomFields 列出全部字段（后台管理用，按排序）。
func (s *Store) ListClientCustomFields(ctx context.Context) ([]ClientCustomField, error) {
	rows, err := s.DB.Query(ctx, clientFieldSelect+` ORDER BY f.sort_weight,f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientCustomField{}
	for rows.Next() {
		v, err := scanClientField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateClientCustomField 新增字段，排到末尾。
func (s *Store) CreateClientCustomField(ctx context.Context, in ClientCustomFieldInput) (ClientCustomField, error) {
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO client_custom_fields(name,type,options,description,regexpr,admin_only,required,before_settle,show_register,sort_weight)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,(SELECT coalesce(max(sort_weight),0)+1 FROM client_custom_fields)) RETURNING public_id::text`,
		in.Name, in.Type, in.Options, in.Description, in.Regexpr, in.AdminOnly, in.Required, in.BeforeSettle, in.ShowRegister).Scan(&publicID)
	if err != nil {
		return ClientCustomField{}, err
	}
	return scanClientField(s.DB.QueryRow(ctx, clientFieldSelect+` WHERE f.public_id=$1`, publicID))
}

// UpdateClientCustomField 修改字段（类型不可改，与插件前端的禁用一致）。
func (s *Store) UpdateClientCustomField(ctx context.Context, publicID string, in ClientCustomFieldInput) error {
	tag, err := s.DB.Exec(ctx, `UPDATE client_custom_fields SET name=$2,options=$3,description=$4,regexpr=$5,admin_only=$6,required=$7,before_settle=$8,show_register=$9,updated_at=now() WHERE public_id=$1`,
		publicID, in.Name, in.Options, in.Description, in.Regexpr, in.AdminOnly, in.Required, in.BeforeSettle, in.ShowRegister)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetClientCustomFieldStatus 切换显示状态。
func (s *Store) SetClientCustomFieldStatus(ctx context.Context, publicID string, status bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE client_custom_fields SET status=$2,updated_at=now() WHERE public_id=$1`, publicID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveClientCustomField 把字段移到 prev 之后（0/空 = 最前），整表重写权重。
func (s *Store) MoveClientCustomField(ctx context.Context, publicID, prevPublicID string) error {
	fields, err := s.ListClientCustomFields(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(fields))
	var moving bool
	for _, f := range fields {
		if f.PublicID == publicID {
			moving = true
			continue
		}
		ids = append(ids, f.PublicID)
	}
	if !moving {
		return ErrNotFound
	}
	pos := 0
	if prevPublicID != "" && prevPublicID != "0" {
		pos = -1
		for i, id := range ids {
			if id == prevPublicID {
				pos = i + 1
				break
			}
		}
		if pos == -1 {
			return ErrNotFound
		}
	}
	ids = append(ids, "")
	copy(ids[pos+1:], ids[pos:])
	ids[pos] = publicID
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE client_custom_fields SET sort_weight=$2,updated_at=now() WHERE public_id=$1`, id, int64(i+1)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteClientCustomField 删除字段（字段值随之删除，与插件的确认删除口径一致）。
func (s *Store) DeleteClientCustomField(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM client_custom_fields WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// clientFieldWithID 是内部使用的「字段 + 数值主键」。
type clientFieldWithID struct {
	ID    int64
	Field ClientCustomField
}

func (s *Store) clientFieldsWithValues(ctx context.Context, userID int64, where string) ([]clientFieldWithID, map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT f.id,f.public_id::text,f.name,f.type,f.options,f.description,f.regexpr,f.admin_only,f.required,f.before_settle,f.show_register,f.status,f.sort_weight,
(SELECT count(*) FROM client_custom_field_values v WHERE v.field_id=f.id),f.created_at,coalesce(v.value,'')
FROM client_custom_fields f LEFT JOIN client_custom_field_values v ON v.field_id=f.id AND v.user_id=$1`+where+` ORDER BY f.sort_weight,f.id`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []clientFieldWithID{}
	values := map[string]string{}
	for rows.Next() {
		var item clientFieldWithID
		var value string
		if err := rows.Scan(&item.ID, &item.Field.PublicID, &item.Field.Name, &item.Field.Type, &item.Field.Options, &item.Field.Description,
			&item.Field.Regexpr, &item.Field.AdminOnly, &item.Field.Required, &item.Field.BeforeSettle, &item.Field.ShowRegister,
			&item.Field.Status, &item.Field.SortWeight, &item.Field.ValueCount, &item.Field.CreatedAt, &value); err != nil {
			return nil, nil, err
		}
		out = append(out, item)
		values[item.Field.PublicID] = value
	}
	return out, values, rows.Err()
}

// clientFieldView 把数值转成对外的视图（password 不回显明文）。
func clientFieldView(item clientFieldWithID, value string) ClientCustomFieldValue {
	v := ClientCustomFieldValue{ClientCustomField: item.Field}
	if item.Field.Type == "password" {
		v.HasValue = value != ""
		return v
	}
	if item.Field.Type == "tickbox" && value == "" {
		value = "0"
	}
	v.Value = value
	v.HasValue = value != ""
	return v
}

// ClientFieldValues 返回用户可以自助填写的字段（显示中、非管理员专用）及其值。
func (s *Store) ClientFieldValues(ctx context.Context, userID int64) ([]ClientCustomFieldValue, error) {
	items, values, err := s.clientFieldsWithValues(ctx, userID, ` WHERE f.status=TRUE AND f.admin_only=FALSE`)
	if err != nil {
		return nil, err
	}
	out := make([]ClientCustomFieldValue, 0, len(items))
	for _, item := range items {
		out = append(out, clientFieldView(item, values[item.Field.PublicID]))
	}
	return out, nil
}

// AdminUserFieldValues 返回某用户的全部启用字段及其值（管理员视角，含管理员专用字段）。
func (s *Store) AdminUserFieldValues(ctx context.Context, userPublicID string) ([]ClientCustomFieldValue, error) {
	var userID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM users WHERE public_id=$1 AND deleted_at IS NULL`, userPublicID).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	items, values, err := s.clientFieldsWithValues(ctx, userID, ` WHERE f.status=TRUE`)
	if err != nil {
		return nil, err
	}
	out := make([]ClientCustomFieldValue, 0, len(items))
	for _, item := range items {
		out = append(out, clientFieldView(item, values[item.Field.PublicID]))
	}
	return out, nil
}

// normalizeClientFieldValue 归一化输入：勾选框转 1/0，其余去首尾空白。
func normalizeClientFieldValue(f ClientCustomField, raw string) string {
	raw = strings.TrimSpace(raw)
	if f.Type == "tickbox" {
		switch strings.ToLower(raw) {
		case "1", "true", "on", "yes":
			return "1"
		default:
			return "0"
		}
	}
	return raw
}

// validateClientFieldValue 校验单个值（必填 / 下拉选项 / 正则 / 长度）。
func validateClientFieldValue(f ClientCustomField, value string) error {
	if len(value) > 5000 {
		return errors.New("字段「" + f.Name + "」内容过长")
	}
	if value == "" {
		if f.Required && f.Type != "tickbox" {
			return errors.New("字段「" + f.Name + "」为必填")
		}
		return nil
	}
	if f.Type == "dropdown" || f.Type == "dropdown_text" {
		ok := false
		for _, opt := range strings.Split(f.Options, ",") {
			if strings.TrimSpace(opt) == value {
				ok = true
				break
			}
		}
		if !ok {
			return errors.New("字段「" + f.Name + "」取值不在下拉选项中")
		}
	}
	if f.Regexpr != "" {
		re, err := regexp.Compile(f.Regexpr)
		if err != nil {
			return errors.New("字段「" + f.Name + "」的验证规则不可用，请联系管理员")
		}
		if !re.MatchString(value) {
			return errors.New("字段「" + f.Name + "」格式不正确")
		}
	}
	return nil
}

// SaveClientFieldValues 保存用户填写的一组字段值。
// password 字段：空输入表示保持原值（不回显就不可能「原样提交」）。
// 未出现在 values 里的字段视为清空（除 password 保持不变外）。
func (s *Store) SaveClientFieldValues(ctx context.Context, userID int64, values map[string]string) error {
	return s.saveClientFieldValues(ctx, userID, values, false)
}

// SaveRegisterFieldValues 保存注册时提交的字段值（只认注册可见的字段）。
func (s *Store) SaveRegisterFieldValues(ctx context.Context, userID int64, values map[string]string) error {
	return s.saveClientFieldValues(ctx, userID, values, true)
}

func (s *Store) saveClientFieldValues(ctx context.Context, userID int64, values map[string]string, registerOnly bool) error {
	where := ` WHERE f.status=TRUE AND f.admin_only=FALSE`
	if registerOnly {
		where += ` AND f.show_register=TRUE`
	}
	items, _, err := s.clientFieldsWithValues(ctx, userID, where)
	if err != nil {
		return err
	}
	keep := map[int64]string{}
	scoped := make([]int64, 0, len(items))
	for _, item := range items {
		scoped = append(scoped, item.ID)
		raw, provided := values[item.Field.PublicID]
		if !provided && item.Field.Type == "password" {
			continue
		}
		value := normalizeClientFieldValue(item.Field, raw)
		if item.Field.Type == "password" && value == "" {
			continue // 留空 = 保持原值
		}
		if err := validateClientFieldValue(item.Field, value); err != nil {
			return err
		}
		if value != "" {
			keep[item.ID] = value
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM client_custom_field_values v
WHERE v.user_id=$1 AND v.field_id=ANY($2::bigint[]) AND ($3::bigint[] IS NULL OR NOT (v.field_id=ANY($3::bigint[])))`, userID, scoped, bigintArray(keep)); err != nil {
		return err
	}
	for fieldID, value := range keep {
		if _, err := tx.Exec(ctx, `INSERT INTO client_custom_field_values(field_id,user_id,value) VALUES($1,$2,$3)
ON CONFLICT (field_id,user_id) DO UPDATE SET value=EXCLUDED.value,updated_at=now()`, fieldID, userID, value); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// bigintArray 把 map 的键转成 pgx 可用的 bigint[] 参数（空 map 返回 nil）。
func bigintArray(m map[int64]string) []int64 {
	if len(m) == 0 {
		return nil
	}
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// RegisterClientFields 返回注册表单应展示的字段（显示中、注册可见、非管理员专用）。
func (s *Store) RegisterClientFields(ctx context.Context) ([]ClientCustomField, error) {
	rows, err := s.DB.Query(ctx, clientFieldSelect+` WHERE f.status=TRUE AND f.show_register=TRUE AND f.admin_only=FALSE ORDER BY f.sort_weight,f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientCustomField{}
	for rows.Next() {
		v, err := scanClientField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MissingBeforeSettleFields 返回「订购前必填」但用户还没填的字段名。
func (s *Store) MissingBeforeSettleFields(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT f.name FROM client_custom_fields f
LEFT JOIN client_custom_field_values v ON v.field_id=f.id AND v.user_id=$1
WHERE f.status=TRUE AND f.admin_only=FALSE AND f.before_settle=TRUE AND f.type<>'tickbox' AND coalesce(v.value,'')=''
ORDER BY f.sort_weight,f.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ClientFieldValueCount 统计某字段已有的数据条数（删除前提示用）。
func (s *Store) ClientFieldValueCount(ctx context.Context, publicID string) (int64, error) {
	var count int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM client_custom_field_values v JOIN client_custom_fields f ON f.id=v.field_id WHERE f.public_id=$1`, publicID).Scan(&count)
	return count, err
}

// ValidateRegisterFieldValues 在创建账号前校验注册表单提交的字段值。
func (s *Store) ValidateRegisterFieldValues(ctx context.Context, values map[string]string) error {
	items, _, err := s.clientFieldsWithValues(ctx, 0, ` WHERE f.status=TRUE AND f.admin_only=FALSE AND f.show_register=TRUE`)
	if err != nil {
		return err
	}
	for _, item := range items {
		value := normalizeClientFieldValue(item.Field, values[item.Field.PublicID])
		if item.Field.Type == "password" && value == "" {
			continue
		}
		if err := validateClientFieldValue(item.Field, value); err != nil {
			return err
		}
	}
	return nil
}
