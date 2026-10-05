// 腾讯云短信 SendSms（Sms 2021-01-11，API 3.0）。
//
// 对应魔方 public/plugins/sms/qcloudsms：那个插件直接依赖官方 SDK，这里按
// API 3.0 的 TC3-HMAC-SHA256 签名规范手写（纯标准库，与 wechatpay 的做法一致）：
//
//	CanonicalRequest = "POST\n/\n\n" + 规范化头(按名排序,每条以\n结尾) + "\n" +
//	                   "content-type;host\n" + Hex(SHA256(请求体))
//	StringToSign     = "TC3-HMAC-SHA256\n" + 时间戳 + "\n" +
//	                   日期 + "/sms/tc3_request\n" + Hex(SHA256(CanonicalRequest))
//	Signature        = Hex(HMAC(kSigning, StringToSign))，
//	kDate→kService→kSigning 逐层 HMAC，种子是 "TC3"+SecretKey
//
// 国内号码要求 E.164（+86 前缀）：平台侧入库前已去掉非数字，这里对
// 「11 位且以 1 开头」的号码补 +86；带国家码的按 + 号补回。
package sms

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/tc3"
)

// Qcloudsms 实现腾讯云短信通道。
type Qcloudsms struct {
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	// Now / Nonce 允许测试注入，保证签名可复算。
	Now   func() time.Time
	Nonce func() string
}

// NewQcloudsms 构造通道。
func NewQcloudsms() *Qcloudsms {
	return &Qcloudsms{Endpoint: "https://sms.tencentcloudapi.com", Now: time.Now, Nonce: randomHex16}
}

// Name 返回通道标识（沿用魔方插件的标识 qcloudsms）。
func (q *Qcloudsms) Name() string { return "qcloudsms" }

func init() { Register(NewQcloudsms()) }

// Validate 检查必填配置。
func (q *Qcloudsms) Validate(cfg Config, secret Secret) error {
	if secret.Get("secret_id") == "" || secret.Get("secret_key") == "" {
		return fmt.Errorf("腾讯云短信需要 secret_id 与 secret_key")
	}
	if cfg.Field("sms_sdk_app_id") == "" {
		return fmt.Errorf("腾讯云短信需要应用 SmsSdkAppId")
	}
	if cfg.Field("sign_name") == "" {
		return fmt.Errorf("腾讯云短信需要短信签名 sign_name")
	}
	if cfg.Field("template_id") == "" {
		return fmt.Errorf("腾讯云短信需要模板 ID template_id")
	}
	return nil
}

// Send 调用 SendSms 接口。模板参数固定为验证码一项。
func (q *Qcloudsms) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := q.Validate(cfg, secret); err != nil {
		return err
	}
	now := q.Now()
	timestamp := now.Unix()
	date := now.UTC().Format("2006-01-02")
	region := firstNonEmptyStr(cfg.Field("region"), "ap-guangzhou")

	payload, err := json.Marshal(map[string]any{
		"PhoneNumberSet":   []string{e164Phone(msg.Phone)},
		"SmsSdkAppId":      cfg.Field("sms_sdk_app_id"),
		"SignName":         cfg.Field("sign_name"),
		"TemplateId":       cfg.Field("template_id"),
		"TemplateParamSet": []string{msg.Code},
	})
	if err != nil {
		return err
	}

	authorization := tc3Signature(secret.Get("secret_id"), secret.Get("secret_key"), date, timestamp, string(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-TC-Action", "SendSms")
	req.Header.Set("X-TC-Version", "2021-01-11")
	req.Header.Set("X-TC-Region", region)
	req.Header.Set("X-TC-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("Authorization", authorization)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("腾讯云短信请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Response struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
			SendStatusSet []struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"SendStatusSet"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("腾讯云短信返回无法解析(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// 平台级错误（鉴权失败、参数错误）在 Response.Error；发送级错误在 SendStatusSet。
	if e := out.Response.Error; e != nil {
		return fmt.Errorf("腾讯云短信发送失败: %s (%s)", firstNonEmptyStr(e.Message, "未知错误"), e.Code)
	}
	if len(out.Response.SendStatusSet) == 0 {
		return fmt.Errorf("腾讯云短信返回没有 SendStatusSet(HTTP %d)", resp.StatusCode)
	}
	if s := out.Response.SendStatusSet[0]; s.Code != "Ok" {
		return fmt.Errorf("腾讯云短信发送失败: %s (%s)", firstNonEmptyStr(s.Message, "未知错误"), s.Code)
	}
	return nil
}

// tc3Signature 计算 API 3.0 的 Authorization 头。
// Host 固定为 sms.tencentcloudapi.com（endpoint 覆盖只用于测试，签名不变）；
// 签名实现与验证码通道共用 internal/tc3。
func tc3Signature(secretID, secretKey, date string, timestamp int64, payload string) string {
	return tc3.Authorization(secretID, secretKey, "sms", "sms.tencentcloudapi.com", date, timestamp, payload)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex16() string {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		// crypto/rand 失败时退回时间戳，nonce 只要求不可预测性弱即可。
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// e164Phone 把入库的纯数字号码补成 E.164：
// 11 位以 1 开头视为中国大陆手机号（+86）；其余假定已带国家码，补 +。
func e164Phone(phone string) string {
	if strings.HasPrefix(phone, "+") {
		return phone
	}
	if len(phone) == 11 && strings.HasPrefix(phone, "1") {
		return "+86" + phone
	}
	return "+" + phone
}

// isMainlandPhone 判断是否中国大陆手机号（决定国际短信走哪个子端点）。
func isMainlandPhone(phone string) bool {
	return len(phone) == 11 && strings.HasPrefix(phone, "1")
}
