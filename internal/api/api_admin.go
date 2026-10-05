package api

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/mail"
	"github.com/hutuyee/ShitIDC/internal/payment"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// adminListUsers returns the user directory for the admin console.
func (a *App) adminListUsers(c *gin.Context) {
	v, err := a.Store.ListUsers(c, c.Query("query"), parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取用户列表失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminListOrders(c *gin.Context) {
	v, err := a.Store.ListOrdersAdmin(c, parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取订单失败")
		return
	}
	httpx.OK(c, 200, v)
}

// ---- online payment providers ----

func sanitizePayTypes(raw []string) []string {
	allowed := map[string]bool{"alipay": true, "wxpay": true, "qqpay": true, "bank": true, "jiedebao": true, "paypal": true, "usdt": true, "epay": true, "manual": true}
	out := []string{}
	for _, t := range raw {
		t = strings.ToLower(strings.TrimSpace(t))
		if allowed[t] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		out = []string{"alipay", "wxpay"}
	}
	return out
}

func (a *App) adminListPaymentProviders(c *gin.Context) {
	items, err := a.Store.ListPaymentProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取支付方式失败")
		return
	}
	httpx.OK(c, 200, items)
}

func (a *App) adminCreatePaymentProvider(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name       string   `json:"name"`
		Method     string   `json:"method"`
		GatewayURL string   `json:"gateway_url"`
		MerchantID string   `json:"merchant_id"`
		Secret     string   `json:"secret"`
		PayTypes   []string `json:"pay_types"`
		Message    string   `json:"message"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Method = strings.ToLower(strings.TrimSpace(in.Method))
	if in.Method == "" {
		in.Method = "epay"
	}
	// 第七阶段: any method registered in the payment gateway registry is
	// accepted; unknown channels are rejected with the available list.
	if _, ok := payment.Get(in.Method); !ok {
		httpx.Fail(c, 400, "METHOD_UNSUPPORTED", "不支持的支付方式，当前可用: "+strings.Join(payment.Methods(), ", "))
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "支付方式名称不能为空")
		return
	}
	var enc string
	if in.Method != "manual" {
		var err error
		enc, err = security.Encrypt(a.Cfg.MasterKey, in.Secret)
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "MASTER_KEY_BASE64 未正确配置，无法安全保存支付密钥")
			return
		}
	}
	cfg := map[string]any{"pay_types": sanitizePayTypes(in.PayTypes)}
	if in.Method == "manual" {
		// 线下支付不需要网关参数：渠道固定 manual，密钥留空，说明存 config.message。
		cfg["pay_types"] = []string{"manual"}
	}
	if msg := strings.TrimSpace(in.Message); msg != "" {
		cfg["message"] = msg
	}
	v, err := a.Store.CreatePaymentProvider(c, strings.TrimSpace(in.Name), in.Method, strings.TrimSpace(in.GatewayURL), strings.TrimSpace(in.MerchantID), enc, cfg)
	if err != nil {
		httpx.Fail(c, 400, "PAYMENT_PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "payment_provider.create", "payment_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdatePaymentProvider(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name       string   `json:"name"`
		GatewayURL string   `json:"gateway_url"`
		MerchantID string   `json:"merchant_id"`
		Secret     string   `json:"secret"`
		PayTypes   []string `json:"pay_types"`
		Message    string   `json:"message"`
		Active     *bool    `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	current, _, err := a.Store.GetPaymentProviderCredentials(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PAYMENT_PROVIDER_NOT_FOUND", "支付方式不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取支付方式失败")
		return
	}
	enc := ""
	if !strings.EqualFold(current.Method, "manual") && strings.TrimSpace(in.Secret) != "" {
		enc, err = security.Encrypt(a.Cfg.MasterKey, in.Secret)
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "MASTER_KEY_BASE64 未正确配置，无法安全保存支付密钥")
			return
		}
	}
	cfg := map[string]any{"pay_types": sanitizePayTypes(in.PayTypes)}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		if old, ok := current.Config["message"].(string); ok {
			msg = strings.TrimSpace(old)
		}
	}
	if strings.EqualFold(current.Method, "manual") {
		// 线下支付：渠道固定 manual、永远没有密钥；收款说明允许清空。
		cfg["pay_types"] = []string{"manual"}
		cfg["message"] = msg
	} else if msg != "" {
		cfg["message"] = msg
	}
	v, err := a.Store.UpdatePaymentProvider(c, c.Param("id"), strings.TrimSpace(in.Name), strings.TrimSpace(in.GatewayURL), strings.TrimSpace(in.MerchantID), enc, cfg, active)
	if err != nil {
		httpx.Fail(c, 400, "PAYMENT_PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "payment_provider.update", "payment_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 200, v)
}

// ---- SMTP / mail settings ----

func (a *App) adminGetMailSettings(c *gin.Context) {
	s, err := a.Store.GetMailSettings(c)
	if err != nil {
		s = store.MailSettings{}
	}
	httpx.OK(c, 200, map[string]any{
		"smtp_host":       s.SMTPHost,
		"smtp_port":       s.SMTPPort,
		"smtp_username":   s.SMTPUsername,
		"smtp_from":       s.SMTPFrom,
		"from_name":       s.FromName,
		"smtp_encryption": s.SMTPEncryption,
		"verify_required": s.VerifyRequired,
		"has_password":    s.SMTPPasswordEn != "",
		"smtp_enabled":    s.SMTPHost != "" && s.SMTPFrom != "" && s.SMTPPort > 0,
	})
}

func (a *App) adminSaveMailSettings(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		SMTPHost       string `json:"smtp_host"`
		SMTPPort       int    `json:"smtp_port"`
		SMTPUsername   string `json:"smtp_username"`
		SMTPPassword   string `json:"smtp_password"`
		SMTPFrom       string `json:"smtp_from"`
		FromName       string `json:"from_name"`
		SMTPEncryption string `json:"smtp_encryption"`
		VerifyRequired bool   `json:"verify_required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	switch strings.ToLower(in.SMTPEncryption) {
	case "ssl", "smtps", "none", "plain", "", "starttls":
	default:
		httpx.Fail(c, 400, "INVALID_REQUEST", "加密方式仅支持 starttls / ssl / none")
		return
	}
	current, err := a.Store.GetMailSettings(c)
	if err != nil {
		current = store.MailSettings{}
	}
	enc := current.SMTPPasswordEn
	if strings.TrimSpace(in.SMTPPassword) != "" {
		e, err := security.Encrypt(a.Cfg.MasterKey, in.SMTPPassword)
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "MASTER_KEY_BASE64 未正确配置，无法安全保存 SMTP 密码")
			return
		}
		enc = e
	}
	settings := store.MailSettings{
		SMTPHost:       strings.TrimSpace(in.SMTPHost),
		SMTPPort:       in.SMTPPort,
		SMTPUsername:   strings.TrimSpace(in.SMTPUsername),
		SMTPPasswordEn: enc,
		SMTPFrom:       strings.TrimSpace(in.SMTPFrom),
		FromName:       strings.TrimSpace(in.FromName),
		SMTPEncryption: strings.ToLower(in.SMTPEncryption),
		VerifyRequired: in.VerifyRequired,
	}
	if err := a.Store.SaveMailSettings(c, settings); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存邮件设置失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "settings.mail.update", "settings", "mail", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"verify_required": settings.VerifyRequired, "smtp_host": settings.SMTPHost})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminTestMail(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		To string `json:"to"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	to := strings.TrimSpace(in.To)
	if to == "" {
		to = p.User.Email
	}
	sender, channel, err := a.resolveMailSender(c)
	if err != nil {
		httpx.Fail(c, 503, "SMTP_NOT_CONFIGURED", err.Error())
		return
	}
	subject := "ShitIDC 测试邮件"
	body := `<p>这是一封来自 ShitIDC 后台的测试邮件。收到即表示「` + channel + `」配置正确。</p>`
	if err := sender(c, to, subject, body); err != nil {
		httpx.Fail(c, 502, "MAIL_SEND_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "settings.mail.test", "settings", "mail", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"to": to})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// mailOptions assembles SMTP options from stored settings, decrypting the password.
func (a *App) mailOptions(ctx context.Context) (mail.Options, error) {
	s, err := a.Store.GetMailSettings(ctx)
	if err != nil {
		return mail.Options{}, errors.New("邮件服务未配置")
	}
	opts := mail.Options{Host: s.SMTPHost, Port: s.SMTPPort, Username: s.SMTPUsername, From: s.SMTPFrom, Encryption: s.SMTPEncryption}
	if !opts.Enabled() {
		return mail.Options{}, errors.New("邮件服务未配置")
	}
	if s.SMTPPasswordEn != "" {
		plain, derr := security.Decrypt(a.Cfg.MasterKey, s.SMTPPasswordEn)
		if derr != nil {
			return mail.Options{}, errors.New("SMTP 密码解密失败")
		}
		opts.Password = plain
	}
	return opts, nil
}
