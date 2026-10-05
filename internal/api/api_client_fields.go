package api

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 客户自定义字段（对齐魔方 CBAP 插件 client_custom_field）。
//
// 后台定义用户详情可输入的信息；用户在个人中心填写、注册时可提交；
// 管理员在用户详情查看（含管理员专用字段）。接口对照插件前端 api/index.js：
//   GET/POST /client_custom_field、PUT /{id}、PUT /{id}/status、DELETE /{id}、
//   PUT /{id}/drag、GET /client/{id}/client_custom_field_value。

// anyBool 兼容插件的 1/0 与布尔两种写法。
func anyBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "1" || s == "true" || s == "on" || s == "yes"
	}
	return false
}

// clientFieldBody 是字段的新增/修改请求体。
type clientFieldBody struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Options      string `json:"options"`
	Description  string `json:"description"`
	Regexpr      string `json:"regexpr"`
	AdminOnly    any    `json:"admin_only"`
	Required     any    `json:"required"`
	BeforeSettle any    `json:"before_settle"`
	ShowRegister any    `json:"show_register"`
}

// normalizeClientFieldOptions 归一化下拉值（英文逗号分隔，去空白与空项）。
func normalizeClientFieldOptions(raw string) string {
	parts := strings.Split(raw, ",")
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}

func (in *clientFieldBody) toInput() (store.ClientCustomFieldInput, error) {
	var out store.ClientCustomFieldInput
	out.Name = strings.TrimSpace(in.Name)
	out.Type = strings.ToLower(strings.TrimSpace(in.Type))
	out.Description = strings.TrimSpace(in.Description)
	out.Regexpr = strings.TrimSpace(in.Regexpr)
	if out.Name == "" || utf8.RuneCountInString(out.Name) > 50 {
		return out, errors.New("字段名称不能为空且不超过 50 字")
	}
	if !store.ValidClientCustomFieldTypes[out.Type] {
		return out, errors.New("字段类型不合法")
	}
	if utf8.RuneCountInString(out.Description) > 200 {
		return out, errors.New("字段描述不超过 200 字")
	}
	if out.Type == "dropdown" || out.Type == "dropdown_text" {
		out.Options = normalizeClientFieldOptions(in.Options)
		if out.Options == "" {
			return out, errors.New("下拉类型必须填写下拉值（英文半角逗号分隔）")
		}
	}
	if out.Regexpr != "" {
		if _, err := regexp.Compile(out.Regexpr); err != nil {
			return out, errors.New("正则表达式不可用：" + err.Error())
		}
	}
	out.AdminOnly = anyBool(in.AdminOnly)
	out.ShowRegister = anyBool(in.ShowRegister)
	out.Required = anyBool(in.Required)
	out.BeforeSettle = anyBool(in.BeforeSettle)
	if out.Type == "tickbox" {
		// 与插件前端一致：勾选框不提供「必填 / 订购前必填」。
		out.Required = false
		out.BeforeSettle = false
	}
	return out, nil
}

// adminListClientCustomFields 列出全部客户自定义字段。
func (a *App) adminListClientCustomFields(c *gin.Context) {
	list, err := a.Store.ListClientCustomFields(c)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELDS_FAILED", "读取自定义字段失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": len(list)})
}

// adminCreateClientCustomField 新增字段。
func (a *App) adminCreateClientCustomField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw clientFieldBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "CLIENT_FIELD_INVALID", err.Error())
		return
	}
	v, err := a.Store.CreateClientCustomField(c, in)
	if err != nil {
		httpx.Fail(c, 400, "CLIENT_FIELD_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "client_field.create", "client_custom_field", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"name": v.Name, "type": v.Type, "required": v.Required, "before_settle": v.BeforeSettle})
	httpx.OK(c, 201, v)
}

// adminUpdateClientCustomField 修改字段（类型不可改，与插件一致）。
func (a *App) adminUpdateClientCustomField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw clientFieldBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "CLIENT_FIELD_INVALID", err.Error())
		return
	}
	err = a.Store.UpdateClientCustomField(c, c.Param("id"), in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELD_UPDATE_FAILED", "保存字段失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "client_field.update", "client_custom_field", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"name": in.Name, "type": in.Type})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetClientCustomFieldStatus 切换字段显示状态。
func (a *App) adminSetClientCustomFieldStatus(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Status any `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	status := anyBool(in.Status)
	err := a.Store.SetClientCustomFieldStatus(c, c.Param("id"), status)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELD_UPDATE_FAILED", "保存字段失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "client_field.status", "client_custom_field", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"status": status})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminMoveClientCustomField 拖动排序：排到 prev_id 之后（0 = 最前）。
func (a *App) adminMoveClientCustomField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		PrevID string `json:"prev_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.MoveClientCustomField(c, c.Param("id"), strings.TrimSpace(in.PrevID))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELD_MOVE_FAILED", "排序失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "client_field.move", "client_custom_field", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"prev_id": in.PrevID})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteClientCustomField 删除字段（含已有数据，页面先提示再确认）。
func (a *App) adminDeleteClientCustomField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteClientCustomField(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELD_DELETE_FAILED", "删除字段失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "client_field.delete", "client_custom_field", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminUserClientFields 返回某用户的全部启用字段及其值（管理员视角）。
func (a *App) adminUserClientFields(c *gin.Context) {
	list, err := a.Store.AdminUserFieldValues(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELDS_FAILED", "读取字段值失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

// myClientFields 个人中心：可自助填写的字段及当前值。
func (a *App) myClientFields(c *gin.Context) {
	pr, _ := getPrincipal(c)
	list, err := a.Store.ClientFieldValues(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELDS_FAILED", "读取自定义字段失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

// saveMyClientFields 个人中心：保存字段值（password 留空 = 不修改）。
func (a *App) saveMyClientFields(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Values map[string]string `json:"values"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	values := in.Values
	if values == nil {
		values = map[string]string{}
	}
	if err := a.Store.SaveClientFieldValues(c, pr.User.ID, values); err != nil {
		httpx.Fail(c, 400, "CLIENT_FIELD_SAVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "client_field.fill", "client_custom_field_value", pr.User.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"fields": len(values)})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// registerClientFields 注册页展示的字段（公开接口，不含任何用户数据）。
func (a *App) registerClientFields(c *gin.Context) {
	list, err := a.Store.RegisterClientFields(c)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_FIELDS_FAILED", "读取自定义字段失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}
