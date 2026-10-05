package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/mail"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 邮件通道解析（对应魔方 public/plugins/mail/）。
//
// 发送顺序是「启用中的通道优先，未配置通道时回退到内置 SMTP」：
// 老部署继续用 settings 里的 SMTP；配了通道就用通道。失败不静默换通道——
// 同一个站点用两个身份发信，收件人看到的发件人会飘。

// mailSecret 把加密存储的凭据 JSON 解密成 mail.Secret。
func (a *App) mailSecret(enc string) (mail.Secret, error) {
	if strings.TrimSpace(enc) == "" {
		return mail.Secret{}, nil
	}
	if len(a.Cfg.MasterKey) == 0 {
		return nil, errors.New("服务器未配置 MASTER_KEY_BASE64，无法解密邮件凭据")
	}
	plain, err := security.Decrypt(a.Cfg.MasterKey, enc)
	if err != nil {
		return nil, errors.New("解密邮件凭据失败")
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, errors.New("邮件凭据不是合法 JSON")
	}
	return mail.Secret(out), nil
}

// resolveMailSender 返回一个已就绪的发送函数与通道标识（用于日志/审计）。
func (a *App) resolveMailSender(ctx context.Context) (func(context.Context, string, string, string) error, string, error) {
	pv, secretEnc, err := a.Store.ActiveMailProvider(ctx)
	if err == nil {
		impl, ok := mail.Get(pv.Provider)
		if !ok {
			return nil, "", errors.New("未知的邮件通道：" + pv.Provider)
		}
		secret, serr := a.mailSecret(secretEnc)
		if serr != nil {
			return nil, "", serr
		}
		cfg := mail.Config{Provider: pv.Provider, Fields: pv.Config}
		if verr := impl.Validate(cfg, secret); verr != nil {
			return nil, "", verr
		}
		send := func(sctx context.Context, to, subject, body string) error {
			serr := impl.Send(sctx, cfg, secret, mail.Message{To: to, Subject: subject, HTML: body})
			a.Store.TouchMailProvider(sctx, pv.PublicID, serr == nil, errText(serr))
			return serr
		}
		return send, pv.Provider, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, "", err
	}
	// 兜底：内置 SMTP 设置。
	opts, err := a.mailOptions(ctx)
	if err != nil {
		return nil, "", err
	}
	return func(sctx context.Context, to, subject, body string) error {
		return opts.Send(sctx, to, subject, body)
	}, "smtp", nil
}

// mailConfigured 判断当前是否存在可用的发信方式（通道或 SMTP）。
// 注册/登录流程用它决定「邮箱验证」是否可以启用，避免只配了通道却提示未配置。
func (a *App) mailConfigured(ctx context.Context) bool {
	if _, _, err := a.Store.ActiveMailProvider(ctx); err == nil {
		return true
	}
	s, err := a.Store.GetMailSettings(ctx)
	return err == nil && s.SMTPHost != "" && s.SMTPFrom != "" && s.SMTPPort > 0
}

// deliverMail 把邮件交给队列；没有 Redis 时同步发送。
func (a *App) deliverMail(ctx context.Context, to, subject, body string) error {
	if a.Queue != nil {
		return a.Queue.MailSend(to, subject, body)
	}
	sender, _, err := a.resolveMailSender(ctx)
	if err != nil {
		return err
	}
	return sender(ctx, to, subject, body)
}

// ---- 管理端：邮件通道 ----

// adminListMailProviders 列出所有邮件通道，并带上可用的通道实现标识。
func (a *App) adminListMailProviders(c *gin.Context) {
	items, err := a.Store.ListMailProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "MAIL_PROVIDERS_FAILED", "读取邮件通道失败")
		return
	}
	httpx.OK(c, 200, map[string]any{
		"providers": items,
		"available": mail.Names(),
	})
}

// adminCreateMailProvider 新增邮件通道。
// secret 是通道凭据 JSON（如 {"access_key_id":"..","access_key_secret":".."}）。
func (a *App) adminCreateMailProvider(c *gin.Context) {
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
		httpx.Fail(c, 400, "INVALID_MAIL_PROVIDER", "通道名称与类型不能为空")
		return
	}
	impl, ok := mail.Get(in.Provider)
	if !ok {
		httpx.Fail(c, 400, "MAIL_PROVIDER_UNKNOWN", "未知的邮件通道："+in.Provider)
		return
	}
	cfg := mail.Config{Provider: in.Provider, Fields: in.Config}
	secret := mail.Secret(in.Secret)
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 400, "MAIL_PROVIDER_INVALID", err.Error())
		return
	}
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		httpx.Fail(c, 400, "MAIL_PROVIDER_INVALID", "凭据格式错误")
		return
	}
	enc := ""
	if len(secret) > 0 {
		if len(a.Cfg.MasterKey) == 0 {
			httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法加密邮件凭据")
			return
		}
		enc, err = security.Encrypt(a.Cfg.MasterKey, string(secretJSON))
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密邮件凭据失败")
			return
		}
	}
	v, err := a.Store.CreateMailProvider(c, in.Name, in.Provider, in.Config, enc, in.IsDefault)
	if err != nil {
		httpx.Fail(c, 400, "MAIL_PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "mail_provider.create", "mail_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "provider": v.Provider})
	httpx.OK(c, 201, v)
}

// adminSetDefaultMailProvider 把某个通道设为默认。
func (a *App) adminSetDefaultMailProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.SetMailProviderDefault(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "MAIL_PROVIDER_NOT_FOUND", "邮件通道不存在")
			return
		}
		httpx.Fail(c, 400, "MAIL_PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "mail_provider.set_default", "mail_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteMailProvider 删除通道。
func (a *App) adminDeleteMailProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteMailProvider(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "MAIL_PROVIDER_NOT_FOUND", "邮件通道不存在")
			return
		}
		httpx.Fail(c, 400, "MAIL_PROVIDER_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "mail_provider.delete", "mail_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminTestMailProvider 用指定通道发一封测试邮件。
func (a *App) adminTestMailProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		To string `json:"to"`
	}
	_ = c.ShouldBindJSON(&in) // 允许空请求体：默认发给当前管理员
	to := strings.TrimSpace(in.To)
	if to == "" {
		to = pr.User.Email
	}
	pv, secretEnc, err := a.Store.GetMailProvider(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "MAIL_PROVIDER_NOT_FOUND", "邮件通道不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "MAIL_PROVIDER_READ_FAILED", "读取邮件通道失败")
		return
	}
	impl, ok := mail.Get(pv.Provider)
	if !ok {
		httpx.Fail(c, 400, "MAIL_PROVIDER_UNKNOWN", "未知的邮件通道："+pv.Provider)
		return
	}
	secret, err := a.mailSecret(secretEnc)
	if err != nil {
		httpx.Fail(c, 500, "MAIL_SECRET_INVALID", err.Error())
		return
	}
	cfg := mail.Config{Provider: pv.Provider, Fields: pv.Config}
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 400, "MAIL_PROVIDER_INVALID", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	subject := "ShitIDC 测试邮件"
	body := "<p>这是一封来自 ShitIDC 后台的测试邮件。收到即表示「" + pv.Name + "」通道配置正确。</p>"
	err = impl.Send(ctx, cfg, secret, mail.Message{To: to, Subject: subject, HTML: body})
	a.Store.TouchMailProvider(c, pv.PublicID, err == nil, errText(err))
	if err != nil {
		httpx.Fail(c, 502, "MAIL_SEND_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "mail_provider.test", "mail_provider", pv.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"to": to})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
