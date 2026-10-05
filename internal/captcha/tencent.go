// 腾讯云验证码（对应魔方 public/plugins/captcha/TencentCaptcha）。
//
// 前端加载 turing.captcha.qcloud.com/TCaptcha.js，弹窗验证通过后拿到
// ticket 与 randstr；后端调 captcha.tencentcloudapi.com 的
// DescribeCaptchaResult（API 3.0，TC3 签名，版本 2019-07-22），
// CaptchaCode 为 1 才算通过。
package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/tc3"
)

// Tencent 实现腾讯云验证码。
type Tencent struct {
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	// Now 允许测试注入，保证签名可复算。
	Now  func() time.Time
	http *http.Client
}

// NewTencent 构造通道。
func NewTencent() *Tencent {
	return &Tencent{
		Endpoint: "https://captcha.tencentcloudapi.com",
		Now:      time.Now,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Name 返回通道标识。
func (t *Tencent) Name() string { return "tencent_captcha" }

func init() { Register(NewTencent()) }

// Validate 检查必填配置。
func (t *Tencent) Validate(cfg Config, secret Secret) error {
	if secret.Get("secret_id") == "" || secret.Get("secret_key") == "" {
		return fmt.Errorf("腾讯云验证码需要 SecretId 与 SecretKey")
	}
	if secret.Get("app_secret_key") == "" {
		return fmt.Errorf("腾讯云验证码需要 AppSecretKey")
	}
	if _, err := tencentAppID(cfg); err != nil {
		return err
	}
	return nil
}

// Challenge 返回前端渲染 TCaptcha 所需的 CaptchaAppId。
func (t *Tencent) Challenge(cfg Config) Challenge {
	return Challenge{Provider: "tencent_captcha", Params: map[string]string{"captcha_app_id": cfg.Field("captcha_app_id")}}
}

// tencentAppID 把 CaptchaAppId 转成整数（插件同样是 intval 后提交）。
func tencentAppID(cfg Config) (int64, error) {
	id, err := strconv.ParseInt(cfg.Field("captcha_app_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("腾讯云验证码需要数字 CaptchaAppId")
	}
	return id, nil
}

// Verify 调 DescribeCaptchaResult 校验 ticket + randstr。
func (t *Tencent) Verify(ctx context.Context, cfg Config, secret Secret, token Token) error {
	if err := t.Validate(cfg, secret); err != nil {
		return err
	}
	appID, _ := tencentAppID(cfg)
	if strings.TrimSpace(token.Value) == "" || strings.TrimSpace(token.Randstr) == "" {
		return fmt.Errorf("腾讯云验证码缺少票据，请重新验证")
	}
	payload, err := json.Marshal(map[string]any{
		"CaptchaType":  9,
		"Ticket":       token.Value,
		"UserIp":       token.RemoteIP,
		"Randstr":      token.Randstr,
		"CaptchaAppId": appID,
		"AppSecretKey": secret.Get("app_secret_key"),
	})
	if err != nil {
		return err
	}
	now := t.Now()
	auth := tc3.Authorization(secret.Get("secret_id"), secret.Get("secret_key"), "captcha",
		"captcha.tencentcloudapi.com", now.UTC().Format("2006-01-02"), now.Unix(), string(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-TC-Action", "DescribeCaptchaResult")
	req.Header.Set("X-TC-Version", "2019-07-22")
	req.Header.Set("X-TC-Timestamp", fmt.Sprintf("%d", now.Unix()))
	req.Header.Set("Authorization", auth)
	resp, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("腾讯云验证码请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Response struct {
			CaptchaCode int64  `json:"CaptchaCode"`
			CaptchaMsg  string `json:"CaptchaMsg"`
			Error       *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("腾讯云验证码返回无法解析(HTTP %d)", resp.StatusCode)
	}
	if e := out.Response.Error; e != nil {
		return fmt.Errorf("腾讯云验证码校验失败: %s (%s)", firstNonEmpty(e.Message, "未知错误"), e.Code)
	}
	if out.Response.CaptchaCode != 1 {
		return fmt.Errorf("腾讯云验证码校验失败: %s (CaptchaCode %d)",
			firstNonEmpty(out.Response.CaptchaMsg, "验证失败"), out.Response.CaptchaCode)
	}
	return nil
}

// firstNonEmpty 返回第一个非空字符串（错误文案兜底）。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
