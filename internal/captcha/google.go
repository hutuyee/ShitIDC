// 谷歌 reCAPTCHA（对应魔方 public/plugins/captcha/GoogleCaptcha）。
//
// 前端加载 recaptcha 的 api.js 渲染组件，用户通过后拿到 response token；
// 后端把 secret、response 与 remoteip 提交到校验接口，success 为 true 才通过。
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google 实现谷歌 reCAPTCHA。
type Google struct {
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	http     *http.Client
}

// NewGoogle 构造通道。
func NewGoogle() *Google {
	return &Google{
		Endpoint: "https://www.recaptcha.net/recaptcha/api/siteverify",
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Name 返回通道标识。
func (g *Google) Name() string { return "google_captcha" }

func init() { Register(NewGoogle()) }

// Validate 检查必填配置。
func (g *Google) Validate(cfg Config, secret Secret) error {
	if cfg.Field("site_key") == "" {
		return fmt.Errorf("谷歌人机验证需要 SiteKey site_key")
	}
	if secret.Get("secret_key") == "" {
		return fmt.Errorf("谷歌人机验证需要 SecretKey")
	}
	return nil
}

// Challenge 返回前端渲染 reCAPTCHA 所需的 SiteKey。
func (g *Google) Challenge(cfg Config) Challenge {
	return Challenge{Provider: "google_captcha", Params: map[string]string{"site_key": cfg.Field("site_key")}}
}

// Verify 调用 siteverify 校验票据。
func (g *Google) Verify(ctx context.Context, cfg Config, secret Secret, token Token) error {
	if err := g.Validate(cfg, secret); err != nil {
		return err
	}
	if strings.TrimSpace(token.Value) == "" {
		return fmt.Errorf("谷歌人机验证缺少票据，请重新验证")
	}
	form := url.Values{
		"secret":   {secret.Get("secret_key")},
		"response": {token.Value},
	}
	if strings.TrimSpace(token.RemoteIP) != "" {
		form.Set("remoteip", token.RemoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("谷歌人机验证请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("谷歌人机验证接口异常(HTTP %d)", resp.StatusCode)
	}
	var out struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("谷歌人机验证返回无法解析(HTTP %d)", resp.StatusCode)
	}
	if out.Success {
		return nil
	}
	return fmt.Errorf("谷歌人机验证未通过: %s", googleErrorText(out.ErrorCodes))
}

// googleErrorText 把官方 error-codes 翻成人话；未知码原样带出。
func googleErrorText(codes []string) string {
	if len(codes) == 0 {
		return "验证失败"
	}
	switch codes[0] {
	case "missing-input-secret":
		return "通道未配置 SecretKey"
	case "invalid-input-secret":
		return "SecretKey 无效"
	case "missing-input-response":
		return "缺少验证票据"
	case "invalid-input-response":
		return "验证票据无效或已过期"
	case "bad-request":
		return "请求格式错误"
	case "timeout-or-duplicate":
		return "验证票据已过期或重复使用"
	default:
		return codes[0]
	}
}
