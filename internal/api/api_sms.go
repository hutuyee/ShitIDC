package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/sms"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 短信验证码（对应魔方 public/plugins/sms/）。
//
// 与邮箱验证码同构：只存哈希、按 (手机号, 用途) 限频、错 5 次锁定、10 分钟过期。
// 额外做了一层风控——短信是花钱的，所以按手机号统计最近发送量，超了就拒绝。

const (
	smsCodeTTL      = 10 * time.Minute
	smsResendWindow = 60 * time.Second
	// 单号风控阈值：1 分钟 1 条、1 小时 5 条、1 天 10 条。
	smsMaxPerMinute = 1
	smsMaxPerHour   = 5
	smsMaxPerDay    = 10
)

// sendSMSCode 发送短信验证码。默认用于注册；登录/绑定等场景通过 purpose 区分。
func (a *App) sendSMSCode(c *gin.Context) {
	var in struct {
		Phone      string `json:"phone"`
		Purpose    string `json:"purpose"`
		CaptchaID  string `json:"captcha_id"`
		CaptchaAns string `json:"captcha_answer"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if !a.verifyCaptcha(c, in.CaptchaID, in.CaptchaAns) {
		return
	}
	phone := normalizeSMSCode(in.Phone)
	if phone == "" {
		httpx.Fail(c, 400, "INVALID_PHONE", "手机号格式错误")
		return
	}
	purpose := strings.TrimSpace(in.Purpose)
	if purpose == "" {
		purpose = "register"
	}
	switch purpose {
	case "register", "login", "bind", "reset":
	default:
		httpx.Fail(c, 400, "INVALID_REQUEST", "不支持的验证码用途")
		return
	}

	// 风控：先看这个号码最近的发送量，避免被刷成"短信轰炸"的跳板。
	stats, err := a.Store.SmsSendStats(c, phone)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取发送记录失败")
		return
	}
	if stats.LastMinute >= smsMaxPerMinute || stats.LastHour >= smsMaxPerHour || stats.LastDay >= smsMaxPerDay {
		a.Store.RecordSmsMessage(c, phone, purpose, "", "", false, "rate limited", 0, clientIP(c))
		httpx.Fail(c, 429, "SMS_RATE_LIMITED", "该手机号发送过于频繁，请稍后再试")
		return
	}

	provider, secretEnc, err := a.Store.ActiveSmsProvider(c)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 503, "SMS_NOT_CONFIGURED", "短信服务未配置，请联系管理员")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取短信配置失败")
		return
	}
	impl, ok := sms.Get(provider.Provider)
	if !ok {
		httpx.Fail(c, 503, "SMS_PROVIDER_UNKNOWN", "未知的短信通道："+provider.Provider)
		return
	}
	secret, err := a.smsSecret(secretEnc)
	if err != nil {
		httpx.Fail(c, 500, "SMS_SECRET_INVALID", err.Error())
		return
	}
	cfg := sms.Config{Provider: provider.Provider, Fields: provider.Config}
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 503, "SMS_PROVIDER_INVALID", err.Error())
		return
	}

	code, err := security.NumericCode(6)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成验证码失败")
		return
	}
	// 先入库再发送：如果发送失败就把刚写的验证码撤掉，避免占着限频窗口。
	if err := a.Store.PutSmsCode(c, phone, purpose, security.SHA256Hex(code), smsCodeTTL, smsResendWindow); err != nil {
		if errors.Is(err, store.ErrCodeRateLimited) {
			httpx.Fail(c, 429, "CODE_RATE_LIMITED", "发送太频繁，请 1 分钟后再试")
			return
		}
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存验证码失败")
		return
	}
	sendCtx, cancel := context.WithTimeout(c, 15*time.Second)
	defer cancel()
	err = impl.Send(sendCtx, cfg, secret, sms.Message{
		Phone: phone, Code: code, Purpose: purpose, TTLMinutes: int(smsCodeTTL.Minutes()),
	})
	a.Store.TouchSmsProvider(c, provider.PublicID, err == nil, errText(err))
	a.Store.RecordSmsMessage(c, phone, purpose, provider.Name, provider.Provider, err == nil, errText(err), 0, clientIP(c))
	if err != nil {
		httpx.Fail(c, 502, "SMS_SEND_FAILED", "短信发送失败："+err.Error())
		return
	}
	httpx.OK(c, 200, map[string]any{"ok": true, "ttl_seconds": int(smsCodeTTL.Seconds())})
}

// verifySMSCode 校验短信验证码（用于注册、登录、绑定手机号）。
func (a *App) verifySMSCode(c *gin.Context) {
	var in struct {
		Phone   string `json:"phone"`
		Code    string `json:"code"`
		Purpose string `json:"purpose"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	phone := normalizeSMSCode(in.Phone)
	purpose := strings.TrimSpace(in.Purpose)
	if purpose == "" {
		purpose = "register"
	}
	if err := a.Store.ConsumeSmsCode(c, phone, purpose, security.SHA256Hex(strings.TrimSpace(in.Code))); err != nil {
		httpx.Fail(c, 400, mapCodeError(err), "验证码错误或已过期")
		return
	}
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// smsSecret 解密通道凭据（JSON）。
func (a *App) smsSecret(enc string) (sms.Secret, error) {
	if strings.TrimSpace(enc) == "" {
		return sms.Secret{}, nil
	}
	if len(a.Cfg.MasterKey) == 0 {
		return nil, errors.New("服务器未配置 MASTER_KEY_BASE64，无法解密短信凭据")
	}
	plain, err := security.Decrypt(a.Cfg.MasterKey, enc)
	if err != nil {
		return nil, errors.New("解密短信凭据失败")
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, errors.New("短信凭据不是合法 JSON")
	}
	return sms.Secret(out), nil
}

// normalizeSMSCode 规范手机号：只保留数字，并要求长度合理。
func normalizeSMSCode(phone string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(phone) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) < 6 || len(out) > 20 {
		return ""
	}
	return out
}

// errText 把可能为 nil 的错误转成字符串。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- 绑定手机号（短信验证码 purpose=bind 的落地处）----

// bindPhone 用短信验证码把手机号绑到当前账号。
// 验证码消费与绑定在同一请求内完成：验证码错误时绑定不发生。
func (a *App) bindPhone(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	phone := normalizeSMSCode(in.Phone)
	if phone == "" {
		httpx.Fail(c, 400, "INVALID_PHONE", "手机号格式错误")
		return
	}
	if err := a.Store.ConsumeSmsCode(c, phone, "bind", security.SHA256Hex(strings.TrimSpace(in.Code))); err != nil {
		httpx.Fail(c, 400, mapCodeError(err), "验证码错误或已过期")
		return
	}
	if err := a.Store.SetUserPhone(c, pr.User.ID, phone, true); err != nil {
		// 唯一索引冲突（手机号已被其它账号绑定）会带出人话提示。
		httpx.Fail(c, 400, "PHONE_BIND_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "profile.phone_bind", "user", fmt.Sprintf("%d", pr.User.ID), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]any{"ok": true, "phone": phone})
}

// unbindPhone 解绑手机号。解绑是自由的——号码换主人时用户不该等管理员。
func (a *App) unbindPhone(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.SetUserPhone(c, pr.User.ID, "", false); err != nil {
		httpx.Fail(c, 400, "PHONE_UNBIND_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "profile.phone_unbind", "user", fmt.Sprintf("%d", pr.User.ID), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- 管理端：短信通道 ----

// adminListSmsProviders 列出所有短信通道，并带上可用的通道实现标识。
func (a *App) adminListSmsProviders(c *gin.Context) {
	items, err := a.Store.ListSmsProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "SMS_PROVIDERS_FAILED", "读取短信通道失败")
		return
	}
	httpx.OK(c, 200, map[string]any{
		"providers": items,
		"available": sms.Names(),
	})
}

// adminCreateSmsProvider 新增短信通道。
// secret 是通道凭据 JSON（如 {"access_key_id":"..","access_key_secret":".."}）。
func (a *App) adminCreateSmsProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Name      string            `json:"name"`
		Provider  string            `json:"provider"`
		Config    map[string]string `json:"config"`
		Secret    map[string]string `json:"secret"`
		IsDefault bool              `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if in.Name == "" || in.Provider == "" {
		httpx.Fail(c, 400, "INVALID_SMS_PROVIDER", "通道名称与类型不能为空")
		return
	}
	impl, ok := sms.Get(in.Provider)
	if !ok {
		httpx.Fail(c, 400, "SMS_PROVIDER_UNKNOWN", "未知的短信通道："+in.Provider)
		return
	}
	cfg := sms.Config{Provider: in.Provider, Fields: in.Config}
	secret := sms.Secret(in.Secret)
	// 保存前先做一次静态校验：少填字段比发不出去更早暴露更好。
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 400, "SMS_PROVIDER_INVALID", err.Error())
		return
	}
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		httpx.Fail(c, 400, "SMS_PROVIDER_INVALID", "凭据格式错误")
		return
	}
	enc := ""
	if len(secret) > 0 {
		if len(a.Cfg.MasterKey) == 0 {
			httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法加密短信凭据")
			return
		}
		enc, err = security.Encrypt(a.Cfg.MasterKey, string(secretJSON))
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密短信凭据失败")
			return
		}
	}
	v, err := a.Store.CreateSmsProvider(c, in.Name, in.Provider, in.Config, enc, in.IsDefault)
	if err != nil {
		httpx.Fail(c, 400, "SMS_PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "sms_provider.create", "sms_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "provider": v.Provider})
	httpx.OK(c, 201, v)
}

// adminSetDefaultSmsProvider 把某个通道设为默认。
func (a *App) adminSetDefaultSmsProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.SetSmsProviderDefault(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "SMS_PROVIDER_NOT_FOUND", "短信通道不存在")
			return
		}
		httpx.Fail(c, 400, "SMS_PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "sms_provider.set_default", "sms_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteSmsProvider 删除通道。
func (a *App) adminDeleteSmsProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteSmsProvider(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "SMS_PROVIDER_NOT_FOUND", "短信通道不存在")
			return
		}
		httpx.Fail(c, 400, "SMS_PROVIDER_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "sms_provider.delete", "sms_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminListSmsMessages 查看发送流水——短信是花钱的，这是排查盗刷的主要依据。
func (a *App) adminListSmsMessages(c *gin.Context) {
	items, err := a.Store.ListSmsMessages(c, c.Query("phone"), parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "SMS_MESSAGES_FAILED", "读取发送记录失败")
		return
	}
	httpx.OK(c, 200, items)
}
