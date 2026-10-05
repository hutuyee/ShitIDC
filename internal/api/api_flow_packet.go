package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 流量包（对齐魔方 CBAP FlowPacket 插件）。
//
// 后台维护流量包配置（名称 / 流量GB / 售价 / 库存 / 关联商品）并查看购买订单；
// 用户端「流量包」页为名下关联产品购买，余额支付：下单时生成待付款订单，
// 支付后扣余额并把订单置为已付款。插件只暴露了后台前端契约
//（/flow_packet、/flow_packet_order），这里按本站风格落到 admin 路由。

type flowPacketBody struct {
	Name        string   `json:"name"`
	CapacityGB  int      `json:"capacity_gb"`
	PriceCents  int64    `json:"price_cents"`
	Stock       int      `json:"stock"`
	StockEnable bool     `json:"stock_enable"`
	Notes       string   `json:"notes"`
	ProductIDs  []string `json:"product_ids"`
}

func (in flowPacketBody) validate() string {
	switch {
	case strings.TrimSpace(in.Name) == "":
		return "请填写流量包名称"
	case in.CapacityGB < 0:
		return "流量不能为负数"
	case in.PriceCents < 0:
		return "售价不能为负数"
	case in.Stock < 0:
		return "库存不能为负数"
	case len(in.ProductIDs) == 0:
		return "请至少选择一个关联商品"
	}
	return ""
}

func (in flowPacketBody) toInput() store.FlowPacketInput {
	return store.FlowPacketInput{
		Name:        in.Name,
		CapacityGB:  in.CapacityGB,
		PriceCents:  in.PriceCents,
		Stock:       in.Stock,
		StockEnable: in.StockEnable,
		Notes:       in.Notes,
		ProductIDs:  in.ProductIDs,
	}
}

// adminListFlowPackets 流量包列表；status 取 1（开启）/ 0（关闭），空为全部。
func (a *App) adminListFlowPackets(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 100)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	var active *bool
	switch c.Query("status") {
	case "1":
		v := true
		active = &v
	case "0":
		v := false
		active = &v
	}
	items, total, err := a.Store.ListFlowPackets(c, c.Query("keyword"), active, limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKETS_FAILED", "读取流量包失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": total, "page": page})
}

func (a *App) adminCreateFlowPacket(c *gin.Context) {
	var in flowPacketBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if msg := in.validate(); msg != "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", msg)
		return
	}
	packet, err := a.Store.CreateFlowPacket(c, in.toInput())
	if errors.Is(err, store.ErrFlowPacketProduct) {
		httpx.Fail(c, 400, "FLOW_PACKET_PRODUCT_INVALID", "关联商品不存在或不合法")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKET_CREATE_FAILED", "创建流量包失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "flow_packet.create", "flow_packet", packet.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": packet.Name, "capacity_gb": packet.CapacityGB, "price_cents": packet.PriceCents})
	}
	httpx.OK(c, 201, packet)
}

func (a *App) adminUpdateFlowPacket(c *gin.Context) {
	var in flowPacketBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if msg := in.validate(); msg != "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", msg)
		return
	}
	packet, err := a.Store.UpdateFlowPacket(c, c.Param("id"), in.toInput())
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "FLOW_PACKET_NOT_FOUND", "流量包不存在")
		return
	case errors.Is(err, store.ErrFlowPacketProduct):
		httpx.Fail(c, 400, "FLOW_PACKET_PRODUCT_INVALID", "关联商品不存在或不合法")
		return
	case err != nil:
		httpx.Fail(c, 500, "FLOW_PACKET_UPDATE_FAILED", "保存流量包失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "flow_packet.update", "flow_packet", packet.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": packet.Name, "capacity_gb": packet.CapacityGB, "price_cents": packet.PriceCents})
	}
	httpx.OK(c, 200, packet)
}

// adminSetFlowPacketStatus 启停流量包（对齐插件「开关」）。
func (a *App) adminSetFlowPacketStatus(c *gin.Context) {
	var in struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.SetFlowPacketActive(c, c.Param("id"), in.Active)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "FLOW_PACKET_NOT_FOUND", "流量包不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKET_STATUS_FAILED", "更新流量包状态失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "flow_packet.status", "flow_packet", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"active": in.Active})
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminDeleteFlowPacket(c *gin.Context) {
	err := a.Store.DeleteFlowPacket(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "FLOW_PACKET_NOT_FOUND", "流量包不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKET_DELETE_FAILED", "删除流量包失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "flow_packet.delete", "flow_packet", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminListFlowPacketOrders 流量包订单列表（关键词：用户邮箱 / 流量包名 / 产品ID）。
func (a *App) adminListFlowPacketOrders(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 100)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	status := c.Query("status")
	switch status {
	case "unpaid", "paid", "cancelled", "refunded":
	default:
		status = ""
	}
	items, total, err := a.Store.ListFlowPacketOrders(c, c.Query("keyword"), status, limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKET_ORDERS_FAILED", "读取流量包订单失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": total, "page": page})
}

func (a *App) adminDeleteFlowPacketOrder(c *gin.Context) {
	err := a.Store.DeleteFlowPacketOrder(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "FLOW_PACKET_ORDER_NOT_FOUND", "订单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKET_ORDER_DELETE_FAILED", "删除订单失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "flow_packet.order_delete", "flow_packet_order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 用户端 ----

// listMyFlowPackets 上架中的流量包 + 每个包可用于名下哪些产品。
func (a *App) listMyFlowPackets(c *gin.Context) {
	p, _ := getPrincipal(c)
	items, err := a.Store.ListUserFlowPackets(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKETS_FAILED", "读取流量包失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

// listMyFlowPacketOrders 我的流量包订单。
func (a *App) listMyFlowPacketOrders(c *gin.Context) {
	p, _ := getPrincipal(c)
	items, err := a.Store.ListUserFlowPacketOrders(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "FLOW_PACKET_ORDERS_FAILED", "读取流量包订单失败")
		return
	}
	httpx.OK(c, 200, items)
}

// purchaseFlowPacket 为名下产品下单（待付款，随后余额支付）。
func (a *App) purchaseFlowPacket(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ServiceID string `json:"service_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.ServiceID = strings.TrimSpace(in.ServiceID)
	if in.ServiceID == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择要购买的产品")
		return
	}
	order, err := a.Store.CreateFlowPacketOrder(c, p.User.ID, c.Param("id"), in.ServiceID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "FLOW_PACKET_NOT_FOUND", "流量包或产品不存在")
		return
	case errors.Is(err, store.ErrFlowPacketInactive):
		httpx.Fail(c, 409, "FLOW_PACKET_INACTIVE", "流量包已下架")
		return
	case errors.Is(err, store.ErrFlowPacketSoldOut):
		httpx.Fail(c, 409, "FLOW_PACKET_SOLD_OUT", "流量包库存不足")
		return
	case errors.Is(err, store.ErrFlowPacketIneligible):
		httpx.Fail(c, 400, "FLOW_PACKET_NOT_APPLICABLE", "该产品不适用于此流量包")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "FLOW_PACKET_SERVICE_STATE", "该产品当前状态不支持购买流量包")
		return
	case err != nil:
		httpx.Fail(c, 500, "FLOW_PACKET_ORDER_FAILED", "创建流量包订单失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "flow_packet.order_create", "flow_packet_order", order.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"packet": order.PacketName, "service_id": order.ServiceID, "amount_cents": order.AmountCents})
	httpx.OK(c, 201, order)
}

// payFlowPacketOrder 余额支付流量包订单。
func (a *App) payFlowPacketOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	order, err := a.Store.PayFlowPacketOrder(c, p.User.ID, c.Param("id"), strings.TrimSpace(c.GetHeader("Idempotency-Key")))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "FLOW_PACKET_ORDER_NOT_FOUND", "订单不存在")
		return
	case errors.Is(err, store.ErrInsufficientBalance):
		httpx.Fail(c, 402, "INSUFFICIENT_BALANCE", "余额不足，请先充值")
		return
	case errors.Is(err, store.ErrFlowPacketSoldOut):
		httpx.Fail(c, 409, "FLOW_PACKET_SOLD_OUT", "流量包库存不足，无法支付")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "FLOW_PACKET_ORDER_NOT_PAYABLE", "该订单当前不可支付")
		return
	case err != nil:
		httpx.Fail(c, 500, "FLOW_PACKET_PAY_FAILED", "支付流量包订单失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "flow_packet.order_pay", "flow_packet_order", order.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"packet": order.PacketName, "amount_cents": order.AmountCents})
	if order.ServiceID != "" && a.Bus != nil {
		a.Bus.Emit(a.eventCtx(c), events.ServiceUpdated, map[string]any{"service_id": order.ServiceID, "kind": "flow_packet_paid", "packet": order.PacketName, "capacity_gb": order.CapacityGB})
	}
	httpx.OK(c, 200, order)
}

// cancelFlowPacketOrder 取消未付款订单。
func (a *App) cancelFlowPacketOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	order, err := a.Store.CancelFlowPacketOrder(c, p.User.ID, c.Param("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "FLOW_PACKET_ORDER_NOT_FOUND", "订单不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "FLOW_PACKET_ORDER_NOT_CANCELLABLE", "只有未付款订单可以取消")
		return
	case err != nil:
		httpx.Fail(c, 500, "FLOW_PACKET_CANCEL_FAILED", "取消订单失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "flow_packet.order_cancel", "flow_packet_order", order.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, order)
}
