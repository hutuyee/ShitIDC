// 阿里云短信（Dysmsapi 2017-05-25）。
//
// 签名算法（RPC 风格，官方文档 "签名机制"）：
//  1. 公共参数 + 业务参数按参数名排序，逐个 URL 编码后拼成
//     k1=v1&k2=v2 形式的规范化查询串
//  2. StringToSign = "POST" + "&" + percentEncode("/") + "&" + percentEncode(规范化查询串)
//  3. Signature = Base64(HMAC-SHA1(AccessKeySecret + "&", StringToSign))
//
// 编码规则是这里最容易写错的地方：阿里云要求 percentEncode 把 "+" 编成 "%20"、
// "*" 编成 "%2A"、"%7E" 还原成 "~"，与标准 url.QueryEscape 不同。
// 我把不同之处单独抽成 aliPercentEncode 并配了测试。
package sms

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Aliyun 实现阿里云短信通道。
type Aliyun struct {
	http *http.Client
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	// Now / Random 允许测试注入，保证签名可复算。
	Now    func() time.Time
	Random func() string
}

// NewAliyun 构造通道。
func NewAliyun() *Aliyun {
	return &Aliyun{
		http:     &http.Client{Timeout: 15 * time.Second},
		Endpoint: "https://dysmsapi.aliyuncs.com/",
		Now:      time.Now,
		Random:   randomNumeric,
	}
}

// Name 返回通道标识。
func (a *Aliyun) Name() string { return "aliyun" }

// init 把阿里云通道注册进注册表，导入本包即生效。
func init() { Register(NewAliyun()) }

func randomNumeric() string {
	const digits = "0123456789"
	b := make([]byte, 16)
	for i := range b {
		b[i] = digits[rand.Intn(len(digits))]
	}
	return string(b)
}

// aliPercentEncode 是阿里云要求的编码：在标准 URL 编码基础上做三处替换。
//
//   - -> %20（标准编码是 %2B 才算对，但阿里云要求空格语义的 %20）
//   - -> %2A
//     %7E -> ~
func aliPercentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

// canonicalQuery 把参数按名排序并编码成规范化查询串。
func canonicalQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, aliPercentEncode(k)+"="+aliPercentEncode(params[k]))
	}
	return strings.Join(parts, "&")
}

// aliyunSignature 计算签名：Base64(HMAC-SHA1(secret+"&", StringToSign))。
func aliyunSignature(secret string, params map[string]string) string {
	stringToSign := "POST&" + aliPercentEncode("/") + "&" + aliPercentEncode(canonicalQuery(params))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Validate 检查必填配置。
func (a *Aliyun) Validate(cfg Config, secret Secret) error {
	if cfg.Field("sign_name") == "" {
		return fmt.Errorf("阿里云短信需要短信签名 sign_name")
	}
	if cfg.Field("template_code") == "" {
		return fmt.Errorf("阿里云短信需要模板 ID template_code")
	}
	if secret.Get("access_key_id") == "" || secret.Get("access_key_secret") == "" {
		return fmt.Errorf("阿里云短信需要 access_key_id 与 access_key_secret")
	}
	return nil
}

// Send 调用 SendSms 接口。
func (a *Aliyun) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := a.Validate(cfg, secret); err != nil {
		return err
	}
	params := map[string]string{
		"AccessKeyId":      secret.Get("access_key_id"),
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     msg.Phone,
		"RegionId":         firstNonEmptyStr(cfg.Field("region_id"), "cn-hangzhou"),
		"SignName":         cfg.Field("sign_name"),
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   a.Random(),
		"SignatureVersion": "1.0",
		"TemplateCode":     cfg.Field("template_code"),
		"TemplateParam":    templateParam(cfg, msg),
		"Timestamp":        a.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2017-05-25",
	}
	params["Signature"] = aliyunSignature(secret.Get("access_key_secret"), params)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("阿里云短信请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("阿里云短信返回无法解析(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// 阿里云用 Code="OK" 表示成功。
	if out.Code != "OK" {
		return fmt.Errorf("阿里云短信发送失败: %s (%s)", firstNonEmptyStr(out.Message, "未知错误"), out.Code)
	}
	return nil
}

// templateParam 组装模板参数。默认用 {"code":"123456"}；
// 模板需要别的参数时可在配置里写 template_param（JSON），其中的 {{code}} 会被替换。
func templateParam(cfg Config, msg Message) string {
	if raw := cfg.Field("template_param"); raw != "" {
		replaced := strings.ReplaceAll(raw, "{{code}}", msg.Code)
		if json.Valid([]byte(replaced)) {
			return replaced
		}
	}
	payload, err := json.Marshal(map[string]string{"code": msg.Code})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

// firstNonEmptyStr 返回第一个非空字符串。
func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
