package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 商品购买限制（对齐魔方 CBAP 插件 product_cert_limit / product_cycle_limit /
// product_related_limit）：后台 CRUD。
// 下单时的强制校验在 store.checkProductPurchaseLimitsTx（实名 / 周期 / 必需 /
// 互斥）与 store.CheckProductBundleLimits（捆绑，整车维度）。

// ---------- 实名要求 ----------

// adminListProductCertLimits 列出全部商品实名要求。
func (a *App) adminListProductCertLimits(c *gin.Context) {
	items, err := a.Store.ListProductCertLimits(c)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_CERT_LIMITS_FAILED", "读取商品实名要求失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminCreateProductCertLimit 新增一条商品实名要求。
func (a *App) adminCreateProductCertLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProductID string `json:"product_id"`
		Type      int    `json:"type"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.ProductID = strings.TrimSpace(in.ProductID)
	if in.ProductID == "" {
		httpx.Fail(c, 400, "PRODUCT_CERT_LIMIT_INVALID", "请选择商品")
		return
	}
	v, err := a.Store.CreateProductCertLimit(c, in.ProductID, in.Type)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_CERT_LIMIT_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cert_limit.create", "product_cert_limit", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"product_id": in.ProductID, "type": v.Type})
	httpx.OK(c, 201, v)
}

// adminUpdateProductCertLimit 修改类型要求（商品不可改）。
func (a *App) adminUpdateProductCertLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Type int `json:"type"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateProductCertLimit(c, c.Param("id"), in.Type)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_CERT_LIMIT_NOT_FOUND", "实名要求不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_CERT_LIMIT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cert_limit.update", "product_cert_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"type": in.Type})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetProductCertLimitStatus 启用 / 停用实名要求。
func (a *App) adminSetProductCertLimitStatus(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Status *bool `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Status == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.SetProductCertLimitStatus(c, c.Param("id"), *in.Status)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_CERT_LIMIT_NOT_FOUND", "实名要求不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_CERT_LIMIT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cert_limit.status", "product_cert_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": *in.Status})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteProductCertLimit 删除实名要求。
func (a *App) adminDeleteProductCertLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteProductCertLimit(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_CERT_LIMIT_NOT_FOUND", "实名要求不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_CERT_LIMIT_DELETE_FAILED", "删除失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cert_limit.delete", "product_cert_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---------- 周期性限购 ----------

// adminListProductCycleLimits 列出全部周期性限购。
func (a *App) adminListProductCycleLimits(c *gin.Context) {
	items, err := a.Store.ListProductCycleLimits(c)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_CYCLE_LIMITS_FAILED", "读取周期性限购失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminCreateProductCycleLimit 新增一条周期性限购。
func (a *App) adminCreateProductCycleLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProductID string `json:"product_id"`
		Num       int    `json:"num"`
		Cycle     int    `json:"cycle"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.ProductID = strings.TrimSpace(in.ProductID)
	if in.ProductID == "" {
		httpx.Fail(c, 400, "PRODUCT_CYCLE_LIMIT_INVALID", "请选择商品")
		return
	}
	v, err := a.Store.CreateProductCycleLimit(c, in.ProductID, in.Num, in.Cycle)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_CYCLE_LIMIT_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cycle_limit.create", "product_cycle_limit", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"product_id": in.ProductID, "num": v.Num, "cycle": v.Cycle})
	httpx.OK(c, 201, v)
}

// adminUpdateProductCycleLimit 修改限制数量与周期（商品不可改）。
func (a *App) adminUpdateProductCycleLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Num   int `json:"num"`
		Cycle int `json:"cycle"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateProductCycleLimit(c, c.Param("id"), in.Num, in.Cycle)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_CYCLE_LIMIT_NOT_FOUND", "周期性限购不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_CYCLE_LIMIT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cycle_limit.update", "product_cycle_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"num": in.Num, "cycle": in.Cycle})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetProductCycleLimitStatus 启用 / 停用周期性限购。
func (a *App) adminSetProductCycleLimitStatus(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Status *bool `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Status == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.SetProductCycleLimitStatus(c, c.Param("id"), *in.Status)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_CYCLE_LIMIT_NOT_FOUND", "周期性限购不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_CYCLE_LIMIT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cycle_limit.status", "product_cycle_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": *in.Status})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteProductCycleLimit 删除周期性限购。
func (a *App) adminDeleteProductCycleLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteProductCycleLimit(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_CYCLE_LIMIT_NOT_FOUND", "周期性限购不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_CYCLE_LIMIT_DELETE_FAILED", "删除失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_cycle_limit.delete", "product_cycle_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---------- 关联限购 ----------

// adminListProductRelatedLimits 列出全部关联限购。
func (a *App) adminListProductRelatedLimits(c *gin.Context) {
	items, err := a.Store.ListProductRelatedLimits(c)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_RELATED_LIMITS_FAILED", "读取关联限购失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminCreateProductRelatedLimit 新增一条关联限购。
func (a *App) adminCreateProductRelatedLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProductID        string   `json:"product_id"`
		RelatedProductID []string `json:"related_product_id"`
		Type             int      `json:"type"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.ProductID = strings.TrimSpace(in.ProductID)
	if in.ProductID == "" {
		httpx.Fail(c, 400, "PRODUCT_RELATED_LIMIT_INVALID", "请选择被限制商品")
		return
	}
	v, err := a.Store.CreateProductRelatedLimit(c, in.ProductID, in.RelatedProductID, in.Type)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_RELATED_LIMIT_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_related_limit.create", "product_related_limit", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"product_id": in.ProductID, "type": v.Type, "related": v.RelatedPublicIDs})
	httpx.OK(c, 201, v)
}

// adminUpdateProductRelatedLimit 修改关联商品与类型（被限制商品不可改）。
func (a *App) adminUpdateProductRelatedLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		RelatedProductID []string `json:"related_product_id"`
		Type             int      `json:"type"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateProductRelatedLimit(c, c.Param("id"), in.RelatedProductID, in.Type)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_RELATED_LIMIT_NOT_FOUND", "关联限购不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_RELATED_LIMIT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_related_limit.update", "product_related_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"type": in.Type, "related": in.RelatedProductID})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetProductRelatedLimitStatus 启用 / 停用关联限购。
func (a *App) adminSetProductRelatedLimitStatus(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Status *bool `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Status == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.SetProductRelatedLimitStatus(c, c.Param("id"), *in.Status)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_RELATED_LIMIT_NOT_FOUND", "关联限购不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_RELATED_LIMIT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_related_limit.status", "product_related_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": *in.Status})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteProductRelatedLimit 删除关联限购。
func (a *App) adminDeleteProductRelatedLimit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteProductRelatedLimit(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_RELATED_LIMIT_NOT_FOUND", "关联限购不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_RELATED_LIMIT_DELETE_FAILED", "删除失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "product_related_limit.delete", "product_related_limit", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
