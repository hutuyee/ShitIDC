package api

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/payment"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// Handlers and middleware for the MVP gap fill: captcha (§9), device
// management (§8), 2FA (§9), product groups (第五阶段), announcements,
// invoice/service detail, API request logs (§5.6) and the payment method
// registry listing.

// ---- API request logging (§5.6 api_logs) ----

// apiLogger records one row per /api request through a bounded async queue:
// the request path never waits on the log write, and overflowing drops logs
// instead of blocking traffic.
func (a *App) apiLogger() gin.HandlerFunc {
	ch := make(chan store.APILogEntry, 1024)
	go a.drainAPILogs(ch)
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api/") {
			return
		}
		entry := store.APILogEntry{
			Method:     c.Request.Method,
			Path:       path,
			Status:     c.Writer.Status(),
			RequestID:  c.GetString("request_id"),
			IP:         clientIP(c),
			UserAgent:  c.Request.UserAgent(),
			DurationMs: int(time.Since(start).Milliseconds()),
		}
		if code, ok := c.Get("error_code"); ok {
			entry.ErrorCode, _ = code.(string)
		}
		if p, ok := getPrincipal(c); ok {
			entry.UserID = p.User.ID
			entry.APIToken = p.APIToken
		}
		select {
		case ch <- entry:
		default: // queue full: drop the log, never the request
		}
	}
}

func (a *App) drainAPILogs(ch <-chan store.APILogEntry) {
	batch := make([]store.APILogEntry, 0, 64)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := a.Store.InsertAPILogs(ctx, batch); err != nil {
			// Logging must never crash the server; dropping is acceptable.
			_ = err
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= 64 {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func (a *App) adminListAPILogs(c *gin.Context) {
	uid, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	v, err := a.Store.ListAPILogs(c, strings.ToUpper(strings.TrimSpace(c.Query("method"))), strings.TrimSpace(c.Query("path")), parseIntDefault(c.Query("status_min"), 0), uid, parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取 API 日志失败")
		return
	}
	httpx.OK(c, 200, v)
}

// ---- sessions / device management (§8) ----

func (a *App) listSessions(c *gin.Context) {
	p, _ := getPrincipal(c)
	tokenHash := ""
	if cookie, err := c.Cookie("shitidc_session"); err == nil {
		tokenHash = security.SHA256Hex(cookie)
	}
	v, err := a.Store.ListSessions(c, p.User.ID, tokenHash)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取登录设备失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) revokeSession(c *gin.Context) {
	p, _ := getPrincipal(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "会话 ID 无效")
		return
	}
	if err := a.Store.RevokeSessionByID(c, p.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "SESSION_NOT_FOUND", "会话不存在或已下线")
			return
		}
		httpx.Fail(c, 500, "INTERNAL_ERROR", "吊销会话失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "session.revoke", "session", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) revokeOtherSessions(c *gin.Context) {
	p, _ := getPrincipal(c)
	cookie, err := c.Cookie("shitidc_session")
	if err != nil || cookie == "" {
		httpx.Fail(c, 401, "UNAUTHORIZED", "请先登录")
		return
	}
	n, err := a.Store.RevokeOtherSessions(c, p.User.ID, security.SHA256Hex(cookie))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "吊销其他会话失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "session.revoke_others", "session", p.User.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"revoked": n})
	httpx.OK(c, 200, map[string]any{"ok": true, "revoked": n})
}

// ---- TOTP 2FA (§9 可选 2FA) ----

func (a *App) totpSetup(c *gin.Context) {
	p, _ := getPrincipal(c)
	if len(a.Cfg.MasterKey) != 32 {
		httpx.Fail(c, 503, "MASTER_KEY_REQUIRED", "需要配置 MASTER_KEY_BASE64 才能启用两步验证")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	creds, err := a.Store.GetLoginCredentials(c, p.User.Email)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账户失败")
		return
	}
	if creds.TOTPEnabled {
		httpx.Fail(c, 409, "TOTP_ALREADY_ENABLED", "两步验证已开启，如需重置请先关闭")
		return
	}
	if !security.VerifyPassword(creds.PasswordHash, in.Password) {
		httpx.Fail(c, 403, "WRONG_PASSWORD", "密码不正确")
		return
	}
	secret, err := security.GenerateTOTPSecret()
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成密钥失败")
		return
	}
	enc, err := security.Encrypt(a.Cfg.MasterKey, secret)
	if err != nil {
		httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密两步验证密钥失败")
		return
	}
	if err := a.Store.SaveTOTPPending(c, p.User.ID, enc); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存两步验证密钥失败")
		return
	}
	httpx.OK(c, 200, map[string]string{
		"secret":      secret,
		"otpauth_uri": security.TOTPProvisioningURI(secret, "ShitIDC", p.User.Email),
	})
}

func (a *App) totpEnable(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	creds, err := a.Store.GetLoginCredentials(c, p.User.Email)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账户失败")
		return
	}
	if creds.TOTPSecretEnc == "" || len(a.Cfg.MasterKey) != 32 {
		httpx.Fail(c, 400, "TOTP_NOT_SETUP", "请先完成两步验证设置")
		return
	}
	secret, err := security.Decrypt(a.Cfg.MasterKey, creds.TOTPSecretEnc)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "解密两步验证密钥失败")
		return
	}
	if !security.VerifyTOTP(secret, in.Code) {
		httpx.Fail(c, 400, "TOTP_INVALID", "验证码错误，请确认认证器时间后重试")
		return
	}
	if err := a.Store.EnableTOTP(c, p.User.ID); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "开启两步验证失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "auth.2fa.enable", "user", p.User.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	_ = a.Store.SecurityEvent(c, p.User.ID, "auth.2fa.enable", "notice", clientIP(c), nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) totpDisable(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	creds, err := a.Store.GetLoginCredentials(c, p.User.Email)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账户失败")
		return
	}
	if !creds.TOTPEnabled {
		httpx.Fail(c, 409, "TOTP_NOT_ENABLED", "两步验证未开启")
		return
	}
	if !security.VerifyPassword(creds.PasswordHash, in.Password) {
		httpx.Fail(c, 403, "WRONG_PASSWORD", "密码不正确")
		return
	}
	secret, derr := security.Decrypt(a.Cfg.MasterKey, creds.TOTPSecretEnc)
	if derr != nil || !security.VerifyTOTP(secret, in.Code) {
		httpx.Fail(c, 400, "TOTP_INVALID", "验证码错误")
		return
	}
	if err := a.Store.DisableTOTP(c, p.User.ID); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "关闭两步验证失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "auth.2fa.disable", "user", p.User.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	_ = a.Store.SecurityEvent(c, p.User.ID, "auth.2fa.disable", "warning", clientIP(c), nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- product groups (第五阶段) ----

// productPrices lists every purchasable billing cycle of a product so the
// storefront can offer monthly / quarterly / yearly switches.
func (a *App) productPrices(c *gin.Context) {
	v, err := a.Store.ListProductPrices(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取产品价格失败")
		return
	}
	httpx.OK(c, 200, v)
}

// totpStatus tells the profile page whether the second factor is on.
func (a *App) totpStatus(c *gin.Context) {
	p, _ := getPrincipal(c)
	enabled, err := a.Store.TOTPState(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取两步验证状态失败")
		return
	}
	httpx.OK(c, 200, map[string]bool{"enabled": enabled})
}

func (a *App) listProductGroups(c *gin.Context) {
	v, err := a.Store.ListProductGroups(c, true)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取产品分组失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminCreateProductGroup(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name       string `json:"name"`
		SortWeight int    `json:"sort_weight"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "分组名称不能为空")
		return
	}
	v, err := a.Store.CreateProductGroup(c, in.Name, in.SortWeight)
	if err != nil {
		httpx.Fail(c, 400, "GROUP_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "product_group.create", "product_group", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateProductGroup(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name       string `json:"name"`
		SortWeight int    `json:"sort_weight"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "分组名称不能为空")
		return
	}
	v, err := a.Store.UpdateProductGroup(c, c.Param("id"), in.Name, in.SortWeight)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "GROUP_NOT_FOUND", "分组不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "GROUP_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "product_group.update", "product_group", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteProductGroup(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteProductGroup(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "GROUP_NOT_FOUND", "分组不存在")
			return
		}
		httpx.Fail(c, 500, "GROUP_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "product_group.delete", "product_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- product administration (edit / list / shelf) ----

func (a *App) adminListProducts(c *gin.Context) {
	v, err := a.Store.ListProductsAdmin(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取产品失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminUpdateProduct(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		GroupID     *string `json:"group_id"` // null: unchanged, "": clear
		SortWeight  *int    `json:"sort_weight"`
		Active      *bool   `json:"active"`
		AmountCents *int64  `json:"amount_cents"`
		// 计费模型（魔方 pay_type）：recurring / onetime / free / trial
		PayType           string `json:"pay_type"`
		TrialDays         *int   `json:"trial_days"`
		TrialPriceCents   *int64 `json:"trial_price_cents"`
		AutoTerminateDays *int   `json:"auto_terminate_days"`
		// 多周期价格：提交后整体覆盖（未提交的周期下架，不影响已有订单）
		Prices []struct {
			BillingCycle string `json:"billing_cycle"`
			AmountCents  int64  `json:"amount_cents"`
			// Currency 允许逐行指定币种（多币种独立定价）；留空则用顶层的 currency。
			Currency string `json:"currency"`
		} `json:"prices"`
		Currency string `json:"currency"`
		// 库存与限购
		StockControl   *bool `json:"stock_control"`
		StockQty       *int  `json:"stock_qty"`
		AllowQty       *bool `json:"allow_qty"`
		MaxPerCustomer *int  `json:"max_per_customer"`
		IsFeatured     *bool `json:"is_featured"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	// 计费配置与多周期价格单独走一条路径，避免把主更新语句撑得过于复杂。
	if strings.TrimSpace(in.PayType) != "" || in.TrialDays != nil || in.Prices != nil ||
		in.StockControl != nil || in.StockQty != nil || in.AllowQty != nil || in.MaxPerCustomer != nil || in.IsFeatured != nil {
		billing := store.ProductBillingInput{
			PayType:           in.PayType,
			TrialDays:         derefInt(in.TrialDays),
			TrialPriceCents:   derefInt64(in.TrialPriceCents),
			AutoTerminateDays: derefInt(in.AutoTerminateDays),
			StockControl:      derefBool(in.StockControl),
			StockQty:          derefInt(in.StockQty),
			AllowQty:          in.AllowQty == nil || *in.AllowQty,
			MaxPerCustomer:    derefInt(in.MaxPerCustomer),
			IsFeatured:        derefBool(in.IsFeatured),
		}
		if err := a.Store.UpdateProductBilling(c, c.Param("id"), billing); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "产品不存在")
				return
			}
			httpx.Fail(c, 400, "PRODUCT_BILLING_FAILED", err.Error())
			return
		}
		if len(in.Prices) > 0 {
			// 按币种分组：每个币种单独调用一次，未提交的周期在该币种内下架，
			// 其它币种的价格不受影响。
			byCurrency := map[string][]store.PriceInput{}
			defaultCurrency := strings.ToUpper(strings.TrimSpace(in.Currency))
			if defaultCurrency == "" {
				defaultCurrency = "CNY"
			}
			for _, pr := range in.Prices {
				cur := strings.ToUpper(strings.TrimSpace(pr.Currency))
				if cur == "" {
					cur = defaultCurrency
				}
				byCurrency[cur] = append(byCurrency[cur], store.PriceInput{BillingCycle: pr.BillingCycle, AmountCents: pr.AmountCents})
			}
			for cur, prices := range byCurrency {
				if err := a.Store.SetProductPrices(c, c.Param("id"), cur, prices); err != nil {
					httpx.Fail(c, 400, "PRODUCT_PRICE_FAILED", err.Error())
					return
				}
			}
		}
	}
	v, err := a.Store.UpdateProduct(c, c.Param("id"), strings.TrimSpace(in.Name), in.Description, in.GroupID, in.SortWeight, in.Active, in.AmountCents)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "产品不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "product.update", "product", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 200, v)
}

// ---- announcements ----

// listAnnouncements 返回对用户可见的公告。
// limit 可由查询串放大（列表页一次要展示更多条），上限 100。
func (a *App) listAnnouncements(c *gin.Context) {
	v, err := a.Store.ListAnnouncements(c, true, parseIntDefault(c.Query("limit"), 20))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取公告失败")
		return
	}
	httpx.OK(c, 200, v)
}

// announcementDetail 读一条公告的完整内容，供 /announcements/:id 详情页使用。
func (a *App) announcementDetail(c *gin.Context) {
	v, err := a.Store.GetAnnouncement(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "ANNOUNCEMENT_NOT_FOUND", "公告不存在或已下线")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取公告失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminListAnnouncements(c *gin.Context) {
	v, err := a.Store.ListAnnouncements(c, false, 100)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取公告失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminCreateAnnouncement(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Active *bool  `json:"active"`
		Pinned bool   `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "公告标题不能为空")
		return
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	v, err := a.Store.CreateAnnouncement(c, in.Title, in.Body, active, in.Pinned)
	if err != nil {
		httpx.Fail(c, 500, "ANNOUNCEMENT_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "announcement.create", "announcement", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateAnnouncement(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Active bool   `json:"active"`
		Pinned bool   `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "公告标题不能为空")
		return
	}
	v, err := a.Store.UpdateAnnouncement(c, c.Param("id"), in.Title, in.Body, in.Active, in.Pinned)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "ANNOUNCEMENT_NOT_FOUND", "公告不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "ANNOUNCEMENT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "announcement.update", "announcement", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteAnnouncement(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteAnnouncement(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "ANNOUNCEMENT_NOT_FOUND", "公告不存在")
			return
		}
		httpx.Fail(c, 500, "ANNOUNCEMENT_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "announcement.delete", "announcement", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- invoice / service detail ----

func (a *App) invoiceDetail(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.GetInvoiceDetail(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_NOT_FOUND", "账单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账单失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) serviceDetail(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.GetServiceDetail(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) && p.Permissions["service.manage"] {
		// Administrators may inspect any service (§10 object-level fallback).
		v, err = a.Store.GetServiceDetail(c, 0, c.Param("id"))
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取服务失败")
		return
	}
	httpx.OK(c, 200, v)
}

// ---- payment methods registry ----

func (a *App) listSupportedPaymentMethods(c *gin.Context) {
	httpx.OK(c, 200, map[string]any{"methods": payment.Methods()})
}

// 指针解引用助手：管理端接口用 *T 区分“不提交”与“提交零值”。
func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefBool(v *bool) bool {
	if v == nil {
		return false
	}
	return *v
}
