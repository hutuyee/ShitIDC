package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/provider/magiccube"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 本文件收纳会话、商品/订单、API Token 与后台供应商管理这批 HTTP 处理函数。
// 业务逻辑一律在 store 层，这里只做「解析参数 → 调 store → 统一响应」。

// parseIntDefault 解析查询串里的整数，非法或缺失时返回默认值。
func parseIntDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

// ---- 会话 ----

// logout 吊销当前会话并清掉 Cookie。
func (a *App) logout(c *gin.Context) {
	if cookie, err := c.Cookie("shitidc_session"); err == nil {
		_ = a.Store.RevokeSession(c, security.SHA256Hex(cookie))
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("shitidc_session", "", -1, "/", a.Cfg.CookieDomain, a.Cfg.CookieSecure, true)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// me 返回当前登录用户、权限、CSRF 与 API Token 标记，前端启动时用它判断会话是否有效。
func (a *App) me(c *gin.Context) {
	p, _ := getPrincipal(c)
	httpx.OK(c, 200, map[string]any{
		"user":        p.User,
		"permissions": p.Permissions,
		"csrf_token":  p.CSRF,
		"api_token":   p.APIToken,
	})
}

// ---- 商品与订单（前台）----

// listProducts 返回在售商品。管理端用 /admin/products，这里只给前台卖的东西。
func (a *App) listProducts(c *gin.Context) {
	v, err := a.Store.ListProducts(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取产品失败")
		return
	}
	httpx.OK(c, 200, v)
}

// listOrders 返回当前用户的订单；带 id 时返回单笔详情（含明细与支付信息）。
func (a *App) listOrders(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.ListOrders(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取订单失败")
		return
	}
	httpx.OK(c, 200, v)
}

// createOrder 下单。金额与折扣一律由服务端重算，请求里的价格字段会被忽略。
func (a *App) createOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ProductID    string               `json:"product_id"`
		BillingCycle string               `json:"billing_cycle"`
		Quantity     int                  `json:"quantity"`
		CouponCode   string               `json:"coupon_code"`
		VoucherCode  string               `json:"voucher_code"`
		Currency     string               `json:"currency"`
		Config       []store.ConfigChoice `json:"config"`
		CustomFields map[string]string    `json:"custom_fields"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProductID) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择商品")
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if in.BillingCycle == "" {
		in.BillingCycle = "monthly"
	}
	// 实名认证开关：开启时未实名的用户不能下单。
	if required, err := a.Store.CertificationRequired(c); err == nil && required {
		if ok, cerr := a.Store.IsCertified(c, p.User.ID); cerr == nil && !ok {
			httpx.Fail(c, 403, "CERTIFICATION_REQUIRED", "按站点要求需先完成实名认证")
			return
		}
	}
	o, err := a.Store.CreateOrderInCurrency(c, p.User.ID, in.ProductID, in.BillingCycle, in.Quantity, strings.TrimSpace(in.CouponCode), store.OrderConfigInput{
		Choices:      in.Config,
		CustomFields: in.CustomFields,
		VoucherCode:  strings.TrimSpace(in.VoucherCode),
	}, strings.ToUpper(strings.TrimSpace(in.Currency)))
	if err != nil {
		httpx.Fail(c, 400, "ORDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "order.create", "order", o.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"total": o.TotalCents, "currency": o.Currency})
	httpx.OK(c, 201, o)
}

// payOrder 余额支付。金额为 0 的订单（免费/试用）走 CompleteFreeOrder，不碰钱包。
func (a *App) payOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	// 幂等键优先取请求头；没给就由 store 现生成，重复提交不会重复扣款。
	idempotency := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	res, err := a.Store.PayOrderWithWallet(c, p.User.ID, c.Param("id"), idempotency)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单不存在")
		case errors.Is(err, store.ErrInsufficientBalance):
			httpx.Fail(c, 402, "INSUFFICIENT_BALANCE", "余额不足，请先充值")
		case errors.Is(err, store.ErrInvalidState):
			httpx.Fail(c, 409, "ORDER_NOT_PAYABLE", "该订单当前不可支付")
		default:
			httpx.Fail(c, 400, "ORDER_PAY_FAILED", err.Error())
		}
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "order.pay_wallet", "order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, res)
}

// ---- API Token（用户自助）----

func (a *App) listTokens(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.ListAPITokens(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取 Token 失败")
		return
	}
	httpx.OK(c, 200, v)
}

// createToken 生成一个 API Token。
//
// keyID 是公开部分；secret 是机密部分，**只在本次响应里返回一次明文**，
// 库里只保存它的 SHA256——这是刻意的：Token 泄留只能吊销，不能找回。
func (a *App) createToken(c *gin.Context) {
	p, _ := getPrincipal(c)
	if len(a.Cfg.MasterKey) == 0 {
		httpx.Fail(c, 503, "MASTER_KEY_REQUIRED", "需要配置 MASTER_KEY_BASE64")
		return
	}
	var in struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写 Token 名称")
		return
	}
	keyID, err := security.RandomToken(12)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成 Token 失败")
		return
	}
	secret, err := security.RandomToken(32)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成 Token 失败")
		return
	}
	scopes := sanitizeTokenScopes(in.Scopes)
	if err := a.Store.CreateAPIToken(c, p.User.ID, in.Name, keyID, security.SHA256Hex(secret), scopes, in.ExpiresAt); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "创建 Token 失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "api_token.create", "api_token", keyID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": in.Name, "scopes": scopes})
	httpx.OK(c, 201, map[string]any{"key_id": keyID, "secret": secret, "token": keyID + "." + secret, "scopes": scopes})
}

// revokeToken 吊销一个 API Token。
func (a *App) revokeToken(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.RevokeAPIToken(c, p.User.ID, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "TOKEN_NOT_FOUND", "Token 不存在或已吊销")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "api_token.revoke", "api_token", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// sanitizeTokenScopes 过滤掉未知权限，避免客户端塞任意字符串当 scope。
func sanitizeTokenScopes(raw []string) []string {
	allowed := map[string]bool{
		"order.read": true, "order.create": true, "service.read": true, "service.operate": true,
		"wallet.read": true, "ticket.read": true, "ticket.create": true,
		"profile.read": true, "profile.update": true, "api_token.manage": true,
	}
	out := []string{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if allowed[s] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = []string{"order.read", "service.read", "wallet.read"}
	}
	return out
}

// ---- 后台：供应商 ----

func (a *App) adminListProviders(c *gin.Context) {
	v, err := a.Store.ListProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取供应商失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminCreateProvider 新增供应商。
//
// 密钥先加密再落库；magiccube 的 auth_mode / paths 等非敏感项放在 config 里。
func (a *App) adminCreateProvider(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name         string         `json:"name"`
		ProviderType string         `json:"provider_type"`
		BaseURL      string         `json:"base_url"`
		Username     string         `json:"username"`
		APIKey       string         `json:"api_key"`
		Config       map[string]any `json:"config"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.ProviderType = strings.ToLower(strings.TrimSpace(in.ProviderType))
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.Name == "" || in.BaseURL == "" {
		httpx.Fail(c, 400, "PROVIDER_CONFIG_INVALID", "供应商名称与接口地址不能为空")
		return
	}
	if in.ProviderType == "" {
		in.ProviderType = "magiccube"
	}
	if len(a.Cfg.MasterKey) == 0 {
		httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法安全保存上游密钥")
		return
	}
	// 先构造一次客户端做静态校验，配置不对就不落库。
	cfg := in.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	probe := model.Provider{Name: in.Name, ProviderType: in.ProviderType, BaseURL: in.BaseURL, Config: cfg}
	if _, err := a.resolveProviderClient(c, probe, in.APIKey); err != nil {
		httpx.Fail(c, 400, "PROVIDER_CONFIG_INVALID", err.Error())
		return
	}
	enc, err := security.Encrypt(a.Cfg.MasterKey, in.APIKey)
	if err != nil {
		httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密上游密钥失败")
		return
	}
	v, err := a.Store.CreateProvider(c, in.Name, in.ProviderType, in.BaseURL, strings.TrimSpace(in.Username), enc, cfg)
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "provider.create", "provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "type": v.ProviderType})
	httpx.OK(c, 201, v)
}

// adminUpdateProvider 修改供应商。api_key 留空表示沿用已存的密钥。
func (a *App) adminUpdateProvider(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name     string         `json:"name"`
		BaseURL  string         `json:"base_url"`
		Username string         `json:"username"`
		APIKey   string         `json:"api_key"`
		Config   map[string]any `json:"config"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.BaseURL) == "" {
		httpx.Fail(c, 400, "PROVIDER_CONFIG_INVALID", "供应商名称与接口地址不能为空")
		return
	}
	// 密钥留空 → 传空串，store 侧保留原值（与支付/短信通道一致）。
	enc := ""
	if strings.TrimSpace(in.APIKey) != "" {
		if len(a.Cfg.MasterKey) == 0 {
			httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法安全保存上游密钥")
			return
		}
		var err error
		enc, err = security.Encrypt(a.Cfg.MasterKey, in.APIKey)
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密上游密钥失败")
			return
		}
	}
	cfg := in.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	v, err := a.Store.UpdateProvider(c, c.Param("id"), strings.TrimSpace(in.Name), strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.Username), enc, cfg)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "PROVIDER_NOT_FOUND", "供应商不存在")
			return
		}
		httpx.Fail(c, 400, "PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "provider.update", "provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, v)
}

// adminTestProvider 测试连接。结果写回 providers.status，后台列表直接显示。
func (a *App) adminTestProvider(c *gin.Context) {
	pv, client, err := a.providerClientFor(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_CLIENT_FAILED", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c, providerTestTimeout)
	defer cancel()
	if err := client.TestConnection(ctx); err != nil {
		a.Store.UpdateProviderCheck(c, pv.ID, false, err.Error())
		httpx.OK(c, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	a.Store.UpdateProviderCheck(c, pv.ID, true, "")
	httpx.OK(c, 200, map[string]any{"ok": true, "message": "连接正常"})
}

// adminSyncProvider 从上游拉取商品列表并落库。
//
// 目前只有 magiccube 支持商品同步；其他类型返回明确说明而不是静默成功。
func (a *App) adminSyncProvider(c *gin.Context) {
	pv, client, err := a.providerClientFor(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_CLIENT_FAILED", err.Error())
		return
	}
	mc, ok := client.(*magiccube.Client)
	if !ok {
		httpx.Fail(c, 400, "PROVIDER_SYNC_UNSUPPORTED", "该类型供应商不支持商品同步")
		return
	}
	ctx, cancel := context.WithTimeout(c, providerTestTimeout)
	defer cancel()
	remote, err := mc.ListProducts(ctx)
	if err != nil {
		a.Store.UpdateProviderCheck(c, pv.ID, false, err.Error())
		httpx.Fail(c, 502, "PROVIDER_SYNC_FAILED", err.Error())
		return
	}
	items := make([]model.ProviderProduct, 0, len(remote))
	for _, item := range remote {
		items = append(items, model.ProviderProduct{
			ProviderID:        pv.ID,
			UpstreamProductID: item.ID,
			Name:              item.Name,
			Description:       item.Description,
			PriceCents:        item.PriceCents,
			Currency:          item.Currency,
			BillingCycle:      item.BillingCycle,
			RawPayload:        item.Raw,
		})
	}
	if err := a.Store.SaveProviderProducts(c, pv.ID, items); err != nil {
		httpx.Fail(c, 500, "PROVIDER_SYNC_FAILED", err.Error())
		return
	}
	a.Store.UpdateProviderCheck(c, pv.ID, true, "")
	httpx.OK(c, 200, map[string]any{"synced": len(items)})
}

// adminProviderProducts 列出已同步的上游商品。
func (a *App) adminProviderProducts(c *gin.Context) {
	v, err := a.Store.ListProviderProducts(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取上游商品失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminImportProviderProduct 把上游商品导入成站内商品。
// 名称/描述/周期/币种/价格没填的项由 store 用同步下来的上游资料补齐。
func (a *App) adminImportProviderProduct(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		BillingCycle string `json:"billing_cycle"`
		Currency     string `json:"currency"`
		AmountCents  int64  `json:"amount_cents"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.ImportProviderProduct(c, c.Param("id"), c.Param("upstream_id"),
		strings.TrimSpace(in.Name), strings.TrimSpace(in.Description),
		strings.TrimSpace(in.BillingCycle), strings.ToUpper(strings.TrimSpace(in.Currency)), in.AmountCents)
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_PRODUCT_IMPORT_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "provider.product.import", "product", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 201, v)
}

// adminRetryService 重试一台开通失败的服务。
func (a *App) adminRetryService(c *gin.Context) {
	p, _ := getPrincipal(c)
	ok, err := a.Store.RetryFailedService(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "重试失败")
		return
	}
	if !ok {
		httpx.Fail(c, 409, "SERVICE_NOT_RETRYABLE", "仅开通失败的服务可以重试")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "service.retry", "service", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	// 重新入队，交给 worker 真正去开通。
	if a.Queue != nil {
		_ = a.Queue.Provision(c.Param("id"))
	}
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminAudit 审计日志。带 actor 时按操作者过滤（邮箱 / UID / UUID 都可以）。
func (a *App) adminAudit(c *gin.Context) {
	actor := strings.TrimSpace(c.Query("actor"))
	limit := parseIntDefault(c.Query("limit"), 200)
	if actor != "" {
		v, err := a.Store.ListAuditFiltered(c, strings.TrimSpace(c.Query("action")), actor, limit)
		if err != nil {
			httpx.Fail(c, 500, "INTERNAL_ERROR", "读取审计日志失败")
			return
		}
		httpx.OK(c, 200, v)
		return
	}
	v, err := a.Store.ListAudit(c, limit)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取审计日志失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminAdjustWallet 手工调账。金额可正可负，必须写原因。
func (a *App) adminAdjustWallet(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		UserID      string `json:"user_id"`
		Currency    string `json:"currency"`
		AmountCents int64  `json:"amount_cents"`
		Reason      string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.AmountCents == 0 {
		httpx.Fail(c, 400, "INVALID_AMOUNT", "调账金额不能为 0")
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		httpx.Fail(c, 400, "REASON_REQUIRED", "必须填写调账原因")
		return
	}
	// user_id 既接受 UUID 也接受纯数字 UID：后台不必让管理员抄 UUID。
	// user_id 既接受 UUID 也接受纯数字 UID：后台不必让管理员抄 UUID。
	u, err := a.Store.GetUserByIDOrPublicID(c, in.UserID)
	if err != nil {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "CNY"
	}
	if err := a.Store.AdjustWallet(c, p.User.ID, u.ID, currency, in.AmountCents, strings.TrimSpace(in.Reason), c.GetString("request_id")); err != nil {
		httpx.Fail(c, 400, "WALLET_ADJUST_FAILED", err.Error())
		return
	}
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

var _ = net.ParseIP
var _ = events.UserLogin
var _ = time.Now
