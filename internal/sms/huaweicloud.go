// 华为云短信 batchSendSms v1（对应魔方 public/plugins/sms/huaweicloud）。
//
// POST https://smsapi.cn-north-4.myhuaweicloud.com:443/sms/batchSendSms/v1
// 认证是 WSSE UsernameToken 头（不是标准 HTTP Basic）：
//
//	X-WSSE: UsernameToken Username="APP_Key",
//	        PasswordDigest="Base64(SHA256(Nonce+Created+APP_Secret) 的十六进制串)",
//	        Nonce="...", Created="2026-10-05T00:00:00Z"
//
// 注意 PasswordDigest 是「先转十六进制再 Base64」，不是 Base64(原始摘要)——
// 与魔方 PHP 插件 `base64_encode(hash('sha256', ...))` 逐字节一致（PHP 的
// hash() 默认返回十六进制串）。华为官方文档同样是这个口径。有测试钉死。
//
// 表单字段：from(通道号) / to(E.164) / templateId / templateParas('["验证码"]')
// / signature(国内签名)。成功判据：响应 JSON 的 code == "000000"。
package sms

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Huaweicloud 实现华为云短信通道。
type Huaweicloud struct {
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	// Now / Nonce 允许测试注入，保证 WSSE 头可复算。
	Now   func() time.Time
	Nonce func() string
}

// NewHuaweicloud 构造通道。
func NewHuaweicloud() *Huaweicloud {
	return &Huaweicloud{Endpoint: "https://smsapi.cn-north-4.myhuaweicloud.com:443/sms/batchSendSms/v1", Now: time.Now, Nonce: randomHex16}
}

// Name 返回通道标识。
func (h *Huaweicloud) Name() string { return "huaweicloud" }

func init() { Register(NewHuaweicloud()) }

// Validate 检查必填配置（国内通道）。国际通道号/模板是可选项。
func (h *Huaweicloud) Validate(cfg Config, secret Secret) error {
	if secret.Get("app_key") == "" || secret.Get("app_secret") == "" {
		return fmt.Errorf("华为云短信需要 app_key 与 app_secret")
	}
	if cfg.Field("sender") == "" {
		return fmt.Errorf("华为云短信需要国内短信签名通道号 sender")
	}
	if cfg.Field("template_id") == "" {
		return fmt.Errorf("华为云短信需要模板 ID template_id")
	}
	return nil
}

// Send 发送模板短信，模板参数固定为验证码一项。
func (h *Huaweicloud) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := h.Validate(cfg, secret); err != nil {
		return err
	}
	appKey, appSecret := secret.Get("app_key"), secret.Get("app_secret")
	sender, templateID := cfg.Field("sender"), cfg.Field("template_id")
	signature := cfg.Field("sign_name")

	to := e164Phone(msg.Phone)
	if !isMainlandPhone(msg.Phone) {
		sender = cfg.Field("global_sender")
		templateID = firstNonEmptyStr(cfg.Field("global_template_id"), templateID)
		appKey = firstNonEmptyStr(secret.Get("global_app_key"), appKey)
		appSecret = firstNonEmptyStr(secret.Get("global_app_secret"), appSecret)
		signature = ""
		if sender == "" || cfg.Field("global_template_id") == "" {
			return fmt.Errorf("国际短信需要配置 global_sender 与 global_template_id")
		}
	}

	form := url.Values{}
	form.Set("from", sender)
	form.Set("to", to)
	form.Set("templateId", templateID)
	// 模板参数是 JSON 字符串数组：'["123456"]'。
	paramsJSON, _ := json.Marshal([]string{msg.Code})
	form.Set("templateParas", string(paramsJSON))
	if signature != "" {
		form.Set("signature", signature)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", `WSSE realm="SDP",profile="UsernameToken",type="Appkey"`)
	req.Header.Set("X-WSSE", buildWSSEHeader(appKey, appSecret, h.Now().UTC().Format("2006-01-02T15:04:05Z"), h.Nonce()))

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("华为云短信请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("华为云短信返回无法解析(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out.Code != "000000" {
		return fmt.Errorf("华为云短信发送失败: %s (%s)", firstNonEmptyStr(out.Description, "未知错误"), out.Code)
	}
	return nil
}

// buildWSSEHeader 构造 X-WSSE 头。
// PasswordDigest = Base64( SHA256(Nonce+Created+Secret) 的十六进制小写串 )。
func buildWSSEHeader(appKey, appSecret, created, nonce string) string {
	digest := base64.StdEncoding.EncodeToString([]byte(sha256Hex(nonce + created + appSecret)))
	return fmt.Sprintf(`UsernameToken Username="%s",PasswordDigest="%s",Nonce="%s",Created="%s"`,
		appKey, digest, nonce, created)
}
