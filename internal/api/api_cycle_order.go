package api

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 周期人工订单（对齐魔方 CBAP 插件 CycleArtificialOrder）。
//
// 后台接口字段沿用插件前端契约（template/admin/api/cycle_order.js）：
//   GET    /admin/cycle-artificial-orders              列表（keywords/page/limit）
//   POST   /admin/cycle-artificial-orders              新增生成规则
//   GET    /admin/cycle-artificial-orders/:id          详情：生成规则 + 子订单（分页/筛选）
//   PUT    /admin/cycle-artificial-orders/:id          修改（描述/金额/时间范围/生成周期）
//   DELETE /admin/cycle-artificial-orders/:id          删除生成规则（子订单保留）
//   POST   /admin/cycle-artificial-orders/batch-delete 批量删除未支付子订单
//   PUT    /admin/artificial-orders/:id/price          调整子订单价格（金额 + 描述）
//   POST   /admin/artificial-orders/:id/mark-paid      标记支付（可勾选「优先扣除余额」）
//   DELETE /admin/artificial-orders/:id                删除（作废）未支付子订单
//
// 金额沿用插件的「元」，时间沿用 Unix 秒；落库为「分」与 TIMESTAMPTZ。

type cycleArtificialForm struct {
	ClientID    string  `json:"client_id"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	RenewAmount float64 `json:"renew_amount"`
	StartTime   int64   `json:"start_time"`
	EndTime     int64   `json:"end_time"`
	Num         int     `json:"num"`
	Unit        string  `json:"unit"`
}

func (f cycleArtificialForm) input() store.CycleArtificialOrderInput {
	in := store.CycleArtificialOrderInput{
		ClientPublicID:   strings.TrimSpace(f.ClientID),
		Description:      f.Description,
		AmountCents:      int64(math.Round(f.Amount * 100)),
		RenewAmountCents: int64(math.Round(f.RenewAmount * 100)),
		Num:              f.Num,
		Unit:             strings.ToLower(strings.TrimSpace(f.Unit)),
	}
	if f.StartTime > 0 {
		in.StartAt = time.Unix(f.StartTime, 0)
	}
	if f.EndTime > 0 {
		end := time.Unix(f.EndTime, 0)
		in.EndAt = &end
	}
	return in
}

// cycleArtificialPayload 把内部结构转成插件字段形状（元 / Unix 秒）。
func cycleArtificialPayload(v store.CycleArtificialOrder) gin.H {
	endUnix, lastUnix, nextUnix := int64(0), int64(0), int64(0)
	if v.EndAt != nil {
		endUnix = v.EndAt.Unix()
	}
	if v.LastGeneratedAt != nil {
		lastUnix = v.LastGeneratedAt.Unix()
	}
	if v.NextGenerateAt != nil {
		nextUnix = v.NextGenerateAt.Unix()
	}
	return gin.H{
		"id": v.PublicID, "client_id": v.ClientPublicID,
		"username": v.Username, "company": v.Company, "email": v.Email,
		"description": v.Description,
		"amount":      float64(v.AmountCents) / 100, "renew_amount": float64(v.RenewAmountCents) / 100,
		"start_time": v.StartAt.Unix(), "end_time": endUnix,
		"num": v.Num, "unit": v.Unit,
		"last_generated_at": lastUnix, "next_generate_at": nextUnix,
		"generated_count":     v.GeneratedCount,
		"client_credit_cents": v.ClientCreditCents,
		"created_at":          v.CreatedAt,
	}
}

func (a *App) adminListCycleArtificialOrders(c *gin.Context) {
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	items, total, err := a.Store.ListCycleArtificialOrders(c, c.Query("keywords"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "CYCLE_ORDERS_FAILED", "读取周期人工订单失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, v := range items {
		list = append(list, cycleArtificialPayload(v))
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": total, "page": page, "limit": limit})
}

func (a *App) adminCreateCycleArtificialOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in cycleArtificialForm
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.CreateCycleArtificialOrder(c, in.input())
	if err != nil {
		httpx.Fail(c, 400, "CYCLE_ORDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "cycle_order.create", "cycle_order", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"client_id": v.ClientPublicID, "amount": float64(v.AmountCents) / 100, "num": v.Num, "unit": v.Unit})
	httpx.OK(c, 201, gin.H{"cycle_order": cycleArtificialPayload(v)})
}

func (a *App) adminGetCycleArtificialOrder(c *gin.Context) {
	cycle, err := a.Store.GetCycleArtificialOrder(c, c.Param("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "CYCLE_ORDER_NOT_FOUND", "周期人工订单不存在")
		return
	case err != nil:
		httpx.Fail(c, 500, "CYCLE_ORDERS_FAILED", "读取周期人工订单失败")
		return
	}
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	f := store.CycleOrderChildrenFilter{
		Status:  strings.ToLower(strings.TrimSpace(c.Query("status"))),
		Gateway: strings.TrimSpace(c.Query("gateway")),
		OrderBy: strings.TrimSpace(c.Query("orderby")),
		Sort:    strings.TrimSpace(c.Query("sort")),
		Limit:   limit,
		Offset:  (page - 1) * limit,
	}
	if raw := strings.TrimSpace(c.Query("amount")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			f.AmountCents = int64(math.Round(v * 100))
		}
	}
	if raw := strings.TrimSpace(c.Query("start_time")); raw != "" {
		if ts, err := strconv.ParseInt(raw, 10, 64); err == nil && ts > 0 {
			t := time.Unix(ts, 0)
			f.StartAt = &t
		}
	}
	if raw := strings.TrimSpace(c.Query("end_time")); raw != "" {
		if ts, err := strconv.ParseInt(raw, 10, 64); err == nil && ts > 0 {
			t := time.Unix(ts, 0)
			f.EndAt = &t
		}
	}
	children, total, err := a.Store.ListCycleArtificialOrderChildren(c, cycle.PublicID, f)
	if err != nil {
		httpx.Fail(c, 500, "CYCLE_ORDERS_FAILED", "读取子订单失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": children, "count": total, "page": page, "limit": limit, "cycle_order": cycleArtificialPayload(cycle)})
}

func (a *App) adminUpdateCycleArtificialOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in cycleArtificialForm
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateCycleArtificialOrder(c, c.Param("id"), in.input())
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "CYCLE_ORDER_NOT_FOUND", "周期人工订单不存在")
		return
	case err != nil:
		httpx.Fail(c, 400, "CYCLE_ORDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "cycle_order.update", "cycle_order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"description": in.Description, "amount": in.Amount, "num": in.Num, "unit": in.Unit})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteCycleArtificialOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteCycleArtificialOrder(c, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "CYCLE_ORDER_NOT_FOUND", "周期人工订单不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "cycle_order.delete", "cycle_order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminAdjustArtificialOrder 调整未支付人工订单的价格与描述（插件「调整价格」）。
func (a *App) adminAdjustArtificialOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Amount      *float64 `json:"amount"`
		Description string   `json:"description"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Amount == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	amountCents := int64(math.Round(*in.Amount * 100))
	err := a.Store.AdjustArtificialOrder(c, c.Param("id"), amountCents, in.Description)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "ORDER_NOT_ADJUSTABLE", "仅未支付的人工订单支持调整价格")
		return
	case err != nil:
		httpx.Fail(c, 400, "ADJUST_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "cycle_order.adjust_price", "order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"amount": *in.Amount, "description": in.Description})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminMarkArtificialOrderPaid 后台标记支付：use_credit=1 时优先扣除用户余额
// （余额不足则扣可用部分，余下记为线下收款）。对应插件「标记支付」弹窗。
func (a *App) adminMarkArtificialOrderPaid(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		UseCredit flexBool `json:"use_credit"`
	}
	_ = c.ShouldBindJSON(&in)
	credit, err := a.Store.AdminMarkArtificialOrderPaid(c, c.Param("id"), bool(in.UseCredit))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "ORDER_NOT_PAYABLE", "仅未支付的人工订单可以标记支付")
		return
	case err != nil:
		httpx.Fail(c, 400, "MARK_PAID_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "cycle_order.mark_paid", "order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"use_credit": bool(in.UseCredit), "credit_used_cents": credit})
	httpx.OK(c, 200, gin.H{"ok": true, "credit_used_cents": credit})
}

// adminDeleteArtificialOrder 删除（作废）单笔未支付人工订单。
func (a *App) adminDeleteArtificialOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	n, err := a.Store.CancelArtificialOrders(c, []string{c.Param("id")})
	switch {
	case err != nil:
		httpx.Fail(c, 400, "DELETE_FAILED", err.Error())
		return
	case n == 0:
		httpx.Fail(c, 409, "ORDER_NOT_DELETABLE", "订单不存在或不是未支付的人工订单")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "cycle_order.delete_order", "order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminBatchDeleteArtificialOrders 批量删除未支付人工订单（插件详情页批量操作）。
func (a *App) adminBatchDeleteArtificialOrders(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ID []string `json:"id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || len(in.ID) == 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择要删除的订单")
		return
	}
	n, err := a.Store.CancelArtificialOrders(c, in.ID)
	if err != nil {
		httpx.Fail(c, 400, "DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "cycle_order.batch_delete", "order", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"ids": in.ID, "cancelled": n})
	httpx.OK(c, 200, gin.H{"ok": true, "cancelled": n})
}
