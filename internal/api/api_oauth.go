package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/oauth"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 第三方登录（对应魔方 public/plugins/oauth/）。
//
// 流程：
//   1. GET /auth/oauth/:provider/start  -> 302 到平台授权页（带一次性 state）
//   2. 平台回调 GET /auth/oauth/:provider/callback?code=&state=
//   3. 校验并消费 state，用 code 换资料，找绑定 / 建号 / 要求绑定
//
// state 存在库里且用一次就删：防 CSRF，也防重放。

const oauthStateTTL = 10 * time.Minute

// listOAuthProviders 返回可用的第三方登录通道（登录页展示按钮用）。
func (a *App) listOAuthProviders(c *gin.Context) {
	items, err := a.Store.ActiveOAuthProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "OAUTH_LIST_FAILED", "读取第三方登录配置失败")
		return
	}
	// 只回传前端需要的字段，不暴露配置细节。
	out := make([]map[string]any, 0, len(items))
	for _, v := range items {
		out = append(out, map[string]any{"provider": v.Provider, "name": v.Name})
	}
	httpx.OK(c, 200, out)
}

// publicBaseURL 返回站点公开地址；没配时按请求推断，方便本地调试。
func (a *App) publicBaseURL(c *gin.Context) string {
	base := strings.TrimRight(a.Cfg.PublicBaseURL, "/")
	if base == "" {
		scheme := "http"
		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		base = scheme + "://" + c.Request.Host
	}
	return base
}

// oauthCallbackURL 拼出某个通道的回调地址（必须与平台上登记的一致）。
func (a *App) oauthCallbackURL(c *gin.Context, provider string) string {
	return a.publicBaseURL(c) + "/api/v1/auth/oauth/" + url.PathEscape(provider) + "/callback"
}

// oauthStart 发起第三方授权。
func (a *App) oauthStart(c *gin.Context) {
	providerName := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	impl, ok := oauth.Get(providerName)
	if !ok {
		httpx.Fail(c, 404, "OAUTH_PROVIDER_UNKNOWN", "不支持的第三方登录通道")
		return
	}
	row, secretEnc, err := a.Store.GetOAuthProviderSecret(c, providerName)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "OAUTH_PROVIDER_DISABLED", "该第三方登录通道未启用")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取第三方登录配置失败")
		return
	}
	secret, err := a.oauthSecret(secretEnc)
	if err != nil {
		httpx.Fail(c, 500, "OAUTH_SECRET_INVALID", err.Error())
		return
	}
	cfg := oauth.Config{Provider: providerName, Fields: row.Config}
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 503, "OAUTH_PROVIDER_INVALID", err.Error())
		return
	}
	state, err := security.RandomToken(24)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成 state 失败")
		return
	}
	// 已登录用户发起的授权是「绑定」，未登录是「登录/注册」。
	// getPrincipal 在未登录时会返回零值 principal，所以用 User.ID 判断而不是 ok。
	var bindUserID int64
	if pr, _ := getPrincipal(c); pr.User.ID > 0 {
		bindUserID = pr.User.ID
	}
	// 回调后要跳回的站内路径：必须过滤，否则就是开放重定向。
	redirectTo := oauth.SafeRedirectTo(c.Query("redirect_to"))
	if err := a.Store.PutOAuthState(c, state, providerName, redirectTo, bindUserID); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存 state 失败")
		return
	}
	authorizeURL, err := impl.AuthorizeURL(cfg, secret, oauth.AuthorizeParams{
		RedirectURI: a.oauthCallbackURL(c, providerName),
		State:       state,
	})
	if err != nil {
		httpx.Fail(c, 503, "OAUTH_AUTHORIZE_FAILED", err.Error())
		return
	}
	// 浏览器直接跳转。
	c.Redirect(http.StatusFound, authorizeURL)
}

// oauthCallback 处理平台回调。
func (a *App) oauthCallback(c *gin.Context) {
	providerName := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	// 平台可能带回自己的错误（用户点了取消）。
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		a.oauthFail(c, providerName, "", "授权被拒绝："+firstNonEmptyString(c.Query("error_description"), e))
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	// 支付宝回调带的是 auth_code，语义与 code 相同。
	if code == "" {
		code = strings.TrimSpace(c.Query("auth_code"))
	}
	// 钉钉新版 OAuth2 回调带的是 authCode。
	if code == "" {
		code = strings.TrimSpace(c.Query("authCode"))
	}
	state := strings.TrimSpace(c.Query("state"))
	if code == "" || state == "" {
		a.oauthFail(c, providerName, "", "回调缺少 code 或 state")
		return
	}
	// state 一次性消费：取出即删除，重放立刻失败。
	stateRow, err := a.Store.ConsumeOAuthState(c, state, oauthStateTTL)
	if err != nil {
		a.oauthFail(c, providerName, "", "登录状态已失效，请重新发起授权")
		return
	}
	// state 必须属于这个通道，防止拿 A 通道的 state 走 B 通道。
	if stateRow.Provider != providerName {
		a.oauthFail(c, providerName, "", "登录状态与通道不匹配")
		return
	}
	impl, ok := oauth.Get(providerName)
	if !ok {
		a.oauthFail(c, providerName, "", "不支持的第三方登录通道")
		return
	}
	row, secretEnc, err := a.Store.GetOAuthProviderSecret(c, providerName)
	if err != nil {
		a.oauthFail(c, providerName, "", "第三方登录通道不可用")
		return
	}
	secret, err := a.oauthSecret(secretEnc)
	if err != nil {
		a.oauthFail(c, providerName, "", err.Error())
		return
	}
	cfg := oauth.Config{Provider: providerName, Fields: row.Config}
	exchangeCtx, cancel := context.WithTimeout(c, 20*time.Second)
	var identity oauth.Identity
	if sp, ok := impl.(oauth.SuiteTicketProvider); ok {
		// 企业微信服务商应用：suite_ticket 由「指令回调 URL」推送入库。
		ticket, terr := a.Store.GetOAuthSuiteTicket(exchangeCtx, cfg.Field("suite_id"))
		if terr != nil {
			cancel()
			a.Store.MarkOAuthProviderHealth(c, providerName, false, "尚未收到 suite_ticket 推送")
			a.oauthFail(c, providerName, stateRow.RedirectTo, "企业微信尚未推送 suite_ticket：请先在服务商后台把「指令回调URL」配置为 "+a.oauthQyweixinReceiveURL(c))
			return
		}
		identity, err = sp.ExchangeWithSuiteTicket(exchangeCtx, cfg, secret, code, a.oauthCallbackURL(c, providerName), ticket)
	} else {
		identity, err = impl.Exchange(exchangeCtx, cfg, secret, code, a.oauthCallbackURL(c, providerName))
	}
	cancel()
	if err != nil {
		a.Store.MarkOAuthProviderHealth(c, providerName, false, err.Error())
		a.oauthFail(c, providerName, stateRow.RedirectTo, "获取第三方账号信息失败")
		return
	}
	a.Store.MarkOAuthProviderHealth(c, providerName, true, "")

	// 情况一：已登录用户在绑定新通道。
	if stateRow.UserID != nil && *stateRow.UserID > 0 {
		if err := a.Store.BindOAuthIdentity(c, *stateRow.UserID, store.IdentityInput{
			Provider: identity.Provider, Subject: identity.Subject, UnionID: identity.UnionID,
			Nickname: identity.Nickname, AvatarURL: identity.AvatarURL, Email: identity.Email, Raw: identity.Raw,
		}); err != nil {
			a.oauthFail(c, providerName, stateRow.RedirectTo, err.Error())
			return
		}
		_ = a.Store.Audit(c, *stateRow.UserID, "oauth.bind", "oauth_identity", identity.Provider+":"+identity.Subject, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
		c.Redirect(http.StatusFound, stateRow.RedirectTo)
		return
	}

	// 情况二：登录。先找绑定。
	userID, err := a.Store.FindUserByIdentity(c, identity.Provider, identity.Subject, identity.UnionID)
	if err == nil {
		a.oauthLogin(c, providerName, userID, stateRow.RedirectTo)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		a.oauthFail(c, providerName, stateRow.RedirectTo, "查询绑定失败")
		return
	}
	// 情况三：没绑过。不允许自动注册就直接拒。
	if !row.AllowRegister {
		a.oauthFail(c, providerName, stateRow.RedirectTo, "该通道未开放自动注册，请先用邮箱注册后在个人中心绑定")
		return
	}
	newUserID, err := a.Store.CreateOAuthUser(c, identity.Email, identity.Nickname, identity.Email != "")
	if err != nil {
		// 邮箱已存在等情况：提示去绑定，而不是静默登录别人的账号。
		a.oauthFail(c, providerName, stateRow.RedirectTo, err.Error())
		return
	}
	if err := a.Store.BindOAuthIdentity(c, newUserID, store.IdentityInput{
		Provider: identity.Provider, Subject: identity.Subject, UnionID: identity.UnionID,
		Nickname: identity.Nickname, AvatarURL: identity.AvatarURL, Email: identity.Email, Raw: identity.Raw,
	}); err != nil {
		a.oauthFail(c, providerName, stateRow.RedirectTo, err.Error())
		return
	}
	_ = a.Store.Audit(c, newUserID, "oauth.register", "user", identity.Provider+":"+identity.Subject, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	a.oauthLogin(c, providerName, newUserID, stateRow.RedirectTo)
}

// oauthLogin 给第三方登录成功的用户建立会话并跳回站内。
func (a *App) oauthLogin(c *gin.Context, providerName string, userID int64, redirectTo string) {
	token, _ := security.RandomToken(32)
	csrf, _ := security.RandomToken(24)
	expires := time.Now().Add(a.Cfg.SessionTTL)
	if err := a.Store.CreateSession(c, userID, security.SHA256Hex(token), csrf, clientIPAddr(c), c.Request.UserAgent(), expires); err != nil {
		a.oauthFail(c, providerName, redirectTo, "创建会话失败")
		return
	}
	_ = a.Store.RecordLoginAttempt(c, userID, "", true, "oauth:"+providerName, clientIP(c), c.Request.UserAgent())
	_ = a.Store.Audit(c, userID, "auth.login_oauth", "user", providerName, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	a.Bus.Emit(a.eventCtx(c), events.UserLogin, map[string]any{"uid": userID, "provider": providerName, "ip": clientIP(c)})
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("shitidc_session", token, int(a.Cfg.SessionTTL.Seconds()), "/", a.Cfg.CookieDomain, a.Cfg.CookieSecure, true)
	c.Redirect(http.StatusFound, oauth.SafeRedirectTo(redirectTo))
}

// oauthFail 把失败原因带回前端。用 query 参数而不是 JSON：
// 这是一次浏览器跳转，不是 API 调用，前端页面读 query 展示提示。
func (a *App) oauthFail(c *gin.Context, providerName, redirectTo, message string) {
	target := oauth.SafeRedirectTo(redirectTo)
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	c.Redirect(http.StatusFound, target+sep+"oauth_error="+url.QueryEscape(message)+"&oauth_provider="+url.QueryEscape(providerName))
}

// oauthSecret 解密通道凭据。
func (a *App) oauthSecret(enc string) (oauth.Secret, error) {
	if strings.TrimSpace(enc) == "" {
		return oauth.Secret{}, nil
	}
	if len(a.Cfg.MasterKey) == 0 {
		return nil, errors.New("服务器未配置 MASTER_KEY_BASE64，无法解密第三方登录凭据")
	}
	plain, err := security.Decrypt(a.Cfg.MasterKey, enc)
	if err != nil {
		return nil, errors.New("解密第三方登录凭据失败")
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, errors.New("第三方登录凭据不是合法 JSON")
	}
	return oauth.Secret(out), nil
}

// oauthQyweixinReceiveURL 企业微信「指令回调 URL」。
func (a *App) oauthQyweixinReceiveURL(c *gin.Context) string {
	return a.publicBaseURL(c) + "/api/v1/auth/qyweixin/receive"
}

// oauthQyweixinReceive 接收企业微信「指令回调」：
// GET 是保存回调 URL 时的校验（解密 echostr 原样吐回），
// POST 是 suite_ticket 推送（解密后入库，回 "success"）。
// 该端点无需登录，安全性由 token 签名 + AES 解密保证。
func (a *App) oauthQyweixinReceive(c *gin.Context) {
	row, secretEnc, err := a.Store.GetOAuthProviderSecret(c, "qyweixin")
	if err != nil {
		c.String(http.StatusNotFound, "qyweixin provider not configured")
		return
	}
	secret, err := a.oauthSecret(secretEnc)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	token, aesKey := secret.Get("token"), secret.Get("aes_key")
	if token == "" || aesKey == "" {
		c.String(http.StatusBadRequest, "qyweixin callback token/aes_key not configured")
		return
	}
	msgSignature := c.Query("msg_signature")
	timestamp := c.Query("timestamp")
	nonce := c.Query("nonce")
	if c.Request.Method == http.MethodGet {
		plain, verr := oauth.QyweixinVerifyURL(token, aesKey, msgSignature, timestamp, nonce, c.Query("echostr"))
		if verr != nil {
			c.String(http.StatusBadRequest, verr.Error())
			return
		}
		c.String(http.StatusOK, plain)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.String(http.StatusBadRequest, "read body failed")
		return
	}
	plain, derr := oauth.QyweixinDecryptMessage(token, aesKey, msgSignature, timestamp, nonce, string(body))
	if derr != nil {
		c.String(http.StatusBadRequest, derr.Error())
		return
	}
	if ticket := oauth.QyweixinParseSuiteTicket(plain); ticket != "" {
		if err := a.Store.SaveOAuthSuiteTicket(c, row.Config["suite_id"], ticket); err != nil {
			c.String(http.StatusInternalServerError, "save suite_ticket failed")
			return
		}
	}
	c.String(http.StatusOK, "success")
}

// clientIPAddr 把客户端 IP 解析成 net.IP（CreateSession 要这个类型）。
func clientIPAddr(c *gin.Context) net.IP {
	return net.ParseIP(clientIP(c))
}

// firstNonEmptyString 返回第一个非空字符串（本文件内的小工具）。
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---- 个人中心：绑定管理 ----

// myOAuthIdentities 列出当前用户已绑定的第三方身份。
func (a *App) myOAuthIdentities(c *gin.Context) {
	pr, _ := getPrincipal(c)
	ids, err := a.Store.ListOAuthIdentities(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "OAUTH_IDENTITY_LIST_FAILED", "读取绑定失败")
		return
	}
	hasPassword, err := a.Store.UserHasPassword(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账号状态失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"identities": ids, "has_password": hasPassword})
}

// unbindOAuthIdentity 解绑一个第三方身份。
func (a *App) unbindOAuthIdentity(c *gin.Context) {
	pr, _ := getPrincipal(c)
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if err := a.Store.UnbindOAuthIdentity(c, pr.User.ID, provider); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "OAUTH_IDENTITY_NOT_FOUND", "没有绑定该第三方账号")
			return
		}
		// 「唯一的登录方式」这类业务约束按 400 回，前端直接展示原因。
		httpx.Fail(c, 400, "OAUTH_UNBIND_REFUSED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oauth.unbind", "oauth_identity", provider, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- 管理端 ----

// adminListOAuthProviders 列出全部第三方登录通道。
func (a *App) adminListOAuthProviders(c *gin.Context) {
	items, err := a.Store.ListOAuthProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "OAUTH_LIST_FAILED", "读取第三方登录配置失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"providers": items, "available": oauth.Names()})
}

// adminSaveOAuthProvider 新增或更新一个第三方登录通道。
// client_secret 留空表示不改动已保存的密钥。
func (a *App) adminSaveOAuthProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Name          string            `json:"name"`
		Provider      string            `json:"provider"`
		Config        map[string]string `json:"config"`
		Secret        map[string]string `json:"secret"`
		AllowRegister bool              `json:"allow_register"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	impl, ok := oauth.Get(in.Provider)
	if !ok {
		httpx.Fail(c, 400, "OAUTH_PROVIDER_UNKNOWN", "不支持的第三方登录通道："+in.Provider)
		return
	}
	secret := oauth.Secret(in.Secret)
	// 保存前静态校验：少填字段比登录时才发现更好。
	// 密钥留空时用占位值跳过校验（表示沿用已存的密钥）。
	// 各通道的密钥键名都列在这里，避免某个通道更新配置时被自家校验卡住。
	if len(secret) == 0 {
		secret = oauth.Secret{
			"client_secret":   "unchanged",
			"app_secret":      "unchanged",
			"app_key":         "unchanged",
			"app_private_key": "unchanged",
			"secret":          "unchanged",
			"token":           "unchanged",
			"aes_key":         "unchanged",
		}
	}
	if err := impl.Validate(oauth.Config{Provider: in.Provider, Fields: in.Config}, secret); err != nil {
		httpx.Fail(c, 400, "OAUTH_PROVIDER_INVALID", err.Error())
		return
	}
	enc := ""
	if len(in.Secret) > 0 {
		if len(a.Cfg.MasterKey) == 0 {
			httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法加密第三方登录凭据")
			return
		}
		payload, err := json.Marshal(in.Secret)
		if err != nil {
			httpx.Fail(c, 400, "OAUTH_PROVIDER_INVALID", "凭据格式错误")
			return
		}
		enc, err = security.Encrypt(a.Cfg.MasterKey, string(payload))
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密凭据失败")
			return
		}
	}
	v, err := a.Store.UpsertOAuthProvider(c, in.Name, in.Provider, in.Config, enc, in.AllowRegister)
	if err != nil {
		httpx.Fail(c, 400, "OAUTH_PROVIDER_SAVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oauth_provider.save", "oauth_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"provider": v.Provider})
	httpx.OK(c, 200, v)
}

// adminToggleOAuthProvider 启用/停用一个通道。
func (a *App) adminToggleOAuthProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SetOAuthProviderActive(c, c.Param("id"), in.Active); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "OAUTH_PROVIDER_NOT_FOUND", "通道不存在")
			return
		}
		httpx.Fail(c, 400, "OAUTH_PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oauth_provider.toggle", "oauth_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteOAuthProvider 删除一个通道。
func (a *App) adminDeleteOAuthProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteOAuthProvider(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "OAUTH_PROVIDER_NOT_FOUND", "通道不存在")
			return
		}
		httpx.Fail(c, 400, "OAUTH_PROVIDER_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oauth_provider.delete", "oauth_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
