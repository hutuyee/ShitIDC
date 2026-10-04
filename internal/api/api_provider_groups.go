package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 接口分组与容量分配（对应魔方 shd_server_groups）。
//
// 商品可以绑定到分组而不是单个接口：开通时由核心按策略挑一个还有容量的接口，
// 加机器只要往分组里加接口，商品不用改。

func (a *App) adminListProviderGroups(c *gin.Context) {
	groups, err := a.Store.ListProviderGroups(c)
	if err != nil {
		httpx.Fail(c, 500, "PROVIDER_GROUPS_FAILED", "读取接口分组失败")
		return
	}
	httpx.OK(c, 200, groups)
}

func (a *App) adminCreateProviderGroup(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Name        string `json:"name"`
		Strategy    string `json:"strategy"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	g, err := a.Store.CreateProviderGroup(c, in.Name, in.Strategy, in.Description)
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_GROUP_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "provider_group.create", "provider_group", g.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, g)
	httpx.OK(c, 201, g)
}

func (a *App) adminDeleteProviderGroup(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteProviderGroup(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "PROVIDER_GROUP_NOT_FOUND", "接口分组不存在")
			return
		}
		httpx.Fail(c, 400, "PROVIDER_GROUP_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "provider_group.delete", "provider_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminListProviderGroupMembers 列出分组内的接口及其当前负载与剩余容量。
func (a *App) adminListProviderGroupMembers(c *gin.Context) {
	members, err := a.Store.ListGroupMembers(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "PROVIDER_GROUP_MEMBERS_FAILED", "读取分组成员失败")
		return
	}
	httpx.OK(c, 200, members)
}

// adminAddProviderGroupMember 把接口加入分组，并设置容量上限与权重。
func (a *App) adminAddProviderGroupMember(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProviderID  string `json:"provider_id"`
		MaxServices int    `json:"max_services"`
		Weight      int    `json:"weight"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProviderID) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择要加入的接口")
		return
	}
	if err := a.Store.AddProviderToGroup(c, c.Param("id"), in.ProviderID, in.MaxServices, in.Weight); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "PROVIDER_GROUP_NOT_FOUND", "接口分组或接口不存在")
			return
		}
		httpx.Fail(c, 400, "PROVIDER_GROUP_MEMBER_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "provider_group.member_add", "provider_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 201, map[string]bool{"ok": true})
}

func (a *App) adminRemoveProviderGroupMember(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.RemoveProviderFromGroup(c, c.Param("id"), c.Param("provider_id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "PROVIDER_GROUP_MEMBER_NOT_FOUND", "该接口不在此分组中")
			return
		}
		httpx.Fail(c, 400, "PROVIDER_GROUP_MEMBER_REMOVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "provider_group.member_remove", "provider_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- 客户组按产品差异定价 ----

// adminListUserProductPrices 列出某个客户组的所有专属价。
func (a *App) adminListUserProductPrices(c *gin.Context) {
	items, err := a.Store.ListUserProductPrices(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "GROUP_PRICES_FAILED", "读取专属价失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminSetUserProductPrice 设置某个组买某个商品的专属价。
func (a *App) adminSetUserProductPrice(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProductID    string `json:"product_id"`
		BillingCycle string `json:"billing_cycle"`
		Currency     string `json:"currency"`
		AmountCents  int64  `json:"amount_cents"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProductID) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择商品")
		return
	}
	if err := a.Store.SetUserProductPrice(c, c.Param("id"), in.ProductID, in.BillingCycle, in.Currency, in.AmountCents); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "GROUP_OR_PRODUCT_NOT_FOUND", "客户组或商品不存在")
			return
		}
		httpx.Fail(c, 400, "GROUP_PRICE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "user_group.price_set", "user_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteUserProductPrice 删除一条专属价（删掉后回落到标价 + 组折扣）。
func (a *App) adminDeleteUserProductPrice(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteUserProductPrice(c, c.Param("id"), c.Param("product_id"), c.Query("billing_cycle"), c.Query("currency")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "GROUP_PRICE_NOT_FOUND", "专属价不存在")
			return
		}
		httpx.Fail(c, 400, "GROUP_PRICE_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "user_group.price_delete", "user_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// quoteProductPrice 给前台算「这个用户买这个商品要多少钱」，含组折扣与专属价。
// 下单时服务端会重算一遍，这个接口只用于展示，不参与结算。
func (a *App) quoteProductPrice(c *gin.Context) {
	principal, _ := getPrincipal(c)
	quote, err := a.Store.QuoteProductPrice(c, principal.User.ID, c.Param("id"), c.Query("billing_cycle"), c.Query("currency"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRICE_NOT_FOUND", "该商品在此周期与币种下没有价格")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "QUOTE_FAILED", "读取价格失败")
		return
	}
	httpx.OK(c, 200, quote)
}
