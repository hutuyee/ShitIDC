package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// changePassword rotates the caller's password. The old password must be
// verified, and every existing session dies with the change.
func (a *App) changePassword(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if len(in.NewPassword) < 10 {
		httpx.Fail(c, 400, "WEAK_PASSWORD", "新密码至少 10 个字符")
		return
	}
	creds, err := a.Store.GetLoginCredentials(c, p.User.Email)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账户失败")
		return
	}
	if !security.VerifyPassword(creds.PasswordHash, in.CurrentPassword) {
		httpx.Fail(c, 403, "WRONG_PASSWORD", "当前密码不正确")
		return
	}
	newHash, err := security.HashPassword(in.NewPassword)
	if err != nil {
		httpx.Fail(c, 400, "WEAK_PASSWORD", err.Error())
		return
	}
	if err := a.Store.ChangePassword(c, p.User.ID, creds.PasswordHash, newHash); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "修改密码失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "auth.password.change", "user", p.User.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	// The session was revoked; the current cookie must go too.
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("shitidc_session", "", -1, "/", a.Cfg.CookieDomain, a.Cfg.CookieSecure, true)
	httpx.OK(c, 200, map[string]any{"ok": true, "message": "密码已修改，请重新登录"})
}

// requestPasswordReset emails a reset code. It always answers ok so the
// endpoint cannot be used to enumerate registered addresses.
func (a *App) requestPasswordReset(c *gin.Context) {
	var in struct {
		Email      string `json:"email"`
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
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if !strings.Contains(email, "@") {
		httpx.Fail(c, 400, "INVALID_EMAIL", "邮箱格式错误")
		return
	}
	settings, err := a.Store.GetMailSettings(c)
	if err != nil || settings.SMTPHost == "" || settings.SMTPFrom == "" || settings.SMTPPort <= 0 {
		httpx.Fail(c, 503, "SMTP_NOT_CONFIGURED", "邮件服务未配置，请联系管理员")
		return
	}
	if _, err := a.Store.GetLoginCredentials(c, email); err != nil {
		// Unknown address: same answer, no user enumeration.
		httpx.OK(c, 200, map[string]bool{"ok": true})
		return
	}
	code, err := security.NumericCode(6)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成验证码失败")
		return
	}
	if err := a.Store.PutEmailCode(c, email, "password_reset", security.SHA256Hex(code), 10*time.Minute, 60*time.Second); err != nil {
		if errors.Is(err, store.ErrCodeRateLimited) {
			httpx.Fail(c, 429, "CODE_RATE_LIMITED", "发送太频繁，请 1 分钟后再试")
			return
		}
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存验证码失败")
		return
	}
	fallbackSubject, fallbackBody := passwordResetMail(code)
	subject, body := a.renderMail("password_reset", fallbackSubject, fallbackBody, map[string]string{"code": code, "email": email})
	if a.Queue != nil {
		if err := a.Queue.MailSend(email, subject, body); err != nil {
			httpx.Fail(c, 502, "MAIL_ENQUEUE_FAILED", "重置邮件投递失败，请稍后重试")
			return
		}
	} else {
		opts, oerr := a.mailOptions(c)
		if oerr != nil {
			httpx.Fail(c, 503, "SMTP_NOT_CONFIGURED", oerr.Error())
			return
		}
		if err := opts.Send(c, email, subject, body); err != nil {
			httpx.Fail(c, 502, "MAIL_SEND_FAILED", "重置邮件发送失败: "+err.Error())
			return
		}
	}
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// confirmPasswordReset consumes the code and installs the new password.
func (a *App) confirmPasswordReset(c *gin.Context) {
	var in struct {
		Email       string `json:"email"`
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	email := strings.TrimSpace(strings.ToLower(in.Email))
	code := strings.TrimSpace(in.Code)
	if !strings.Contains(email, "@") || len(code) != 6 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "邮箱或验证码格式错误")
		return
	}
	newHash, err := security.HashPassword(in.NewPassword)
	if err != nil {
		httpx.Fail(c, 400, "WEAK_PASSWORD", err.Error())
		return
	}
	if err := a.Store.ConsumeEmailCode(c, email, "password_reset", security.SHA256Hex(code)); err != nil {
		httpx.Fail(c, 400, mapCodeError(err), "验证码错误或已过期")
		return
	}
	uid, err := a.Store.ResetPassword(c, email, newHash)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "重置密码失败")
		return
	}
	_ = a.Store.RecordLoginAttempt(c, uid, email, false, "password_reset", clientIP(c), c.Request.UserAgent())
	_ = a.Store.Audit(c, uid, "auth.password.reset", "user", email, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	_ = a.Store.SecurityEvent(c, uid, "password.reset", "notice", clientIP(c), nil)
	a.Bus.Emit(a.eventCtx(c), events.UserPasswordReset, map[string]any{"uid": uid})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func passwordResetMail(code string) (string, string) {
	subject := "ShitIDC 密码重置验证码"
	body := `<div style="max-width:520px;margin:0 auto;font-family:sans-serif">
<h2 style="color:#4f46e5">密码重置验证码</h2>
<p>你（或有人）请求重置密码，验证码是：</p>
<p style="font-size:30px;font-weight:800;letter-spacing:6px;color:#111">` + code + `</p>
<p>验证码 10 分钟内有效。如果不是你本人操作，请忽略本邮件并检查账户安全。</p>
</div>`
	return subject, body
}
