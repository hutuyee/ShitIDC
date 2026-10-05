package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 升降级（魔方 shd_upgrades 的 Go 实现）。
//
// 流程：列出可升级方案 → 报价 → 提交。差价为 0 或负数时立即生效；
// 为正时返回一张待付款的升级订单，付款后由 worker 通知上游切换方案。

// listUpgradePlans 返回某个服务可以升级到的目标方案与差价。
func (a *App) listUpgradePlans(c *gin.Context) {
	p, _ := getPrincipal(c)
	plans, err := a.Store.ListUpgradePlans(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "UPGRADE_PLANS_FAILED", "读取升级方案失败")
		return
	}
	httpx.OK(c, 200, plans)
}

// quoteUpgrade 对一次具体的目标方案报价，不落库。
func (a *App) quoteUpgrade(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ProductID    string `json:"product_id"`
		BillingCycle string `json:"billing_cycle"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProductID) == "" || strings.TrimSpace(in.BillingCycle) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择目标商品与计费周期")
		return
	}
	quote, err := a.Store.QuoteUpgrade(c, p.User.ID, c.Param("id"), in.ProductID, in.BillingCycle)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "UPGRADE_TARGET_NOT_FOUND", "目标商品或周期不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "SERVICE_NOT_UPGRADABLE", "仅生效中或已暂停的服务可以升降级")
		return
	case err != nil:
		httpx.Fail(c, 400, "UPGRADE_QUOTE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 200, quote)
}

// requestUpgrade 提交升降级。差价为 0 或负数时立即生效；否则返回待付款订单。
func (a *App) requestUpgrade(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ProductID    string `json:"product_id"`
		BillingCycle string `json:"billing_cycle"`
		VoucherCode  string `json:"voucher_code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProductID) == "" || strings.TrimSpace(in.BillingCycle) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择目标商品与计费周期")
		return
	}
	quote, orderPublic, err := a.Store.RequestUpgradeWithVoucher(c, p.User.ID, c.Param("id"), in.ProductID, in.BillingCycle, strings.TrimSpace(in.VoucherCode))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "UPGRADE_TARGET_NOT_FOUND", "目标商品或周期不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "SERVICE_NOT_UPGRADABLE", "仅生效中或已暂停的服务可以升降级")
		return
	case err != nil:
		httpx.Fail(c, 400, "UPGRADE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "service.upgrade", "service", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"to_product": in.ProductID, "to_cycle": in.BillingCycle, "diff_cents": quote.DiffCents, "order_id": orderPublic})
	if orderPublic == "" {
		// 降级/等价：立即生效，顺手通知上游切换方案。
		if a.Queue != nil {
			_ = a.Queue.ServiceUpgrade(c.Param("id"))
		}
		a.Bus.Emit(a.eventCtx(c), events.ServiceUpdated, map[string]any{"service_id": c.Param("id"), "kind": "upgrade_applied", "diff_cents": quote.DiffCents})
		httpx.OK(c, 200, map[string]any{"quote": quote, "payable": false, "message": "升级已生效"})
		return
	}
	a.Bus.Emit(a.eventCtx(c), events.OrderCreated, map[string]any{"order_id": orderPublic, "user_id": p.User.PublicID, "kind": "upgrade", "total_cents": quote.DiffCents})
	httpx.OK(c, 201, map[string]any{"quote": quote, "payable": true, "order_id": orderPublic, "message": "已生成升级订单，付款后生效"})
}
