package api

// 人机验证（对应魔方 public/plugins/captcha/）：内置图形验证码 + 可插拔通道。
//
// 解析顺序：启用中的通道优先；没有通道时回退内置图形验证码（需要 Redis）。
// 第三方通道的票据由浏览器从平台 JS 拿到（provider 与公开参数走 /auth/captcha），
// 服务端只负责校验。

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hutuyee/ShitIDC/internal/captcha"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

const captchaTTL = 5 * time.Minute

// captchaProvider 解析当前启用的人机验证通道；没有配置时返回 store.ErrNotFound。
func (a *App) captchaProvider(ctx context.Context) (captcha.Provider, captcha.Config, captcha.Secret, store.CaptchaProvider, error) {
	pv, secretEnc, err := a.Store.ActiveCaptchaProvider(ctx)
	if err != nil {
		return nil, captcha.Config{}, nil, store.CaptchaProvider{}, err
	}
	impl, ok := captcha.Get(pv.Provider)
	if !ok {
		return nil, captcha.Config{}, nil, pv, errors.New("未知的人机验证通道：" + pv.Provider)
	}
	secret, serr := a.captchaSecret(secretEnc)
	if serr != nil {
		return nil, captcha.Config{}, nil, pv, serr
	}
	cfg := captcha.Config{Provider: pv.Provider, Fields: pv.Config}
	if verr := impl.Validate(cfg, secret); verr != nil {
		return nil, captcha.Config{}, nil, pv, verr
	}
	return impl, cfg, secret, pv, nil
}

// captchaSecret 解密通道凭据（JSON）。
func (a *App) captchaSecret(enc string) (captcha.Secret, error) {
	if strings.TrimSpace(enc) == "" {
		return captcha.Secret{}, nil
	}
	if len(a.Cfg.MasterKey) == 0 {
		return nil, errors.New("服务器未配置 MASTER_KEY_BASE64，无法解密人机验证凭据")
	}
	plain, err := security.Decrypt(a.Cfg.MasterKey, enc)
	if err != nil {
		return nil, errors.New("解密人机验证凭据失败")
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, errors.New("人机验证凭据不是合法 JSON")
	}
	return captcha.Secret(out), nil
}

// captchaEnabled 判断是否需要人机验证：配置了通道，或内置图形验证码可用（Redis）。
func (a *App) captchaEnabled(ctx context.Context) bool {
	if _, _, _, _, err := a.captchaProvider(ctx); err == nil {
		return true
	}
	return a.Redis != nil
}

// issueCaptcha 返回当前验证方式：第三方通道给公开参数，没有通道时给内置图形题。
func (a *App) issueCaptcha(c *gin.Context) {
	impl, cfg, _, _, err := a.captchaProvider(c)
	if err == nil {
		httpx.OK(c, 200, impl.Challenge(cfg))
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 500, "CAPTCHA_PROVIDER_INVALID", err.Error())
		return
	}
	if a.Redis == nil {
		httpx.OK(c, 200, map[string]string{"provider": "none"})
		return
	}
	challenge, err := security.NewCaptcha()
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成验证码失败")
		return
	}
	if err := a.Redis.Set(c, "captcha:"+challenge.ID, security.SHA256Hex(strings.TrimSpace(strconv.Itoa(challenge.Answer))), captchaTTL).Err(); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存验证码失败")
		return
	}
	httpx.OK(c, 200, map[string]string{"provider": "builtin", "id": challenge.ID, "svg": challenge.SVG})
}

// verifyCaptcha 校验本次请求的验证票据；失败时已经写好响应。
// id/answer 是内置图形验证码的字段；token/randstr 是第三方通道的票据。
func (a *App) verifyCaptcha(c *gin.Context, id, answer, token, randstr string) bool {
	impl, cfg, secret, pv, err := a.captchaProvider(c)
	if err == nil {
		tk := captcha.Token{Value: strings.TrimSpace(token), Randstr: strings.TrimSpace(randstr), RemoteIP: clientIP(c)}
		vctx, cancel := context.WithTimeout(c, 10*time.Second)
		defer cancel()
		verr := impl.Verify(vctx, cfg, secret, tk)
		a.Store.TouchCaptchaProvider(c, pv.PublicID, verr == nil, errText(verr))
		if verr != nil {
			httpx.Fail(c, 400, "CAPTCHA_INVALID", verr.Error())
			return false
		}
		return true
	}
	if !errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 500, "CAPTCHA_PROVIDER_INVALID", err.Error())
		return false
	}
	if a.Redis == nil {
		return true
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(answer) == "" {
		httpx.Fail(c, 400, "CAPTCHA_REQUIRED", "请完成人机验证")
		return false
	}
	key := "captcha:" + id
	stored, rerr := a.Redis.Get(c, key).Result()
	if rerr != nil {
		httpx.Fail(c, 400, "CAPTCHA_EXPIRED", "验证码已过期，请刷新后重试")
		return false
	}
	_ = a.Redis.Del(c, key).Err()
	if stored != security.SHA256Hex(strings.TrimSpace(answer)) {
		httpx.Fail(c, 400, "CAPTCHA_INVALID", "图形验证码错误")
		return false
	}
	return true
}

// ---- 管理端：人机验证通道 ----

// adminListCaptchaProviders 列出所有通道，并带上可用的通道实现标识。
func (a *App) adminListCaptchaProviders(c *gin.Context) {
	items, err := a.Store.ListCaptchaProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "CAPTCHA_PROVIDERS_FAILED", "读取人机验证通道失败")
		return
	}
	httpx.OK(c, 200, map[string]any{
		"providers": items,
		"available": captcha.Names(),
	})
}

// adminCreateCaptchaProvider 新增人机验证通道。
// secret 是通道凭据 JSON（如 {"secret_key":".."}）。
func (a *App) adminCreateCaptchaProvider(c *gin.Context) {
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
		httpx.Fail(c, 400, "INVALID_CAPTCHA_PROVIDER", "通道名称与类型不能为空")
		return
	}
	impl, ok := captcha.Get(in.Provider)
	if !ok {
		httpx.Fail(c, 400, "CAPTCHA_PROVIDER_UNKNOWN", "未知的人机验证通道："+in.Provider)
		return
	}
	cfg := captcha.Config{Provider: in.Provider, Fields: in.Config}
	secret := captcha.Secret(in.Secret)
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 400, "CAPTCHA_PROVIDER_INVALID", err.Error())
		return
	}
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		httpx.Fail(c, 400, "CAPTCHA_PROVIDER_INVALID", "凭据格式错误")
		return
	}
	enc := ""
	if len(secret) > 0 {
		if len(a.Cfg.MasterKey) == 0 {
			httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法加密人机验证凭据")
			return
		}
		enc, err = security.Encrypt(a.Cfg.MasterKey, string(secretJSON))
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密人机验证凭据失败")
			return
		}
	}
	v, err := a.Store.CreateCaptchaProvider(c, in.Name, in.Provider, in.Config, enc, in.IsDefault)
	if err != nil {
		httpx.Fail(c, 400, "CAPTCHA_PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "captcha_provider.create", "captcha_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "provider": v.Provider})
	httpx.OK(c, 201, v)
}

// adminSetDefaultCaptchaProvider 把某个通道设为默认。
func (a *App) adminSetDefaultCaptchaProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.SetCaptchaProviderDefault(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CAPTCHA_PROVIDER_NOT_FOUND", "人机验证通道不存在")
			return
		}
		httpx.Fail(c, 400, "CAPTCHA_PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "captcha_provider.set_default", "captcha_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteCaptchaProvider 删除通道。
func (a *App) adminDeleteCaptchaProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteCaptchaProvider(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CAPTCHA_PROVIDER_NOT_FOUND", "人机验证通道不存在")
			return
		}
		httpx.Fail(c, 400, "CAPTCHA_PROVIDER_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "captcha_provider.delete", "captcha_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
