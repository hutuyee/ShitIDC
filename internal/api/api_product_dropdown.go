package api

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 商品下拉优化（对齐魔方 CBAP ProductDropDownSelect 插件）。
//
// 插件契约只有 GET/PUT /product_drop_down_select/config 两个接口；后台选样式，
// 用户端按样式渲染「服务详情 → 升降级」弹窗里的目标商品下拉框。
// 分组视图随配置一并返回，前端不需要自己再拉一遍商品。

// adminGetProductDropDown 返回当前样式与分组预览（预览供后台页还原插件的四张示例卡）。
func (a *App) adminGetProductDropDown(c *gin.Context) {
	cfg, err := a.Store.GetProductDropDownConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_DROP_DOWN_FAILED", "读取商品下拉配置失败")
		return
	}
	groups, err := a.Store.ListProductDropDownGroups(c)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_DROP_DOWN_FAILED", "读取商品分组失败")
		return
	}
	httpx.OK(c, 200, gin.H{"style": cfg.Style, "styles": store.ProductDropDownStyles, "groups": groups})
}

// adminSaveProductDropDown 保存下拉样式。
func (a *App) adminSaveProductDropDown(c *gin.Context) {
	var in struct {
		Style string `json:"style"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SaveProductDropDownConfig(c, in.Style); err != nil {
		if errors.Is(err, store.ErrInvalidState) {
			httpx.Fail(c, 400, "PRODUCT_DROP_DOWN_STYLE_INVALID", "未知下拉样式："+in.Style)
			return
		}
		httpx.Fail(c, 500, "PRODUCT_DROP_DOWN_FAILED", "保存商品下拉配置失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "product_drop_down.save", "product_drop_down_select", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"style": in.Style})
	}
	httpx.OK(c, 200, gin.H{"ok": true, "style": in.Style})
}

// myProductDropDown 用户端读取生效的下拉样式与分组视图（default 时前端按平铺渲染）。
func (a *App) myProductDropDown(c *gin.Context) {
	cfg, err := a.Store.GetProductDropDownConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "PRODUCT_DROP_DOWN_FAILED", "读取商品下拉配置失败")
		return
	}
	httpx.OK(c, 200, gin.H{"style": cfg.Style})
}
