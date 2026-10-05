package api

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 成本支出（对齐魔方 CBAP 插件 cost_pay）。
//
// 后台在订单详情里登记该订单的支出记录，并管理「自定义字段」（文本框/下拉、
// 必填、列表展示、排序）。契约对照插件前端 template/admin/api/client.js：
//   GET/POST    /order/:id/cost_pay
//   GET/PUT/DELETE /cost_pay/:id
//   GET/POST    /cost_pay/self_defined_field
//   PUT/DELETE  /cost_pay/self_defined_field/:id
//   PUT         /cost_pay/self_defined_field/:id/show_list
//   PUT         /cost_pay/self_defined_field/:id/drag
// 本站按后台前缀落在 /admin 下，权限统一用 finance.report。

// parseCostPayTime 解析查询参数时间：支持 Unix 秒与 RFC3339，空串/非法返回 nil。
func parseCostPayTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		t := time.Unix(n, 0).UTC()
		return &t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t
	}
	return nil
}

// bodyCostPayTime 解析请求体的 cost_time：数字按 Unix 秒（插件契约），字符串按 RFC3339。
func bodyCostPayTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case float64:
		if x <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(x), 0).UTC(), true
	case json.Number:
		n, err := x.Int64()
		if err != nil || n <= 0 {
			return time.Time{}, false
		}
		return time.Unix(n, 0).UTC(), true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return time.Time{}, false
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0).UTC(), true
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// costPayBody 是新增/修改支出的请求体；cost 字段兼容插件以「元」提交的写法。
type costPayBody struct {
	Name             string            `json:"name"`
	Owner            string            `json:"owner"`
	CostCents        int64             `json:"cost_cents"`
	Cost             *float64          `json:"cost"`
	CostTime         any               `json:"cost_time"`
	Notes            string            `json:"notes"`
	SelfDefinedField map[string]string `json:"self_defined_field"`
}

func (in *costPayBody) toInput() (store.CostPayInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Owner = strings.TrimSpace(in.Owner)
	in.Notes = strings.TrimSpace(in.Notes)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 {
		return store.CostPayInput{}, errors.New("支出名称不能为空且不超过 100 字")
	}
	if in.Owner == "" || utf8.RuneCountInString(in.Owner) > 100 {
		return store.CostPayInput{}, errors.New("所属主体不能为空且不超过 100 字")
	}
	if utf8.RuneCountInString(in.Notes) > 200 {
		return store.CostPayInput{}, errors.New("备注不超过 200 字")
	}
	cents := in.CostCents
	if cents == 0 && in.Cost != nil {
		cents = int64(math.Round(*in.Cost * 100))
	}
	if cents < 0 {
		return store.CostPayInput{}, errors.New("支出金额不能为负数")
	}
	costTime, ok := bodyCostPayTime(in.CostTime)
	if !ok {
		return store.CostPayInput{}, errors.New("支出日期不合法")
	}
	values := in.SelfDefinedField
	if values == nil {
		values = map[string]string{}
	}
	return store.CostPayInput{Name: in.Name, Owner: in.Owner, CostCents: cents, CostTime: costTime, Notes: in.Notes, Values: values}, nil
}

// adminListOrderCostPays 列出某订单的支出记录（含自定义字段与主体候选）。
func (a *App) adminListOrderCostPays(c *gin.Context) {
	f := store.CostPayFilter{
		Page:            parseIntDefault(c.Query("page"), 1),
		Limit:           parseIntDefault(c.Query("limit"), 10),
		Keywords:        strings.TrimSpace(c.Query("keywords")),
		Owner:           strings.TrimSpace(c.Query("owner")),
		StartCostTime:   parseCostPayTime(c.Query("start_cost_time")),
		EndCostTime:     parseCostPayTime(c.Query("end_cost_time")),
		StartCreateTime: parseCostPayTime(c.Query("start_create_time")),
		EndCreateTime:   parseCostPayTime(c.Query("end_create_time")),
	}
	list, count, owners, err := a.Store.ListOrderCostPays(c, c.Param("id"), f)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FAILED", "读取支出失败")
		return
	}
	fields, err := a.Store.ListCostPayFields(c)
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FIELDS_FAILED", "读取自定义字段失败")
		return
	}
	brief, err := a.Store.GetOrderBrief(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FAILED", "读取订单失败")
		return
	}
	httpx.OK(c, 200, gin.H{"order": brief, "list": list, "count": count, "owner": owners, "self_defined_field": fields})
}

// adminCreateOrderCostPay 在订单下新增一条支出。
func (a *App) adminCreateOrderCostPay(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw costPayBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_INVALID", err.Error())
		return
	}
	v, err := a.Store.CreateOrderCostPay(c, c.Param("id"), pr.User.ID, in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.create", "cost_pay", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"order_id": v.OrderPublicID, "name": v.Name, "owner": v.Owner, "cost_cents": v.CostCents})
	httpx.OK(c, 201, v)
}

// adminGetOrderCostPay 读取一条支出记录。
func (a *App) adminGetOrderCostPay(c *gin.Context) {
	v, err := a.Store.GetOrderCostPay(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "COST_PAY_NOT_FOUND", "支出记录不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FAILED", "读取支出失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminUpdateOrderCostPay 修改一条支出。
func (a *App) adminUpdateOrderCostPay(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw costPayBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_INVALID", err.Error())
		return
	}
	err = a.Store.UpdateOrderCostPay(c, c.Param("id"), in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "COST_PAY_NOT_FOUND", "支出记录不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.update", "cost_pay", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"name": in.Name, "owner": in.Owner, "cost_cents": in.CostCents})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteOrderCostPay 删除一条支出。
func (a *App) adminDeleteOrderCostPay(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteOrderCostPay(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "COST_PAY_NOT_FOUND", "支出记录不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_DELETE_FAILED", "删除支出失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.delete", "cost_pay", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// costPayFieldBody 是自定义字段的新增/修改请求体。
type costPayFieldBody struct {
	FieldName   string `json:"field_name"`
	FieldType   string `json:"field_type"`
	IsRequired  bool   `json:"is_required"`
	FieldOption string `json:"field_option"`
}

// normalizeCostPayOption 归一化下拉值：英文半角逗号分隔，去掉空项与空白。
func normalizeCostPayOption(raw string) string {
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

func (in *costPayFieldBody) toInput() (string, string, bool, string, error) {
	name := strings.TrimSpace(in.FieldName)
	if name == "" || utf8.RuneCountInString(name) > 50 {
		return "", "", false, "", errors.New("字段名称不能为空且不超过 50 字")
	}
	ftype := strings.ToLower(strings.TrimSpace(in.FieldType))
	if ftype == "" {
		ftype = "text"
	}
	if ftype != "text" && ftype != "dropdown" {
		return "", "", false, "", errors.New("字段类型只支持文本框或下拉")
	}
	option := ""
	if ftype == "dropdown" {
		option = normalizeCostPayOption(in.FieldOption)
		if option == "" {
			return "", "", false, "", errors.New("下拉类型必须填写下拉值（英文半角逗号分隔）")
		}
	}
	return name, ftype, in.IsRequired, option, nil
}

// adminListCostPayFields 列出成本支出的自定义字段。
func (a *App) adminListCostPayFields(c *gin.Context) {
	list, err := a.Store.ListCostPayFields(c)
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FIELDS_FAILED", "读取自定义字段失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

// adminCreateCostPayField 新增自定义字段。
func (a *App) adminCreateCostPayField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw costPayFieldBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	name, ftype, required, option, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_FIELD_INVALID", err.Error())
		return
	}
	v, err := a.Store.CreateCostPayField(c, name, ftype, required, option)
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_FIELD_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.field_create", "cost_pay_field", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"field_name": v.FieldName, "field_type": v.FieldType, "is_required": v.IsRequired})
	httpx.OK(c, 201, v)
}

// adminUpdateCostPayField 修改自定义字段。
func (a *App) adminUpdateCostPayField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw costPayFieldBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	name, ftype, required, option, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_FIELD_INVALID", err.Error())
		return
	}
	err = a.Store.UpdateCostPayField(c, c.Param("field_id"), name, ftype, required, option)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "COST_PAY_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_FIELD_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.field_update", "cost_pay_field", c.Param("field_id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"field_name": name, "field_type": ftype, "is_required": required})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetCostPayFieldShowList 切换字段是否在支出列表展示为列。
func (a *App) adminSetCostPayFieldShowList(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ShowList *bool `json:"show_list"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.ShowList == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.SetCostPayFieldShowList(c, c.Param("field_id"), *in.ShowList)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "COST_PAY_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "COST_PAY_FIELD_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.field_show_list", "cost_pay_field", c.Param("field_id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"show_list": *in.ShowList})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminMoveCostPayField 拖动排序：把字段排到 prev_id 之后（prev_id 为空或 0 表示最前）。
func (a *App) adminMoveCostPayField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		PrevID string `json:"prev_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.MoveCostPayField(c, c.Param("field_id"), strings.TrimSpace(in.PrevID))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "COST_PAY_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FIELD_MOVE_FAILED", "排序失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.field_move", "cost_pay_field", c.Param("field_id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"prev_id": in.PrevID})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteCostPayField 删除自定义字段（引用它的字段值一并删除）。
func (a *App) adminDeleteCostPayField(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteCostPayField(c, c.Param("field_id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "COST_PAY_FIELD_NOT_FOUND", "自定义字段不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FIELD_DELETE_FAILED", "删除自定义字段失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "cost_pay.field_delete", "cost_pay_field", c.Param("field_id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminCostPaySummary 今日/本月/今年支出合计（对应插件 AddonCostPay* 看板 widget）。
func (a *App) adminCostPaySummary(c *gin.Context) {
	items, err := a.Store.CostPaySummary(c)
	if err != nil {
		httpx.Fail(c, 500, "COST_PAY_FAILED", "读取支出汇总失败")
		return
	}
	httpx.OK(c, 200, items)
}
