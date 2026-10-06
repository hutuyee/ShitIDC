package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 产品自助转移（对齐魔方主程序附属插件 product_divert）。
//
// 用户在「产品转移」页把名下产品转给另一个用户：转出方按手机号 / 邮箱选人并
// （配置了费用时）支付转出费用；接收方「接收」并（配置了费用时）支付转入费用，
// 双方费用到账后产品立即迁移。后台页提供基础配置与全部转移记录。

// divertView 是给前端的记录行：对用户侧隐藏对方完整账号（掩码）。
type divertView struct {
	store.ProductDivert
	Role string `json:"role"` // push / pull / admin
}

func toDivertView(v store.ProductDivert, viewerID int64, unmask bool) divertView {
	out := divertView{ProductDivert: v, Role: "admin"}
	switch {
	case viewerID > 0 && v.PushUserID == viewerID:
		out.Role = "push"
	case viewerID > 0 && v.PullUserID == viewerID:
		out.Role = "pull"
	}
	if !unmask && viewerID > 0 {
		// 对方账号掩码展示；自己是哪一方就保留自己的完整账号。
		if v.PushUserID != viewerID {
			out.PushEmail = store.MaskDivertAccount(v.PushEmail)
		}
		if v.PullUserID != viewerID {
			out.PullEmail = store.MaskDivertAccount(v.PullEmail)
		}
	}
	return out
}

// myDivertConfig 用户端读取公开配置（启用开关 / 双方费用 / 有效期 / 保护期）。
func (a *App) myDivertConfig(c *gin.Context) {
	cfg, err := a.Store.GetProductDivertConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "DIVERT_CONFIG_FAILED", "读取转移配置失败")
		return
	}
	httpx.OK(c, 200, gin.H{
		"is_open":                cfg.Enabled,
		"validity_period_days":   cfg.ValidityPeriodDays,
		"push_cost_cents":        cfg.PushCostCents,
		"pull_cost_cents":        cfg.PullCostCents,
		"protection_period_days": cfg.ProtectionPeriodDays,
	})
}

// myDivertServices 列出当前用户名下服务及其可转移状态（转出表单数据源）。
func (a *App) myDivertServices(c *gin.Context) {
	p, _ := getPrincipal(c)
	items, err := a.Store.MyDivertableServices(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "DIVERT_SERVICES_FAILED", "读取服务列表失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

// myDivertLookup 按手机号 / 邮箱精确查找接收方，返回掩码账号。
func (a *App) myDivertLookup(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	id, email, err := a.Store.DivertLookupTarget(c, in.Name)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "DIVERT_USER_NOT_FOUND", "没有找到该手机号或邮箱对应的用户")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "DIVERT_LOOKUP_FAILED", "查找用户失败")
		return
	}
	httpx.OK(c, 200, gin.H{"id": id, "account": store.MaskDivertAccount(email)})
}

// myDivertList 当前用户的转移记录（转出或转入），status 取 1~4，空为全部。
func (a *App) myDivertList(c *gin.Context) {
	p, _ := getPrincipal(c)
	limit := parseIntDefault(c.Query("limit"), 50)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	status := parseIntDefault(c.Query("status"), 0)
	items, total, err := a.Store.ListMyProductDiverts(c, p.User.ID, status, limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "DIVERT_LIST_FAILED", "读取转移记录失败")
		return
	}
	out := make([]divertView, 0, len(items))
	for _, v := range items {
		out = append(out, toDivertView(v, p.User.ID, false))
	}
	httpx.OK(c, 200, gin.H{"list": out, "count": total, "page": page})
}

// myDivertCreate 发起转出。
func (a *App) myDivertCreate(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ServiceID string `json:"service_id"`
		ToUID     int64  `json:"to_uid"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.ServiceID) == "" || in.ToUID <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择要转出的产品和接收方")
		return
	}
	v, err := a.Store.CreateProductDivert(c, p.User.ID, strings.TrimSpace(in.ServiceID), in.ToUID)
	if err != nil {
		divertFail(c, err, "发起转出失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "divert.create", "product_divert", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"service_id": v.ServiceID, "to_uid": in.ToUID, "push_cost_cents": v.PushCostCents})
	httpx.OK(c, 200, toDivertView(v, p.User.ID, false))
}

// myDivertAccept 接收方接受转移（转入费用 > 0 时生成待支付费用单）。
func (a *App) myDivertAccept(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.AcceptProductDivert(c, p.User.ID, c.Param("id"))
	if err != nil {
		divertFail(c, err, "接收转移失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "divert.accept", "product_divert", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, toDivertView(v, p.User.ID, false))
}

// myDivertReject 接收方拒绝。
func (a *App) myDivertReject(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.RejectProductDivert(c, p.User.ID, c.Param("id"))
	if err != nil {
		divertFail(c, err, "拒绝转移失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "divert.reject", "product_divert", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, toDivertView(v, p.User.ID, false))
}

// myDivertCancel 转出方取消。
func (a *App) myDivertCancel(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.CancelProductDivert(c, p.User.ID, c.Param("id"))
	if err != nil {
		divertFail(c, err, "取消转移失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "divert.cancel", "product_divert", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, toDivertView(v, p.User.ID, false))
}

// myDivertVerify 手动检测：双方费用已支付但迁移未完成的兜底动作。
func (a *App) myDivertVerify(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.VerifyProductDivert(c, p.User.ID, c.Param("id"))
	if err != nil {
		divertFail(c, err, "检测失败")
		return
	}
	httpx.OK(c, 200, toDivertView(v, p.User.ID, false))
}

// adminGetDivertConfig 后台读取产品自助转移设置。
func (a *App) adminGetDivertConfig(c *gin.Context) {
	cfg, err := a.Store.GetProductDivertConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "DIVERT_CONFIG_FAILED", "读取转移配置失败")
		return
	}
	httpx.OK(c, 200, cfg)
}

// adminSaveDivertConfig 后台保存产品自助转移设置。
func (a *App) adminSaveDivertConfig(c *gin.Context) {
	operator, ok := getPrincipal(c)
	if !ok {
		httpx.Fail(c, 401, "UNAUTHORIZED", "未登录")
		return
	}
	var in store.ProductDivertConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SaveProductDivertConfig(c, in); err != nil {
		httpx.Fail(c, 400, "DIVERT_CONFIG_INVALID", err.Error())
		return
	}
	_ = a.Store.Audit(c, operator.User.ID, "divert.config", "system", "product_divert", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"is_open": in.Enabled, "push_cost_cents": in.PushCostCents, "pull_cost_cents": in.PullCostCents})
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminListDiverts 后台全部转移记录；关键词匹配商品 / 产品ID / 双方邮箱。
func (a *App) adminListDiverts(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 50)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	status := parseIntDefault(c.Query("status"), 0)
	items, total, err := a.Store.AdminListProductDiverts(c, c.Query("keyword"), status, limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "DIVERT_LIST_FAILED", "读取转移记录失败")
		return
	}
	out := make([]divertView, 0, len(items))
	for _, v := range items {
		out = append(out, toDivertView(v, 0, true))
	}
	httpx.OK(c, 200, gin.H{"list": out, "count": total, "page": page})
}

func divertFail(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "DIVERT_NOT_FOUND", "转移记录或目标用户不存在")
	case errors.Is(err, store.ErrDivertDisabled):
		httpx.Fail(c, 409, "DIVERT_DISABLED", "产品自助转移未启用")
	case errors.Is(err, store.ErrDivertTargetSelf):
		httpx.Fail(c, 400, "DIVERT_TARGET_SELF", "接收方就是产品当前所有者")
	case errors.Is(err, store.ErrDivertNotPushable):
		httpx.Fail(c, 400, "DIVERT_SERVICE_STATE", "已删除的产品不能转移")
	case errors.Is(err, store.ErrDivertProtected):
		httpx.Fail(c, 400, "DIVERT_PROTECTED", "产品仍在订购保护期内，暂时不能转移")
	case errors.Is(err, store.ErrDivertProductNotAllowed):
		httpx.Fail(c, 400, "DIVERT_PRODUCT_NOT_ALLOWED", "该商品不在允许自助转移的范围内")
	case errors.Is(err, store.ErrDivertExists):
		httpx.Fail(c, 409, "DIVERT_EXISTS", "该产品已有一笔待接收的转移")
	case errors.Is(err, store.ErrDivertPushUnpaid):
		httpx.Fail(c, 409, "DIVERT_PUSH_UNPAID", "转出方尚未完成转出费用的支付")
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "DIVERT_INVALID_STATE", "该转移当前状态不支持此操作")
	default:
		httpx.Fail(c, 500, "DIVERT_FAILED", fallback)
	}
}
