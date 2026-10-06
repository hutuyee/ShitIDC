package api

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 万云资源管理（对齐魔方 CBAP WanyunResource 插件）。
//
// 五个管理面与插件一致：IP 段 / 节点（类型 + 自定义字段）/ VLAN（类型 + 启停）/
// 光纤 / 纤芯（自定义字段）。插件的 DCIM 接口与同步（加密协议不可读）不落地，
// 数据全部手工维护；统一权限 wanyun_resource.manage。

// ---- 自定义字段 ----

type wanyunFieldBody struct {
	FieldName   string `json:"field_name"`
	FieldType   string `json:"field_type"`
	FieldOption string `json:"field_option"`
	IsRequired  bool   `json:"is_required"`
	ShowList    bool   `json:"show_list"`
}

func (b wanyunFieldBody) toInput() store.WanyunCustomFieldInput {
	return store.WanyunCustomFieldInput{
		FieldName: b.FieldName, FieldType: b.FieldType, FieldOption: b.FieldOption,
		IsRequired: b.IsRequired, ShowList: b.ShowList,
	}
}

func (a *App) adminListWanyunFields(c *gin.Context) {
	scope := c.Param("scope")
	if scope != "node" && scope != "fiber_core" {
		httpx.Fail(c, 400, "WANYUN_SCOPE_INVALID", "未知的自定义字段范围")
		return
	}
	fields, err := a.Store.ListWanyunCustomFields(c, scope)
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_FIELDS_FAILED", "读取自定义字段失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": fields})
}

func (a *App) adminCreateWanyunField(c *gin.Context) {
	a.wanyunFieldWrite(c, func() (any, error) {
		return a.Store.CreateWanyunCustomField(c, c.Param("scope"), wanyunFieldFromBody(c).toInput())
	}, "create")
}

func (a *App) adminUpdateWanyunField(c *gin.Context) {
	a.wanyunFieldWrite(c, func() (any, error) {
		return a.Store.UpdateWanyunCustomField(c, c.Param("id"), wanyunFieldFromBody(c).toInput())
	}, "update")
}

func wanyunFieldFromBody(c *gin.Context) wanyunFieldBody {
	var in wanyunFieldBody
	_ = c.ShouldBindJSON(&in)
	return in
}

func (a *App) wanyunFieldWrite(c *gin.Context, fn func() (any, error), action string) {
	in := wanyunFieldFromBody(c)
	if strings.TrimSpace(in.FieldName) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写字段名称")
		return
	}
	if in.FieldType != "text" && in.FieldType != "dropdown" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "字段类型只支持文本框 / 下拉选择")
		return
	}
	if in.FieldType == "dropdown" && strings.TrimSpace(in.FieldOption) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "下拉字段请填写下拉值（英文半角逗号分隔）")
		return
	}
	out, err := fn()
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "WANYUN_FIELD_NOT_FOUND", "字段不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "WANYUN_FIELD_INVALID", "字段定义不合法")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_FIELDS_FAILED", "保存自定义字段失败")
		return
	}
	a.wanyunAudit(c, "field."+action, gin.H{"field_name": in.FieldName})
	httpx.OK(c, 201, out)
}

func (a *App) adminDeleteWanyunField(c *gin.Context) {
	if err := a.Store.DeleteWanyunCustomField(c, c.Param("id")); err != nil {
		failWanyun(c, err, "删除自定义字段失败")
		return
	}
	a.wanyunAudit(c, "field.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminShowWanyunField(c *gin.Context) {
	var in struct {
		Show *bool `json:"show"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Show == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SetWanyunCustomFieldShow(c, c.Param("id"), *in.Show); err != nil {
		failWanyun(c, err, "切换展示开关失败")
		return
	}
	a.wanyunAudit(c, "field.show", gin.H{"show": *in.Show})
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminDragWanyunField(c *gin.Context) {
	var in struct {
		PrevID string `json:"prev_id"`
	}
	_ = c.ShouldBindJSON(&in)
	scope := c.Param("scope")
	if scope != "node" && scope != "fiber_core" {
		httpx.Fail(c, 400, "WANYUN_SCOPE_INVALID", "未知的自定义字段范围")
		return
	}
	if err := a.Store.DragWanyunCustomField(c, scope, c.Param("id"), in.PrevID); err != nil {
		failWanyun(c, err, "排序失败")
		return
	}
	a.wanyunAudit(c, "field.drag", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 类型（节点 / VLAN） ----

func (a *App) adminListWanyunNodeTypes(c *gin.Context) {
	a.wanyunTypeList(c, a.Store.ListWanyunNodeTypes)
}

func (a *App) adminListWanyunVlanTypes(c *gin.Context) {
	a.wanyunTypeList(c, a.Store.ListWanyunVlanTypes)
}

func (a *App) wanyunTypeList(c *gin.Context, fn func(context.Context) ([]store.WanyunType, error)) {
	list, err := fn(c.Request.Context())
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_TYPES_FAILED", "读取类型失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func wanyunTypeTable(scope string) string {
	if scope == "vlan" {
		return "wy_vlan_types"
	}
	return "wy_node_types"
}

func (a *App) adminCreateWanyunType(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请输入类型名称")
		return
	}
	t, err := a.Store.CreateWanyunType(c, wanyunTypeTable(c.Param("scope")), in.Name)
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_TYPES_FAILED", "新增类型失败")
		return
	}
	a.wanyunAudit(c, "type.create", gin.H{"name": in.Name})
	httpx.OK(c, 201, t)
}

func (a *App) adminUpdateWanyunType(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请输入类型名称")
		return
	}
	if err := a.Store.UpdateWanyunType(c, wanyunTypeTable(c.Param("scope")), c.Param("id"), in.Name); err != nil {
		failWanyun(c, err, "修改类型失败")
		return
	}
	a.wanyunAudit(c, "type.update", gin.H{"name": in.Name})
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminDeleteWanyunType(c *gin.Context) {
	err := a.Store.DeleteWanyunType(c, wanyunTypeTable(c.Param("scope")), c.Param("id"))
	if errors.Is(err, store.ErrInvalidState) {
		httpx.Fail(c, 409, "WANYUN_TYPE_IN_USE", "类型使用中，请先调整引用它的数据")
		return
	}
	if err != nil {
		failWanyun(c, err, "删除类型失败")
		return
	}
	a.wanyunAudit(c, "type.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 节点 ----

type wanyunNodeBody struct {
	Name   string            `json:"name"`
	TypeID string            `json:"type_id"`
	Fields map[string]string `json:"self_defined_field"`
}

func (a *App) adminListWanyunNodes(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 50)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	nodes, total, err := a.Store.ListWanyunNodes(c, c.Query("keyword"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_NODES_FAILED", "读取节点失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": nodes, "count": total, "page": page})
}

func (a *App) adminGetWanyunNode(c *gin.Context) {
	node, err := a.Store.GetWanyunNode(c, c.Param("id"))
	if err != nil {
		failWanyun(c, err, "读取节点失败")
		return
	}
	httpx.OK(c, 200, node)
}

func (a *App) adminCreateWanyunNode(c *gin.Context) {
	var in wanyunNodeBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写节点名称")
		return
	}
	node, err := a.Store.CreateWanyunNode(c, store.WanyunNodeInput{Name: in.Name, TypeID: in.TypeID, Fields: in.Fields})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 400, "WANYUN_NODE_TYPE_INVALID", "节点类型不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "WANYUN_NODE_FIELD_INVALID", "自定义字段校验未通过（必填 / 下拉取值）")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_NODES_FAILED", "新增节点失败")
		return
	}
	a.wanyunAudit(c, "node.create", gin.H{"name": in.Name})
	httpx.OK(c, 201, node)
}

func (a *App) adminUpdateWanyunNode(c *gin.Context) {
	var in wanyunNodeBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写节点名称")
		return
	}
	node, err := a.Store.UpdateWanyunNode(c, c.Param("id"), store.WanyunNodeInput{Name: in.Name, TypeID: in.TypeID, Fields: in.Fields})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "WANYUN_NODE_NOT_FOUND", "节点或类型不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "WANYUN_NODE_FIELD_INVALID", "自定义字段校验未通过（必填 / 下拉取值）")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_NODES_FAILED", "修改节点失败")
		return
	}
	a.wanyunAudit(c, "node.update", gin.H{"name": in.Name})
	httpx.OK(c, 200, node)
}

func (a *App) adminDeleteWanyunNode(c *gin.Context) {
	if err := a.Store.DeleteWanyunNode(c, c.Param("id")); err != nil {
		failWanyun(c, err, "删除节点失败")
		return
	}
	a.wanyunAudit(c, "node.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- VLAN ----

type wanyunVlanBody struct {
	VlanID   int      `json:"vlan_id"`
	Name     string   `json:"name"`
	TypeID   string   `json:"type_id"`
	Assignor string   `json:"assignor"`
	Username string   `json:"username"`
	UseUnit  string   `json:"use_unit"`
	NodeIDs  []string `json:"node_ids"`
	Notes    string   `json:"notes"`
}

func (a *App) adminListWanyunVlans(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 50)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	vlans, total, err := a.Store.ListWanyunVlans(c, c.Query("keyword"), c.Query("status"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_VLANS_FAILED", "读取 VLAN 失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": vlans, "count": total, "page": page})
}

func (a *App) adminCreateWanyunVlan(c *gin.Context) {
	var in wanyunVlanBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" || in.VlanID <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写 VLAN 编号与名称")
		return
	}
	v, err := a.Store.CreateWanyunVlan(c, wanyunVlanInput(in))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 400, "WANYUN_VLAN_TYPE_INVALID", "VLAN 类型或途径节点不存在")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_VLANS_FAILED", "新增 VLAN 失败")
		return
	}
	a.wanyunAudit(c, "vlan.create", gin.H{"name": in.Name, "vlan_id": in.VlanID})
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateWanyunVlan(c *gin.Context) {
	var in wanyunVlanBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" || in.VlanID <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写 VLAN 编号与名称")
		return
	}
	v, err := a.Store.UpdateWanyunVlan(c, c.Param("id"), wanyunVlanInput(in))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "WANYUN_VLAN_NOT_FOUND", "VLAN 或类型 / 节点不存在")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_VLANS_FAILED", "修改 VLAN 失败")
		return
	}
	a.wanyunAudit(c, "vlan.update", gin.H{"name": in.Name, "vlan_id": in.VlanID})
	httpx.OK(c, 200, v)
}

func wanyunVlanInput(in wanyunVlanBody) store.WanyunVlanInput {
	return store.WanyunVlanInput{
		VlanID: in.VlanID, Name: in.Name, TypeID: in.TypeID, Assignor: in.Assignor,
		Username: in.Username, UseUnit: in.UseUnit, NodeIDs: in.NodeIDs, Notes: in.Notes,
	}
}

func (a *App) adminSetWanyunVlanStatus(c *gin.Context) {
	var in struct {
		Active *bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Active == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SetWanyunVlanStatus(c, c.Param("id"), *in.Active); err != nil {
		failWanyun(c, err, "切换 VLAN 状态失败")
		return
	}
	a.wanyunAudit(c, "vlan.status", gin.H{"active": *in.Active})
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminDeleteWanyunVlan(c *gin.Context) {
	if err := a.Store.DeleteWanyunVlan(c, c.Param("id")); err != nil {
		failWanyun(c, err, "删除 VLAN 失败")
		return
	}
	a.wanyunAudit(c, "vlan.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- IP 段 ----

type wanyunSegmentBody struct {
	Name       string `json:"name"`
	Subnet     string `json:"subnet"`
	SubnetMask string `json:"subnet_mask"`
	Gateway    string `json:"gateway"`
	GroupName  string `json:"group_name"`
	Notes      string `json:"notes"`
}

func wanyunSegmentInput(in wanyunSegmentBody) store.WanyunIPSegmentInput {
	return store.WanyunIPSegmentInput{
		Name: in.Name, Subnet: in.Subnet, SubnetMask: in.SubnetMask,
		Gateway: in.Gateway, GroupName: in.GroupName, Notes: in.Notes,
	}
}

func (a *App) adminListWanyunIPSegments(c *gin.Context) {
	segments, err := a.Store.ListWanyunIPSegments(c, c.Query("keyword"), c.Query("group"))
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_IPS_FAILED", "读取 IP 段失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": segments})
}

func (a *App) adminCreateWanyunIPSegment(c *gin.Context) {
	var in wanyunSegmentBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Subnet) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写 IP 段 / 子网")
		return
	}
	seg, err := a.Store.CreateWanyunIPSegment(c, c.Query("parent"), wanyunSegmentInput(in))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 400, "WANYUN_SEGMENT_INVALID", "父级 IP 段不存在")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_IPS_FAILED", "新增 IP 段失败")
		return
	}
	a.wanyunAudit(c, "ip_segment.create", gin.H{"subnet": in.Subnet})
	httpx.OK(c, 201, seg)
}

func (a *App) adminUpdateWanyunIPSegment(c *gin.Context) {
	var in wanyunSegmentBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Subnet) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写 IP 段 / 子网")
		return
	}
	seg, err := a.Store.UpdateWanyunIPSegment(c, c.Param("id"), wanyunSegmentInput(in))
	if err != nil {
		failWanyun(c, err, "修改 IP 段失败")
		return
	}
	a.wanyunAudit(c, "ip_segment.update", gin.H{"subnet": in.Subnet})
	httpx.OK(c, 200, seg)
}

func (a *App) adminDeleteWanyunIPSegment(c *gin.Context) {
	if err := a.Store.DeleteWanyunIPSegment(c, c.Param("id")); err != nil {
		failWanyun(c, err, "删除 IP 段失败")
		return
	}
	a.wanyunAudit(c, "ip_segment.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- IP 地址明细 ----

type wanyunAddressBody struct {
	IP       string `json:"ip"`
	Assignor string `json:"assignor"`
	Username string `json:"username"`
	UseUnit  string `json:"use_unit"`
	Notes    string `json:"notes"`
	Used     bool   `json:"used"`
}

func wanyunAddressInput(in wanyunAddressBody) store.WanyunIPAddressInput {
	return store.WanyunIPAddressInput{IP: in.IP, Assignor: in.Assignor, Username: in.Username, UseUnit: in.UseUnit, Notes: in.Notes, Used: in.Used}
}

func (a *App) adminListWanyunIPAddresses(c *gin.Context) {
	list, err := a.Store.ListWanyunIPAddresses(c, c.Param("id"), c.Query("keyword"))
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_IPS_FAILED", "读取地址明细失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) adminCreateWanyunIPAddress(c *gin.Context) {
	var in wanyunAddressBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.IP) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写 IP")
		return
	}
	addr, err := a.Store.CreateWanyunIPAddress(c, c.Param("id"), wanyunAddressInput(in))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "WANYUN_SEGMENT_NOT_FOUND", "IP 段不存在")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_IPS_FAILED", "登记地址失败")
		return
	}
	a.wanyunAudit(c, "ip_address.create", gin.H{"ip": in.IP})
	httpx.OK(c, 201, addr)
}

func (a *App) adminUpdateWanyunIPAddress(c *gin.Context) {
	var in wanyunAddressBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.IP) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写 IP")
		return
	}
	addr, err := a.Store.UpdateWanyunIPAddress(c, c.Param("id"), wanyunAddressInput(in))
	if err != nil {
		failWanyun(c, err, "修改地址失败")
		return
	}
	a.wanyunAudit(c, "ip_address.update", gin.H{"ip": in.IP, "used": in.Used})
	httpx.OK(c, 200, addr)
}

func (a *App) adminDeleteWanyunIPAddress(c *gin.Context) {
	if err := a.Store.DeleteWanyunIPAddress(c, c.Param("id")); err != nil {
		failWanyun(c, err, "删除地址失败")
		return
	}
	a.wanyunAudit(c, "ip_address.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 光纤 / 纤芯 ----

type wanyunFiberBody struct {
	FiberNum         string   `json:"fiber_num"`
	Owner            string   `json:"owner"`
	CoreNum          int      `json:"core_num"`
	OpenUnit         string   `json:"open_unit"`
	ConstructionUnit string   `json:"construction_unit"`
	Contact          string   `json:"contact"`
	Project          string   `json:"project"`
	Price            float64  `json:"price"`
	NodeIDs          []string `json:"node_ids"`
	Notes            string   `json:"notes"`
}

func (a *App) adminListWanyunFibers(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 50)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	fibers, total, err := a.Store.ListWanyunFibers(c, c.Query("keyword"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "WANYUN_FIBERS_FAILED", "读取光纤失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": fibers, "count": total, "page": page})
}

func (a *App) adminGetWanyunFiber(c *gin.Context) {
	f, err := a.Store.GetWanyunFiber(c, c.Param("id"))
	if err != nil {
		failWanyun(c, err, "读取光纤失败")
		return
	}
	httpx.OK(c, 200, f)
}

func (a *App) adminCreateWanyunFiber(c *gin.Context) {
	var in wanyunFiberBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.FiberNum) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写光纤编号")
		return
	}
	if in.Price < 0 || in.CoreNum < 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "价格与芯数不能为负数")
		return
	}
	f, err := a.Store.CreateWanyunFiber(c, store.WanyunFiberInput{
		FiberNum: in.FiberNum, Owner: in.Owner, CoreNum: in.CoreNum, OpenUnit: in.OpenUnit,
		ConstructionUnit: in.ConstructionUnit, Contact: in.Contact, Project: in.Project,
		PriceCents: int64(in.Price * 100), NodeIDs: in.NodeIDs, Notes: in.Notes,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 400, "WANYUN_NODE_INVALID", "途径节点不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "INVALID_REQUEST", "光纤编号必填")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_FIBERS_FAILED", "新增光纤失败")
		return
	}
	a.wanyunAudit(c, "fiber.create", gin.H{"fiber_num": in.FiberNum})
	httpx.OK(c, 201, f)
}

func (a *App) adminUpdateWanyunFiber(c *gin.Context) {
	var in wanyunFiberBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.FiberNum) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写光纤编号")
		return
	}
	if in.Price < 0 || in.CoreNum < 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "价格与芯数不能为负数")
		return
	}
	f, err := a.Store.UpdateWanyunFiber(c, c.Param("id"), store.WanyunFiberInput{
		FiberNum: in.FiberNum, Owner: in.Owner, CoreNum: in.CoreNum, OpenUnit: in.OpenUnit,
		ConstructionUnit: in.ConstructionUnit, Contact: in.Contact, Project: in.Project,
		PriceCents: int64(in.Price * 100), NodeIDs: in.NodeIDs, Notes: in.Notes,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "WANYUN_FIBER_NOT_FOUND", "光纤或途径节点不存在")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_FIBERS_FAILED", "修改光纤失败")
		return
	}
	a.wanyunAudit(c, "fiber.update", gin.H{"fiber_num": in.FiberNum})
	httpx.OK(c, 200, f)
}

func (a *App) adminDeleteWanyunFiber(c *gin.Context) {
	if err := a.Store.DeleteWanyunFiber(c, c.Param("id")); err != nil {
		failWanyun(c, err, "删除光纤失败")
		return
	}
	a.wanyunAudit(c, "fiber.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminAddWanyunFiberCore(c *gin.Context) {
	core, err := a.Store.AddWanyunFiberCore(c, c.Param("id"))
	if err != nil {
		failWanyun(c, err, "补芯失败")
		return
	}
	a.wanyunAudit(c, "fiber_core.add", nil)
	httpx.OK(c, 201, core)
}

type wanyunCoreBody struct {
	Num     string            `json:"num"`
	NodeIDs []string          `json:"node_ids"`
	Notes   string            `json:"notes"`
	Fields  map[string]string `json:"self_defined_field"`
}

func (a *App) adminUpdateWanyunFiberCore(c *gin.Context) {
	var in wanyunCoreBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	core, err := a.Store.UpdateWanyunFiberCore(c, c.Param("id"), store.WanyunFiberCoreInput{Num: in.Num, NodeIDs: in.NodeIDs, Notes: in.Notes, Fields: in.Fields})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "WANYUN_CORE_NOT_FOUND", "纤芯或途径节点不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "WANYUN_CORE_FIELD_INVALID", "自定义字段校验未通过（必填 / 下拉取值）")
		return
	case err != nil:
		httpx.Fail(c, 500, "WANYUN_FIBERS_FAILED", "修改纤芯失败")
		return
	}
	a.wanyunAudit(c, "fiber_core.update", gin.H{"num": in.Num})
	httpx.OK(c, 200, core)
}

func (a *App) adminDeleteWanyunFiberCore(c *gin.Context) {
	if err := a.Store.DeleteWanyunFiberCore(c, c.Param("id")); err != nil {
		failWanyun(c, err, "删除纤芯失败")
		return
	}
	a.wanyunAudit(c, "fiber_core.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// wanyunAudit 统一审计写入。
func (a *App) wanyunAudit(c *gin.Context, action string, detail map[string]any) {
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "wanyun."+action, "wanyun_resource", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, detail)
	}
}

// failWanyun 把 store 层错误统一映射成 HTTP 响应。
func failWanyun(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "WANYUN_NOT_FOUND", "数据不存在")
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "WANYUN_INVALID_STATE", "数据状态不允许该操作")
	default:
		httpx.Fail(c, 500, "WANYUN_FAILED", message)
	}
}
