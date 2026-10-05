package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 商品返现（对齐魔方 CBAP 插件 product_cashback）：后台 CRUD。
// 支付成功后的返现入账在 store.PayProductCashback（afterPaymentCompleted 调用）。

// adminListProductCashbacks 列出全部返现规则。
func (a *App) adminListProductCashbacks(c *gin.Context) {
	items, err := a.Store.ListProductCashbacks(c)
	if err != nil {
		httpx.Fail(c, 500, "CASHBACKS_FAILED", "读取商品返现失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminCreateProductCashback 新增一条返现规则。
func (a *App) adminCreateProductCashback(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProductID  string `json:"product_id"`
		Type       string `json:"type"`
		PriceCents int64  `json:"price_cents"`
		PeriodDays int    `json:"period_days"`
		Active     *bool  `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.ProductID = strings.TrimSpace(in.ProductID)
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	if in.Type == "" {
		in.Type = "fixed"
	}
	if in.Type != "fixed" {
		httpx.Fail(c, 400, "CASHBACK_TYPE_INVALID", "暂只支持固定金额返现")
		return
	}
	if in.ProductID == "" || in.PriceCents <= 0 || in.PeriodDays < 0 {
		httpx.Fail(c, 400, "CASHBACK_INVALID", "商品、返现金额与期限不合法")
		return
	}

	active := true
	if in.Active != nil {
		active = *in.Active
	}
	v, err := a.Store.CreateProductCashback(c, in.ProductID, in.Type, in.PriceCents, in.PeriodDays, active)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "CASHBACK_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cashback.create", "product_cashback", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"product_id": in.ProductID, "price_cents": in.PriceCents, "period_days": in.PeriodDays})
	httpx.OK(c, 201, v)
}

// adminUpdateProductCashback 修改返现类型、金额与期限。
func (a *App) adminUpdateProductCashback(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Type       string `json:"type"`
		PriceCents int64  `json:"price_cents"`
		PeriodDays int    `json:"period_days"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	if in.Type == "" {
		in.Type = "fixed"
	}
	if in.Type != "fixed" || in.PriceCents <= 0 || in.PeriodDays < 0 {
		httpx.Fail(c, 400, "CASHBACK_INVALID", "返现类型、金额与期限不合法")
		return
	}
	err := a.Store.UpdateProductCashback(c, c.Param("id"), in.Type, in.PriceCents, in.PeriodDays)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CASHBACK_NOT_FOUND", "返现规则不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "CASHBACK_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cashback.update", "product_cashback", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"price_cents": in.PriceCents, "period_days": in.PeriodDays})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetProductCashbackStatus 启用/停用一条规则。
func (a *App) adminSetProductCashbackStatus(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Active *bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Active == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.SetProductCashbackStatus(c, c.Param("id"), *in.Active)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CASHBACK_NOT_FOUND", "返现规则不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "CASHBACK_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cashback.status", "product_cashback", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"active": *in.Active})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteProductCashback 删除一条规则。
func (a *App) adminDeleteProductCashback(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteProductCashback(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CASHBACK_NOT_FOUND", "返现规则不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "CASHBACK_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cashback.delete", "product_cashback", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
