package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 万云资源管理（对齐魔方 CBAP WanyunResource 插件）。
//
// IDC 机房资源台账：IP 段（父子两级子网 + 地址明细）、节点（类型 + 自定义字段）、
// VLAN（类型 + 途径节点 + 启停）、光纤与纤芯（途径节点 + 纤芯自定义字段）。
// 插件的 DCIM 接口同步依赖加密协议（不可读），本实现为纯手工台账；
// 字段面全部来自插件前端契约（ip_manage / node_manage / vlan_manage /
// fiber_manage / fiber_core_manage 五个页面与配套 js）。

// ---- 自定义字段（节点 / 纤芯共用一套结构，按 scope 区分） ----

// WanyunCustomField 是节点 / 纤芯自定义字段定义。
type WanyunCustomField struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	FieldName   string `json:"field_name"`
	FieldType   string `json:"field_type"`
	FieldOption string `json:"field_option"`
	IsRequired  bool   `json:"is_required"`
	ShowList    bool   `json:"show_list"`
}

// WanyunCustomFieldValue 是字段定义带上某个对象上的值。
type WanyunCustomFieldValue struct {
	ID          string `json:"id"`
	FieldName   string `json:"field_name"`
	FieldType   string `json:"field_type"`
	FieldOption string `json:"field_option"`
	IsRequired  bool   `json:"is_required"`
	ShowList    bool   `json:"show_list"`
	Value       string `json:"value"`
}

// WanyunCustomFieldInput 是字段定义入参。
type WanyunCustomFieldInput struct {
	FieldName   string
	FieldType   string
	FieldOption string
	IsRequired  bool
	ShowList    bool
}

// ListWanyunCustomFields 按范围列出字段定义。
func (s *Store) ListWanyunCustomFields(ctx context.Context, scope string) ([]WanyunCustomField, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,scope,field_name,field_type,field_option,is_required,show_list
FROM wy_custom_fields WHERE scope=$1 ORDER BY sort_weight,id`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WanyunCustomField{}
	for rows.Next() {
		var v WanyunCustomField
		if err := rows.Scan(&v.ID, &v.Scope, &v.FieldName, &v.FieldType, &v.FieldOption, &v.IsRequired, &v.ShowList); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateWanyunCustomField 新增字段定义；排序权重追加到末尾。
func (s *Store) CreateWanyunCustomField(ctx context.Context, scope string, in WanyunCustomFieldInput) (WanyunCustomField, error) {
	if err := validateWanyunFieldInput(in); err != nil {
		return WanyunCustomField{}, err
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO wy_custom_fields(scope,field_name,field_type,field_option,is_required,show_list,sort_weight)
VALUES($1,$2,$3,$4,$5,$6,(SELECT coalesce(max(sort_weight),0)+1 FROM wy_custom_fields WHERE scope=$1))
RETURNING public_id::text`, scope, strings.TrimSpace(in.FieldName), in.FieldType, strings.TrimSpace(in.FieldOption), in.IsRequired, in.ShowList).Scan(&publicID)
	if err != nil {
		return WanyunCustomField{}, err
	}
	return s.getWanyunCustomField(ctx, publicID)
}

// UpdateWanyunCustomField 修改字段定义。
func (s *Store) UpdateWanyunCustomField(ctx context.Context, publicID string, in WanyunCustomFieldInput) (WanyunCustomField, error) {
	if err := validateWanyunFieldInput(in); err != nil {
		return WanyunCustomField{}, err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE wy_custom_fields SET field_name=$2,field_type=$3,field_option=$4,is_required=$5,show_list=$6 WHERE public_id=$1`,
		publicID, strings.TrimSpace(in.FieldName), in.FieldType, strings.TrimSpace(in.FieldOption), in.IsRequired, in.ShowList)
	if err != nil {
		return WanyunCustomField{}, err
	}
	if tag.RowsAffected() == 0 {
		return WanyunCustomField{}, ErrNotFound
	}
	return s.getWanyunCustomField(ctx, publicID)
}

// DeleteWanyunCustomField 删除字段定义（值级联删除）。
func (s *Store) DeleteWanyunCustomField(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM wy_custom_fields WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWanyunCustomFieldShow 切换「信息展示」开关。
func (s *Store) SetWanyunCustomFieldShow(ctx context.Context, publicID string, show bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE wy_custom_fields SET show_list=$2 WHERE public_id=$1`, publicID, show)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DragWanyunCustomField 把字段移到目标字段之后（prevID 为空表示移到最前），整表重排权重。
func (s *Store) DragWanyunCustomField(ctx context.Context, scope, publicID, prevID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var targetID int64
	err = tx.QueryRow(ctx, `SELECT id FROM wy_custom_fields WHERE public_id=$1 AND scope=$2 FOR UPDATE`, publicID, scope).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM wy_custom_fields WHERE scope=$1 ORDER BY sort_weight,id FOR UPDATE`, scope)
	if err != nil {
		return err
	}
	order := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		order = append(order, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var prevID2 int64
	hasPrev := false
	if prevID != "" {
		if err := tx.QueryRow(ctx, `SELECT id FROM wy_custom_fields WHERE public_id=$1 AND scope=$2`, prevID, scope).Scan(&prevID2); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		hasPrev = true
	}
	reordered := reorderWanyunAfter(order, targetID, prevID2, hasPrev)
	for i, id := range reordered {
		if _, err := tx.Exec(ctx, `UPDATE wy_custom_fields SET sort_weight=$2 WHERE id=$1`, id, i+1); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// reorderWanyunAfter 返回把 target 移到 prev 之后（无 prev 则最前）的新顺序。
func reorderWanyunAfter(order []int64, target, prev int64, hasPrev bool) []int64 {
	out := make([]int64, 0, len(order))
	for _, id := range order {
		if id != target {
			out = append(out, id)
		}
	}
	if !hasPrev {
		return append([]int64{target}, out...)
	}
	final := make([]int64, 0, len(out)+1)
	for _, id := range out {
		final = append(final, id)
		if id == prev {
			final = append(final, target)
		}
	}
	return final
}

func validateWanyunFieldInput(in WanyunCustomFieldInput) error {
	switch {
	case strings.TrimSpace(in.FieldName) == "":
		return ErrInvalidState
	case in.FieldType != "text" && in.FieldType != "dropdown":
		return ErrInvalidState
	case in.FieldType == "dropdown" && strings.TrimSpace(in.FieldOption) == "":
		return ErrInvalidState
	}
	return nil
}

func (s *Store) getWanyunCustomField(ctx context.Context, publicID string) (WanyunCustomField, error) {
	var v WanyunCustomField
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,scope,field_name,field_type,field_option,is_required,show_list
FROM wy_custom_fields WHERE public_id=$1`, publicID).Scan(&v.ID, &v.Scope, &v.FieldName, &v.FieldType, &v.FieldOption, &v.IsRequired, &v.ShowList)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunCustomField{}, ErrNotFound
	}
	return v, err
}

// saveWanyunFieldValues 保存一个对象的自定义字段值：校验必填与下拉取值，全量替换。
func (s *Store) saveWanyunFieldValues(ctx context.Context, tx pgx.Tx, scope string, objectID int64, fields map[string]string) error {
	defs, err := s.ListWanyunCustomFields(ctx, scope)
	if err != nil {
		return err
	}
	if len(defs) == 0 && len(fields) == 0 {
		return nil
	}
	byPublic := map[string]WanyunCustomField{}
	for _, d := range defs {
		byPublic[d.ID] = d
	}
	clean := map[string]string{}
	for k, v := range fields {
		d, ok := byPublic[k]
		if !ok {
			return ErrInvalidState
		}
		v = strings.TrimSpace(v)
		if d.FieldType == "dropdown" && v != "" {
			found := false
			for _, opt := range strings.Split(d.FieldOption, ",") {
				if strings.TrimSpace(opt) == v {
					found = true
					break
				}
			}
			if !found {
				return ErrInvalidState
			}
		}
		clean[k] = v
	}
	for _, d := range defs {
		if d.IsRequired && strings.TrimSpace(clean[d.ID]) == "" {
			return ErrInvalidState
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM wy_custom_field_values v USING wy_custom_fields f
WHERE v.field_id=f.id AND f.scope=$1 AND v.object_id=$2`, scope, objectID); err != nil {
		return err
	}
	for k, v := range clean {
		if v == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wy_custom_field_values(field_id,object_id,value)
SELECT id,$2,$3 FROM wy_custom_fields WHERE public_id=$1`, k, objectID, v); err != nil {
			return err
		}
	}
	return nil
}

// wanyunFieldDefsByInternalID 把 scope 的字段定义转成 内部id -> 定义。
func (s *Store) wanyunFieldDefsByInternalID(ctx context.Context, tx interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, scope string) (map[int64]WanyunCustomField, []int64, error) {
	rows, err := tx.Query(ctx, `SELECT id,public_id::text,field_name,field_type,field_option,is_required,show_list
FROM wy_custom_fields WHERE scope=$1 ORDER BY sort_weight,id`, scope)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[int64]WanyunCustomField{}
	ids := []int64{}
	for rows.Next() {
		var v WanyunCustomField
		var id int64
		if err := rows.Scan(&id, &v.ID, &v.FieldName, &v.FieldType, &v.FieldOption, &v.IsRequired, &v.ShowList); err != nil {
			return nil, nil, err
		}
		out[id] = v
		ids = append(ids, id)
	}
	return out, ids, rows.Err()
}

// attachWanyanFieldValues 给一批对象（scope 为 node / fiber_core）挂上自定义字段值。
// 返回 内部对象id -> 字段值列表。
func (s *Store) attachWanyanFieldValues(ctx context.Context, scope string, objectIDs []int64) (map[int64][]WanyunCustomFieldValue, error) {
	out := map[int64][]WanyunCustomFieldValue{}
	if len(objectIDs) == 0 {
		return out, nil
	}
	defs, defIDs, err := s.wanyunFieldDefsByInternalID(ctx, s.DB, scope)
	if err != nil {
		return nil, err
	}
	if len(defIDs) == 0 {
		return out, nil
	}
	vals, err := s.DB.Query(ctx, `SELECT v.object_id,v.field_id,v.value FROM wy_custom_field_values v
WHERE v.field_id=ANY($1::bigint[]) AND v.object_id=ANY($2::bigint[])`, defIDs, objectIDs)
	if err != nil {
		return nil, err
	}
	defer vals.Close()
	valueAt := map[int64]map[int64]string{}
	for vals.Next() {
		var objectID, fieldID int64
		var value string
		if err := vals.Scan(&objectID, &fieldID, &value); err != nil {
			return nil, err
		}
		if valueAt[objectID] == nil {
			valueAt[objectID] = map[int64]string{}
		}
		valueAt[objectID][fieldID] = value
	}
	if err := vals.Err(); err != nil {
		return nil, err
	}
	for _, oid := range objectIDs {
		list := make([]WanyunCustomFieldValue, 0, len(defIDs))
		for _, fid := range defIDs {
			d := defs[fid]
			val := ""
			if m := valueAt[oid]; m != nil {
				val = m[fid]
			}
			list = append(list, WanyunCustomFieldValue{
				ID: d.ID, FieldName: d.FieldName, FieldType: d.FieldType, FieldOption: d.FieldOption,
				IsRequired: d.IsRequired, ShowList: d.ShowList, Value: val,
			})
		}
		out[oid] = list
	}
	return out, nil
}

// wanyunPublicToID 把公开 UUID 换成内部 id；不存在时返回 ErrNotFound。
func wanyunPublicToID(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, table, publicID string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM `+table+` WHERE public_id=$1`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// ---- 节点类型 / VLAN 类型 ----

// WanyunType 是节点 / VLAN 类型（count 为使用中的数量）。
type WanyunType struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// ListWanyunNodeTypes 列出节点类型。
func (s *Store) ListWanyunNodeTypes(ctx context.Context) ([]WanyunType, error) {
	return s.listWanyunTypes(ctx, "wy_node_types", "wy_nodes", "x.type_id=t.id")
}

// ListWanyunVlanTypes 列出 VLAN 类型。
func (s *Store) ListWanyunVlanTypes(ctx context.Context) ([]WanyunType, error) {
	return s.listWanyunTypes(ctx, "wy_vlan_types", "wy_vlans", "x.type_id=t.id")
}

func (s *Store) listWanyunTypes(ctx context.Context, table, countTable, countOn string) ([]WanyunType, error) {
	rows, err := s.DB.Query(ctx, `SELECT t.public_id::text,t.name,count(x.id) FROM `+table+` t LEFT JOIN `+countTable+` x ON `+countOn+` GROUP BY t.id ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WanyunType{}
	for rows.Next() {
		var v WanyunType
		if err := rows.Scan(&v.ID, &v.Name, &v.Count); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateWanyunType 新增类型（table 为 wy_node_types / wy_vlan_types）。
func (s *Store) CreateWanyunType(ctx context.Context, table, name string) (WanyunType, error) {
	if table != "wy_node_types" && table != "wy_vlan_types" {
		return WanyunType{}, ErrInvalidState
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO `+table+`(name) VALUES($1) RETURNING public_id::text`, strings.TrimSpace(name)).Scan(&publicID)
	if err != nil {
		return WanyunType{}, err
	}
	return WanyunType{ID: publicID, Name: strings.TrimSpace(name)}, nil
}

// UpdateWanyunType 修改类型名。
func (s *Store) UpdateWanyunType(ctx context.Context, table, publicID, name string) error {
	if table != "wy_node_types" && table != "wy_vlan_types" {
		return ErrInvalidState
	}
	tag, err := s.DB.Exec(ctx, `UPDATE `+table+` SET name=$2 WHERE public_id=$1`, publicID, strings.TrimSpace(name))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWanyunType 删除类型；仍被引用时拒绝。
func (s *Store) DeleteWanyunType(ctx context.Context, table, publicID string) error {
	pairs := map[string]string{
		"wy_node_types": "wy_nodes",
		"wy_vlan_types": "wy_vlans",
	}
	used, ok := pairs[table]
	if !ok {
		return ErrInvalidState
	}
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM `+table+` WHERE public_id=$1`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var n int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM `+used+` WHERE type_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInvalidState
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM `+table+` WHERE id=$1`, id)
	return err
}

// ---- 节点 ----

// WanyunNode 是一个机房节点。
type WanyunNode struct {
	ID        string                   `json:"id"`
	Name      string                   `json:"name"`
	TypeID    string                   `json:"type_id"`
	TypeName  string                   `json:"type_name"`
	Fields    []WanyunCustomFieldValue `json:"self_defined_field"`
	CreatedAt time.Time                `json:"created_at"`
}

// WanyunNodeInput 是节点入参（Fields：字段公开 ID -> 值）。
type WanyunNodeInput struct {
	Name   string
	TypeID string
	Fields map[string]string
}

const wanyunNodeCols = `SELECT n.id,n.public_id::text,n.name,coalesce(t.public_id::text,''),coalesce(t.name,''),n.created_at
FROM wy_nodes n LEFT JOIN wy_node_types t ON t.id=n.type_id`

// ListWanyunNodes 分页列出节点（含类型与自定义字段值）。
func (s *Store) ListWanyunNodes(ctx context.Context, keyword string, limit, offset int) ([]WanyunNode, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := ` WHERE ($1='' OR n.name ILIKE '%'||$1||'%' OR coalesce(t.name,'') ILIKE '%'||$1||'%')`
	rows, err := s.DB.Query(ctx, wanyunNodeCols+where+` ORDER BY n.id DESC LIMIT $2 OFFSET $3`, keyword, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []WanyunNode{}
	ids := []int64{}
	for rows.Next() {
		var v WanyunNode
		var id int64
		if err := rows.Scan(&id, &v.ID, &v.Name, &v.TypeID, &v.TypeName, &v.CreatedAt); err != nil {
			return nil, 0, err
		}
		v.Fields = []WanyunCustomFieldValue{}
		out = append(out, v)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.attachWanyunValuesToNodes(ctx, out, ids); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM wy_nodes n LEFT JOIN wy_node_types t ON t.id=n.type_id`+where, keyword).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// attachWanyunValuesToNodes 按内部 id 批量挂字段值。
func (s *Store) attachWanyunValuesToNodes(ctx context.Context, out []WanyunNode, ids []int64) error {
	if len(out) == 0 {
		return nil
	}
	byOID, err := s.attachWanyanFieldValues(ctx, "node", ids)
	if err != nil {
		return err
	}
	for i := range out {
		if v, ok := byOID[ids[i]]; ok {
			out[i].Fields = v
		}
	}
	return nil
}

// GetWanyunNode 节点详情。
func (s *Store) GetWanyunNode(ctx context.Context, publicID string) (WanyunNode, error) {
	var v WanyunNode
	var id int64
	err := s.DB.QueryRow(ctx, wanyunNodeCols+` WHERE n.public_id=$1`, publicID).Scan(&id, &v.ID, &v.Name, &v.TypeID, &v.TypeName, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunNode{}, ErrNotFound
	}
	if err != nil {
		return WanyunNode{}, err
	}
	byOID, err := s.attachWanyanFieldValues(ctx, "node", []int64{id})
	if err != nil {
		return WanyunNode{}, err
	}
	v.Fields = byOID[id]
	if v.Fields == nil {
		v.Fields = []WanyunCustomFieldValue{}
	}
	return v, nil
}

// CreateWanyunNode 新增节点。
func (s *Store) CreateWanyunNode(ctx context.Context, in WanyunNodeInput) (WanyunNode, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunNode{}, err
	}
	defer tx.Rollback(ctx)
	var typeID *int64
	if in.TypeID != "" {
		id, err := wanyunPublicToID(ctx, tx, "wy_node_types", in.TypeID)
		if err != nil {
			return WanyunNode{}, err
		}
		typeID = &id
	}
	var publicID string
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO wy_nodes(name,type_id) VALUES($1,$2) RETURNING id,public_id::text`,
		strings.TrimSpace(in.Name), typeID).Scan(&id, &publicID)
	if err != nil {
		return WanyunNode{}, err
	}
	if err := s.saveWanyunFieldValues(ctx, tx, "node", id, in.Fields); err != nil {
		return WanyunNode{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunNode{}, err
	}
	return s.GetWanyunNode(ctx, publicID)
}

// UpdateWanyunNode 修改节点（含字段值整体替换）。
func (s *Store) UpdateWanyunNode(ctx context.Context, publicID string, in WanyunNodeInput) (WanyunNode, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunNode{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM wy_nodes WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunNode{}, ErrNotFound
	}
	if err != nil {
		return WanyunNode{}, err
	}
	var typeID *int64
	if in.TypeID != "" {
		tid, err := wanyunPublicToID(ctx, tx, "wy_node_types", in.TypeID)
		if err != nil {
			return WanyunNode{}, err
		}
		typeID = &tid
	}
	if _, err := tx.Exec(ctx, `UPDATE wy_nodes SET name=$2,type_id=$3,updated_at=now() WHERE id=$1`, id, strings.TrimSpace(in.Name), typeID); err != nil {
		return WanyunNode{}, err
	}
	if err := s.saveWanyunFieldValues(ctx, tx, "node", id, in.Fields); err != nil {
		return WanyunNode{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunNode{}, err
	}
	return s.GetWanyunNode(ctx, publicID)
}

// DeleteWanyunNode 删除节点（VLAN / 光纤 / 纤芯的途径节点关联级联删除）。
func (s *Store) DeleteWanyunNode(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM wy_nodes WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- VLAN ----

// WanyunVlan 是一条 VLAN 台账。
type WanyunVlan struct {
	ID        string    `json:"id"`
	VlanID    int       `json:"vlan_id"`
	Name      string    `json:"name"`
	TypeID    string    `json:"type_id"`
	TypeName  string    `json:"type_name"`
	Assignor  string    `json:"assignor"`
	Username  string    `json:"username"`
	UseUnit   string    `json:"use_unit"`
	NodeIDs   []string  `json:"node_ids"`
	NodeNames []string  `json:"node_names"`
	Active    bool      `json:"active"`
	Notes     string    `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
}

// WanyunVlanInput 是 VLAN 入参。
type WanyunVlanInput struct {
	VlanID   int
	Name     string
	TypeID   string
	Assignor string
	Username string
	UseUnit  string
	NodeIDs  []string
	Notes    string
}

const wanyunVlanCols = `SELECT v.id,v.public_id::text,v.vlan_id,v.name,coalesce(t.public_id::text,''),coalesce(t.name,''),
v.assignor,v.username,v.use_unit,v.active,v.notes,v.created_at
FROM wy_vlans v LEFT JOIN wy_vlan_types t ON t.id=v.type_id`

// ListWanyunVlans 分页列出 VLAN（关键词匹配名称 / 编号 / 分配人 / 使用人 / 使用单位）。
func (s *Store) ListWanyunVlans(ctx context.Context, keyword, status string, limit, offset int) ([]WanyunVlan, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := ` WHERE ($1='' OR v.name ILIKE '%'||$1||'%' OR v.assignor ILIKE '%'||$1||'%' OR v.username ILIKE '%'||$1||'%' OR v.use_unit ILIKE '%'||$1||'%' OR v.vlan_id::text=$1)
  AND ($3='' OR ($3='1' AND v.active=true) OR ($3='0' AND v.active=false))`
	rows, err := s.DB.Query(ctx, wanyunVlanCols+where+` ORDER BY v.id DESC LIMIT $2 OFFSET $4`, keyword, limit, status, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []WanyunVlan{}
	ids := []int64{}
	for rows.Next() {
		var v WanyunVlan
		var id int64
		if err := rows.Scan(&id, &v.ID, &v.VlanID, &v.Name, &v.TypeID, &v.TypeName, &v.Assignor, &v.Username, &v.UseUnit, &v.Active, &v.Notes, &v.CreatedAt); err != nil {
			return nil, 0, err
		}
		v.NodeIDs = []string{}
		v.NodeNames = []string{}
		out = append(out, v)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.attachWanyanVlanNodes(ctx, out, ids); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM wy_vlans v WHERE ($1='' OR v.name ILIKE '%'||$1||'%' OR v.assignor ILIKE '%'||$1||'%' OR v.username ILIKE '%'||$1||'%' OR v.use_unit ILIKE '%'||$1||'%' OR v.vlan_id::text=$1)
  AND ($3='' OR ($3='1' AND v.active=true) OR ($3='0' AND v.active=false))`, keyword, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// attachWanyanVlanNodes 给一批 VLAN 挂途径节点。
func (s *Store) attachWanyanVlanNodes(ctx context.Context, out []WanyunVlan, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.DB.Query(ctx, `SELECT vn.vlan_id,n.public_id::text,n.name FROM wy_vlan_nodes vn JOIN wy_nodes n ON n.id=vn.node_id WHERE vn.vlan_id=ANY($1::bigint[]) ORDER BY n.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var vlanID int64
		var publicID, name string
		if err := rows.Scan(&vlanID, &publicID, &name); err != nil {
			return err
		}
		for i := range out {
			if ids[i] == vlanID {
				out[i].NodeIDs = append(out[i].NodeIDs, publicID)
				out[i].NodeNames = append(out[i].NodeNames, name)
			}
		}
	}
	return rows.Err()
}

// saveWanyunVlanNodes 整体替换途径节点。
func saveWanyunVlanNodes(ctx context.Context, tx pgx.Tx, vlanID int64, nodePublicIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM wy_vlan_nodes WHERE vlan_id=$1`, vlanID); err != nil {
		return err
	}
	for _, pid := range nodePublicIDs {
		nodeID, err := wanyunPublicToID(ctx, tx, "wy_nodes", pid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wy_vlan_nodes(vlan_id,node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, vlanID, nodeID); err != nil {
			return err
		}
	}
	return nil
}

// CreateWanyunVlan 新增 VLAN。
func (s *Store) CreateWanyunVlan(ctx context.Context, in WanyunVlanInput) (WanyunVlan, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunVlan{}, err
	}
	defer tx.Rollback(ctx)
	var typeID *int64
	if in.TypeID != "" {
		tid, err := wanyunPublicToID(ctx, tx, "wy_vlan_types", in.TypeID)
		if err != nil {
			return WanyunVlan{}, err
		}
		typeID = &tid
	}
	var publicID string
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO wy_vlans(vlan_id,name,type_id,assignor,username,use_unit,notes) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,public_id::text`,
		in.VlanID, strings.TrimSpace(in.Name), typeID, strings.TrimSpace(in.Assignor), strings.TrimSpace(in.Username), strings.TrimSpace(in.UseUnit), strings.TrimSpace(in.Notes)).Scan(&id, &publicID)
	if err != nil {
		return WanyunVlan{}, err
	}
	if err := saveWanyunVlanNodes(ctx, tx, id, in.NodeIDs); err != nil {
		return WanyunVlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunVlan{}, err
	}
	return s.GetWanyunVlan(ctx, publicID)
}

// GetWanyunVlan VLAN 详情。
func (s *Store) GetWanyunVlan(ctx context.Context, publicID string) (WanyunVlan, error) {
	var v WanyunVlan
	var id int64
	err := s.DB.QueryRow(ctx, wanyunVlanCols+` WHERE v.public_id=$1`, publicID).
		Scan(&id, &v.ID, &v.VlanID, &v.Name, &v.TypeID, &v.TypeName, &v.Assignor, &v.Username, &v.UseUnit, &v.Active, &v.Notes, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunVlan{}, ErrNotFound
	}
	if err != nil {
		return WanyunVlan{}, err
	}
	if err := s.attachWanyanVlanNodes(ctx, []WanyunVlan{v}, []int64{id}); err != nil {
		return WanyunVlan{}, err
	}
	return v, nil
}

// UpdateWanyunVlan 修改 VLAN。
func (s *Store) UpdateWanyunVlan(ctx context.Context, publicID string, in WanyunVlanInput) (WanyunVlan, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunVlan{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM wy_vlans WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunVlan{}, ErrNotFound
	}
	if err != nil {
		return WanyunVlan{}, err
	}
	var typeID *int64
	if in.TypeID != "" {
		tid, err := wanyunPublicToID(ctx, tx, "wy_vlan_types", in.TypeID)
		if err != nil {
			return WanyunVlan{}, err
		}
		typeID = &tid
	}
	if _, err := tx.Exec(ctx, `UPDATE wy_vlans SET vlan_id=$2,name=$3,type_id=$4,assignor=$5,username=$6,use_unit=$7,notes=$8,updated_at=now() WHERE id=$1`,
		id, in.VlanID, strings.TrimSpace(in.Name), typeID, strings.TrimSpace(in.Assignor), strings.TrimSpace(in.Username), strings.TrimSpace(in.UseUnit), strings.TrimSpace(in.Notes)); err != nil {
		return WanyunVlan{}, err
	}
	if err := saveWanyunVlanNodes(ctx, tx, id, in.NodeIDs); err != nil {
		return WanyunVlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunVlan{}, err
	}
	return s.GetWanyunVlan(ctx, publicID)
}

// SetWanyunVlanStatus 启用 / 停用 VLAN。
func (s *Store) SetWanyunVlanStatus(ctx context.Context, publicID string, active bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE wy_vlans SET active=$2,updated_at=now() WHERE public_id=$1`, publicID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWanyunVlan 删除 VLAN。
func (s *Store) DeleteWanyunVlan(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM wy_vlans WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- IP 段 ----

// WanyunIPSegment 是一段 IP（主段或其下的子网）。
type WanyunIPSegment struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Subnet     string            `json:"subnet"`
	SubnetMask string            `json:"subnet_mask"`
	Gateway    string            `json:"gateway"`
	GroupName  string            `json:"group_name"`
	IPNum      int64             `json:"ip_num"`
	Usable     int64             `json:"usable"`
	Used       int64             `json:"used"`
	Notes      string            `json:"notes"`
	Subnets    []WanyunIPSegment `json:"ips_sub,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

// WanyunIPSegmentInput 是 IP 段入参。
type WanyunIPSegmentInput struct {
	Name       string
	Subnet     string
	SubnetMask string
	Gateway    string
	GroupName  string
	Notes      string
}

const wanyunSegmentCols = `SELECT s.id,s.public_id::text,s.name,s.subnet,s.subnet_mask,s.gateway,s.group_name,s.notes,s.created_at,
(SELECT count(*) FROM wy_ip_addresses a WHERE a.segment_id=s.id),
(SELECT count(*) FROM wy_ip_addresses a WHERE a.segment_id=s.id AND a.used=false),
(SELECT count(*) FROM wy_ip_addresses a WHERE a.segment_id=s.id AND a.used=true)
FROM wy_ip_segments s`

// ListWanyunIPSegments 列出主段（parent_id IS NULL），并带出全部子网。
func (s *Store) ListWanyunIPSegments(ctx context.Context, keyword, group string) ([]WanyunIPSegment, error) {
	keyword = strings.TrimSpace(keyword)
	group = strings.TrimSpace(group)
	where := ` WHERE s.parent_id IS NULL AND ($1='' OR s.subnet ILIKE '%'||$1||'%' OR s.name ILIKE '%'||$1||'%' OR s.notes ILIKE '%'||$1||'%') AND ($2='' OR s.group_name=$2)`
	rows, err := s.DB.Query(ctx, wanyunSegmentCols+where+` ORDER BY s.id DESC`, keyword, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WanyunIPSegment{}
	ids := []int64{}
	for rows.Next() {
		var v WanyunIPSegment
		var id int64
		if err := rows.Scan(&id, &v.ID, &v.Name, &v.Subnet, &v.SubnetMask, &v.Gateway, &v.GroupName, &v.Notes, &v.CreatedAt, &v.IPNum, &v.Usable, &v.Used); err != nil {
			return nil, err
		}
		out = append(out, v)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	subs, err := s.DB.Query(ctx, wanyunSegmentCols+` WHERE s.parent_id=ANY($1::bigint[]) ORDER BY s.id`, ids)
	if err != nil {
		return nil, err
	}
	defer subs.Close()
	pos := map[int64]int{}
	for i := range out {
		pos[ids[i]] = i
		out[i].Subnets = []WanyunIPSegment{}
	}
	for subs.Next() {
		var v WanyunIPSegment
		var id int64
		if err := subs.Scan(&id, &v.ID, &v.Name, &v.Subnet, &v.SubnetMask, &v.Gateway, &v.GroupName, &v.Notes, &v.CreatedAt, &v.IPNum, &v.Usable, &v.Used); err != nil {
			return nil, err
		}
		if i, ok := pos[id]; ok {
			out[i].Subnets = append(out[i].Subnets, v)
		}
	}
	return out, subs.Err()
}

// CreateWanyunIPSegment 新增 IP 段（parentPublicID 非空时创建其下的子网）。
func (s *Store) CreateWanyunIPSegment(ctx context.Context, parentPublicID string, in WanyunIPSegmentInput) (WanyunIPSegment, error) {
	var parentID *int64
	if parentPublicID != "" {
		pid, err := wanyunPublicToID(ctx, s.DB, "wy_ip_segments", parentPublicID)
		if err != nil {
			return WanyunIPSegment{}, err
		}
		parentID = &pid
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO wy_ip_segments(parent_id,name,subnet,subnet_mask,gateway,group_name,notes) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING public_id::text`,
		parentID, strings.TrimSpace(in.Name), strings.TrimSpace(in.Subnet), strings.TrimSpace(in.SubnetMask), strings.TrimSpace(in.Gateway), strings.TrimSpace(in.GroupName), strings.TrimSpace(in.Notes)).Scan(&publicID)
	if err != nil {
		return WanyunIPSegment{}, err
	}
	return s.GetWanyunIPSegment(ctx, publicID)
}

// GetWanyunIPSegment IP 段详情。
func (s *Store) GetWanyunIPSegment(ctx context.Context, publicID string) (WanyunIPSegment, error) {
	var v WanyunIPSegment
	err := s.DB.QueryRow(ctx, wanyunSegmentCols+` WHERE s.public_id=$1`, publicID).
		Scan(new(int64), &v.ID, &v.Name, &v.Subnet, &v.SubnetMask, &v.Gateway, &v.GroupName, &v.Notes, &v.CreatedAt, &v.IPNum, &v.Usable, &v.Used)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunIPSegment{}, ErrNotFound
	}
	return v, err
}

// UpdateWanyunIPSegment 修改 IP 段。
func (s *Store) UpdateWanyunIPSegment(ctx context.Context, publicID string, in WanyunIPSegmentInput) (WanyunIPSegment, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE wy_ip_segments SET name=$2,subnet=$3,subnet_mask=$4,gateway=$5,group_name=$6,notes=$7,updated_at=now() WHERE public_id=$1`,
		publicID, strings.TrimSpace(in.Name), strings.TrimSpace(in.Subnet), strings.TrimSpace(in.SubnetMask), strings.TrimSpace(in.Gateway), strings.TrimSpace(in.GroupName), strings.TrimSpace(in.Notes))
	if err != nil {
		return WanyunIPSegment{}, err
	}
	if tag.RowsAffected() == 0 {
		return WanyunIPSegment{}, ErrNotFound
	}
	return s.GetWanyunIPSegment(ctx, publicID)
}

// DeleteWanyunIPSegment 删除 IP 段（子网与地址级联删除）。
func (s *Store) DeleteWanyunIPSegment(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM wy_ip_segments WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// WanyunIPAddress 是段内一个地址明细。
type WanyunIPAddress struct {
	ID       string     `json:"id"`
	IP       string     `json:"ip"`
	Assignor string     `json:"assignor"`
	Username string     `json:"username"`
	UseUnit  string     `json:"use_unit"`
	Used     bool       `json:"used"`
	UsedAt   *time.Time `json:"used_at"`
	Notes    string     `json:"notes"`
}

// WanyunIPAddressInput 是地址入参。
type WanyunIPAddressInput struct {
	IP       string
	Assignor string
	Username string
	UseUnit  string
	Notes    string
	Used     bool
}

// ListWanyunIPAddresses 列出段内地址明细（子段与主段都支持）。
func (s *Store) ListWanyunIPAddresses(ctx context.Context, segmentPublicID, keyword string) ([]WanyunIPAddress, error) {
	keyword = strings.TrimSpace(keyword)
	rows, err := s.DB.Query(ctx, `SELECT a.public_id::text,a.ip,a.assignor,a.username,a.use_unit,a.used,a.used_at,a.notes
FROM wy_ip_addresses a JOIN wy_ip_segments s ON s.id=a.segment_id
WHERE s.public_id=$1 AND ($2='' OR a.ip ILIKE '%'||$2||'%' OR a.assignor ILIKE '%'||$2||'%' OR a.username ILIKE '%'||$2||'%' OR a.use_unit ILIKE '%'||$2||'%' OR a.notes ILIKE '%'||$2||'%')
ORDER BY a.id DESC`, segmentPublicID, keyword)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WanyunIPAddress{}
	for rows.Next() {
		var v WanyunIPAddress
		if err := rows.Scan(&v.ID, &v.IP, &v.Assignor, &v.Username, &v.UseUnit, &v.Used, &v.UsedAt, &v.Notes); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateWanyunIPAddress 手工登记一个地址（插件的地址来自 DCIM 同步；本实现为手工台账）。
func (s *Store) CreateWanyunIPAddress(ctx context.Context, segmentPublicID string, in WanyunIPAddressInput) (WanyunIPAddress, error) {
	if strings.TrimSpace(in.IP) == "" {
		return WanyunIPAddress{}, ErrInvalidState
	}
	var publicID string
	err := s.DB.QueryRow(ctx, `INSERT INTO wy_ip_addresses(segment_id,ip,assignor,username,use_unit,notes,used,used_at)
SELECT id,$2,$3,$4,$5,$6,$7,CASE WHEN $7 THEN now() ELSE NULL END FROM wy_ip_segments WHERE public_id=$1 RETURNING public_id::text`,
		segmentPublicID, strings.TrimSpace(in.IP), strings.TrimSpace(in.Assignor), strings.TrimSpace(in.Username), strings.TrimSpace(in.UseUnit), strings.TrimSpace(in.Notes), in.Used).Scan(&publicID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunIPAddress{}, ErrNotFound
	}
	if err != nil {
		return WanyunIPAddress{}, err
	}
	return s.GetWanyunIPAddress(ctx, publicID)
}

// GetWanyunIPAddress 地址详情。
func (s *Store) GetWanyunIPAddress(ctx context.Context, publicID string) (WanyunIPAddress, error) {
	var v WanyunIPAddress
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,ip,assignor,username,use_unit,used,used_at,notes FROM wy_ip_addresses WHERE public_id=$1`, publicID).
		Scan(&v.ID, &v.IP, &v.Assignor, &v.Username, &v.UseUnit, &v.Used, &v.UsedAt, &v.Notes)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunIPAddress{}, ErrNotFound
	}
	return v, err
}

// UpdateWanyunIPAddress 修改地址（标记已用时补记分配时间，释放时清空）。
func (s *Store) UpdateWanyunIPAddress(ctx context.Context, publicID string, in WanyunIPAddressInput) (WanyunIPAddress, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE wy_ip_addresses SET ip=$2,assignor=$3,username=$4,use_unit=$5,notes=$6,used=$7,
used_at=CASE WHEN $7 AND NOT used THEN now() WHEN NOT $7 THEN NULL ELSE used_at END WHERE public_id=$1`,
		publicID, strings.TrimSpace(in.IP), strings.TrimSpace(in.Assignor), strings.TrimSpace(in.Username), strings.TrimSpace(in.UseUnit), strings.TrimSpace(in.Notes), in.Used)
	if err != nil {
		return WanyunIPAddress{}, err
	}
	if tag.RowsAffected() == 0 {
		return WanyunIPAddress{}, ErrNotFound
	}
	return s.GetWanyunIPAddress(ctx, publicID)
}

// DeleteWanyunIPAddress 删除地址明细。
func (s *Store) DeleteWanyunIPAddress(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM wy_ip_addresses WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 光纤 / 纤芯 ----

// WanyunFiber 是一条光纤台账。
type WanyunFiber struct {
	ID               string            `json:"id"`
	FiberNum         string            `json:"fiber_num"`
	Owner            string            `json:"owner"`
	CoreNum          int               `json:"core_num"`
	OpenUnit         string            `json:"open_unit"`
	ConstructionUnit string            `json:"construction_unit"`
	Contact          string            `json:"contact"`
	Project          string            `json:"project"`
	PriceCents       int64             `json:"price_cents"`
	NodeIDs          []string          `json:"node_ids"`
	NodeNames        []string          `json:"node_names"`
	Notes            string            `json:"notes"`
	Cores            []WanyunFiberCore `json:"cores,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

// WanyunFiberInput 是光纤入参。
type WanyunFiberInput struct {
	FiberNum         string
	Owner            string
	CoreNum          int
	OpenUnit         string
	ConstructionUnit string
	Contact          string
	Project          string
	PriceCents       int64
	NodeIDs          []string
	Notes            string
}

const wanyunFiberCols = `SELECT f.id,f.public_id::text,f.fiber_num,f.owner,f.core_num,f.open_unit,f.construction_unit,f.contact,f.project,f.price_cents,f.notes,f.created_at
FROM wy_fibers f`

// ListWanyunFibers 列出光纤（含途径节点名称）。
func (s *Store) ListWanyunFibers(ctx context.Context, keyword string, limit, offset int) ([]WanyunFiber, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	keyword = strings.TrimSpace(keyword)
	where := ` WHERE ($1='' OR f.fiber_num ILIKE '%'||$1||'%' OR f.owner ILIKE '%'||$1||'%' OR f.project ILIKE '%'||$1||'%' OR f.notes ILIKE '%'||$1||'%')`
	rows, err := s.DB.Query(ctx, wanyunFiberCols+where+` ORDER BY f.id DESC LIMIT $2 OFFSET $3`, keyword, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []WanyunFiber{}
	ids := []int64{}
	for rows.Next() {
		var v WanyunFiber
		var id int64
		if err := rows.Scan(&id, &v.ID, &v.FiberNum, &v.Owner, &v.CoreNum, &v.OpenUnit, &v.ConstructionUnit, &v.Contact, &v.Project, &v.PriceCents, &v.Notes, &v.CreatedAt); err != nil {
			return nil, 0, err
		}
		v.NodeIDs = []string{}
		v.NodeNames = []string{}
		out = append(out, v)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.attachWanyanFiberNodes(ctx, out, ids); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM wy_fibers f`+where, keyword).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// attachWanyanFiberNodes 给一批光纤挂途径节点。
func (s *Store) attachWanyanFiberNodes(ctx context.Context, out []WanyunFiber, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.DB.Query(ctx, `SELECT fn.fiber_id,n.public_id::text,n.name FROM wy_fiber_nodes fn JOIN wy_nodes n ON n.id=fn.node_id WHERE fn.fiber_id=ANY($1::bigint[]) ORDER BY n.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var fiberID int64
		var publicID, name string
		if err := rows.Scan(&fiberID, &publicID, &name); err != nil {
			return err
		}
		for i := range out {
			if ids[i] == fiberID {
				out[i].NodeIDs = append(out[i].NodeIDs, publicID)
				out[i].NodeNames = append(out[i].NodeNames, name)
			}
		}
	}
	return rows.Err()
}

// saveWanyunFiberCores 按芯数补齐纤芯（只增不减；纤芯可能已有途径节点与字段数据）。
func (s *Store) saveWanyunFiberCores(ctx context.Context, tx pgx.Tx, fiberID int64, coreNum int) error {
	var have int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM wy_fiber_cores WHERE fiber_id=$1`, fiberID).Scan(&have); err != nil {
		return err
	}
	for i := have + 1; i <= coreNum; i++ {
		if _, err := tx.Exec(ctx, `INSERT INTO wy_fiber_cores(fiber_id,num) VALUES($1,$2)`, fiberID, strconv.Itoa(i)); err != nil {
			return err
		}
	}
	return nil
}

// GetWanyunFiber 光纤详情（含纤芯）。
func (s *Store) GetWanyunFiber(ctx context.Context, publicID string) (WanyunFiber, error) {
	var v WanyunFiber
	var id int64
	err := s.DB.QueryRow(ctx, wanyunFiberCols+` WHERE f.public_id=$1`, publicID).
		Scan(&id, &v.ID, &v.FiberNum, &v.Owner, &v.CoreNum, &v.OpenUnit, &v.ConstructionUnit, &v.Contact, &v.Project, &v.PriceCents, &v.Notes, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunFiber{}, ErrNotFound
	}
	if err != nil {
		return WanyunFiber{}, err
	}
	if err := s.attachWanyanFiberNodes(ctx, []WanyunFiber{v}, []int64{id}); err != nil {
		return WanyunFiber{}, err
	}
	cores, err := s.ListWanyunFiberCores(ctx, publicID)
	if err != nil {
		return WanyunFiber{}, err
	}
	v.Cores = cores
	return v, nil
}

// CreateWanyunFiber 新增光纤并按芯数生成纤芯。
func (s *Store) CreateWanyunFiber(ctx context.Context, in WanyunFiberInput) (WanyunFiber, error) {
	if strings.TrimSpace(in.FiberNum) == "" {
		return WanyunFiber{}, ErrInvalidState
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunFiber{}, err
	}
	defer tx.Rollback(ctx)
	var publicID string
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO wy_fibers(fiber_num,owner,core_num,open_unit,construction_unit,contact,project,price_cents,notes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id,public_id::text`,
		strings.TrimSpace(in.FiberNum), strings.TrimSpace(in.Owner), in.CoreNum, strings.TrimSpace(in.OpenUnit), strings.TrimSpace(in.ConstructionUnit), strings.TrimSpace(in.Contact), strings.TrimSpace(in.Project), in.PriceCents, strings.TrimSpace(in.Notes)).Scan(&id, &publicID)
	if err != nil {
		return WanyunFiber{}, err
	}
	for _, pid := range in.NodeIDs {
		nodeID, err := wanyunPublicToID(ctx, tx, "wy_nodes", pid)
		if err != nil {
			return WanyunFiber{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wy_fiber_nodes(fiber_id,node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, nodeID); err != nil {
			return WanyunFiber{}, err
		}
	}
	if in.CoreNum > 0 {
		if err := s.saveWanyunFiberCores(ctx, tx, id, in.CoreNum); err != nil {
			return WanyunFiber{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunFiber{}, err
	}
	return s.GetWanyunFiber(ctx, publicID)
}

// UpdateWanyunFiber 修改光纤（途径节点整体替换；芯数只增不减）。
func (s *Store) UpdateWanyunFiber(ctx context.Context, publicID string, in WanyunFiberInput) (WanyunFiber, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunFiber{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM wy_fibers WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunFiber{}, ErrNotFound
	}
	if err != nil {
		return WanyunFiber{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE wy_fibers SET fiber_num=$2,owner=$3,core_num=$4,open_unit=$5,construction_unit=$6,contact=$7,project=$8,price_cents=$9,notes=$10,updated_at=now() WHERE id=$1`,
		id, strings.TrimSpace(in.FiberNum), strings.TrimSpace(in.Owner), in.CoreNum, strings.TrimSpace(in.OpenUnit), strings.TrimSpace(in.ConstructionUnit), strings.TrimSpace(in.Contact), strings.TrimSpace(in.Project), in.PriceCents, strings.TrimSpace(in.Notes)); err != nil {
		return WanyunFiber{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM wy_fiber_nodes WHERE fiber_id=$1`, id); err != nil {
		return WanyunFiber{}, err
	}
	for _, pid := range in.NodeIDs {
		nodeID, err := wanyunPublicToID(ctx, tx, "wy_nodes", pid)
		if err != nil {
			return WanyunFiber{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wy_fiber_nodes(fiber_id,node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, nodeID); err != nil {
			return WanyunFiber{}, err
		}
	}
	if in.CoreNum > 0 {
		if err := s.saveWanyunFiberCores(ctx, tx, id, in.CoreNum); err != nil {
			return WanyunFiber{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunFiber{}, err
	}
	return s.GetWanyunFiber(ctx, publicID)
}

// DeleteWanyunFiber 删除光纤（纤芯级联删除）。
func (s *Store) DeleteWanyunFiber(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM wy_fibers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// WanyunFiberCore 是一芯。
type WanyunFiberCore struct {
	ID        string                   `json:"id"`
	FiberID   string                   `json:"fiber_id"`
	Num       string                   `json:"num"`
	NodeIDs   []string                 `json:"node_ids"`
	NodeNames []string                 `json:"node_names"`
	Notes     string                   `json:"notes"`
	Fields    []WanyunCustomFieldValue `json:"self_defined_field"`
}

// WanyunFiberCoreInput 是纤芯入参。
type WanyunFiberCoreInput struct {
	Num     string
	NodeIDs []string
	Notes   string
	Fields  map[string]string
}

// ListWanyunFiberCores 列出光纤下的纤芯（含途径节点与字段值）。
func (s *Store) ListWanyunFiberCores(ctx context.Context, fiberPublicID string) ([]WanyunFiberCore, error) {
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.public_id::text,c.num,c.notes
FROM wy_fiber_cores c JOIN wy_fibers f ON f.id=c.fiber_id WHERE f.public_id=$1 ORDER BY c.id`, fiberPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WanyunFiberCore{}
	ids := []int64{}
	for rows.Next() {
		var v WanyunFiberCore
		var id int64
		if err := rows.Scan(&id, &v.ID, &v.Num, &v.Notes); err != nil {
			return nil, err
		}
		v.NodeIDs = []string{}
		v.NodeNames = []string{}
		v.Fields = []WanyunCustomFieldValue{}
		out = append(out, v)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	nodeRows, err := s.DB.Query(ctx, `SELECT cn.core_id,n.public_id::text,n.name FROM wy_fiber_core_nodes cn JOIN wy_nodes n ON n.id=cn.node_id WHERE cn.core_id=ANY($1::bigint[]) ORDER BY n.id`, ids)
	if err != nil {
		return nil, err
	}
	defer nodeRows.Close()
	for nodeRows.Next() {
		var coreID int64
		var publicID, name string
		if err := nodeRows.Scan(&coreID, &publicID, &name); err != nil {
			return nil, err
		}
		for i := range out {
			if ids[i] == coreID {
				out[i].NodeIDs = append(out[i].NodeIDs, publicID)
				out[i].NodeNames = append(out[i].NodeNames, name)
			}
		}
	}
	if err := nodeRows.Err(); err != nil {
		return nil, err
	}
	byOID, err := s.attachWanyanFieldValues(ctx, "fiber_core", ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if v, ok := byOID[ids[i]]; ok {
			out[i].Fields = v
		}
	}
	return out, nil
}

// GetWanyunFiberCore 纤芯详情。
func (s *Store) GetWanyunFiberCore(ctx context.Context, publicID string) (WanyunFiberCore, error) {
	var fiberPublic string
	err := s.DB.QueryRow(ctx, `SELECT f.public_id::text FROM wy_fiber_cores c JOIN wy_fibers f ON f.id=c.fiber_id WHERE c.public_id=$1`, publicID).Scan(&fiberPublic)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunFiberCore{}, ErrNotFound
	}
	if err != nil {
		return WanyunFiberCore{}, err
	}
	cores, err := s.ListWanyunFiberCores(ctx, fiberPublic)
	if err != nil {
		return WanyunFiberCore{}, err
	}
	for _, c := range cores {
		if c.ID == publicID {
			c.FiberID = fiberPublic
			return c, nil
		}
	}
	return WanyunFiberCore{}, ErrNotFound
}

// UpdateWanyunFiberCore 修改纤芯（编号 / 途径节点 / 备注 / 自定义字段值）。
func (s *Store) UpdateWanyunFiberCore(ctx context.Context, publicID string, in WanyunFiberCoreInput) (WanyunFiberCore, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunFiberCore{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM wy_fiber_cores WHERE public_id=$1 FOR UPDATE`, publicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunFiberCore{}, ErrNotFound
	}
	if err != nil {
		return WanyunFiberCore{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE wy_fiber_cores SET num=$2,notes=$3,updated_at=now() WHERE id=$1`, id, strings.TrimSpace(in.Num), strings.TrimSpace(in.Notes)); err != nil {
		return WanyunFiberCore{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM wy_fiber_core_nodes WHERE core_id=$1`, id); err != nil {
		return WanyunFiberCore{}, err
	}
	for _, pid := range in.NodeIDs {
		nodeID, err := wanyunPublicToID(ctx, tx, "wy_nodes", pid)
		if err != nil {
			return WanyunFiberCore{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wy_fiber_core_nodes(core_id,node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, nodeID); err != nil {
			return WanyunFiberCore{}, err
		}
	}
	if err := s.saveWanyunFieldValues(ctx, tx, "fiber_core", id, in.Fields); err != nil {
		return WanyunFiberCore{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunFiberCore{}, err
	}
	return s.GetWanyunFiberCore(ctx, publicID)
}

// DeleteWanyunFiberCore 删除纤芯。
func (s *Store) DeleteWanyunFiberCore(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM wy_fiber_cores WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddWanyunFiberCore 给光纤补一芯（插件由芯数生成；手工台账需要补芯能力）。
func (s *Store) AddWanyunFiberCore(ctx context.Context, fiberPublicID string) (WanyunFiberCore, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return WanyunFiberCore{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var have int
	err = tx.QueryRow(ctx, `SELECT f.id,(SELECT count(*) FROM wy_fiber_cores c WHERE c.fiber_id=f.id) FROM wy_fibers f WHERE f.public_id=$1 FOR UPDATE`, fiberPublicID).Scan(&id, &have)
	if errors.Is(err, pgx.ErrNoRows) {
		return WanyunFiberCore{}, ErrNotFound
	}
	if err != nil {
		return WanyunFiberCore{}, err
	}
	var corePublic string
	if err := tx.QueryRow(ctx, `INSERT INTO wy_fiber_cores(fiber_id,num) VALUES($1,$2) RETURNING public_id::text`, id, strconv.Itoa(have+1)).Scan(&corePublic); err != nil {
		return WanyunFiberCore{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE wy_fibers SET core_num=$2,updated_at=now() WHERE id=$1`, id, have+1); err != nil {
		return WanyunFiberCore{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WanyunFiberCore{}, err
	}
	return s.GetWanyunFiberCore(ctx, corePublic)
}
