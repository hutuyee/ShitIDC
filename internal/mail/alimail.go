// 阿里云邮件推送（DirectMail，对应魔方 public/plugins/mail/alimail）。
//
// 接口是阿里云 RPC 风格：POST https://dm.aliyuncs.com/ 表单提交，
// Action=SingleSendMail。签名算法与阿里云短信同源（HMAC-SHA1）：
//
//  1. 参数按名排序，逐个按阿里云规则编码后拼成 k=v&k=v
//  2. StringToSign = "POST" & percentEncode("/") & percentEncode(规范化查询串)
//  3. Signature = Base64(HMAC-SHA1(AccessKeySecret + "&", StringToSign))
//
// 编码规则的三处差异（空格→%20、*→%2A、~ 保持原样）在 sms/aliyun.go 已踩过，
// 这里保持同一套实现；两边各自独立，是因为短信与邮件包互不依赖。
package mail

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Alimail 实现阿里云邮件推送通道。
type Alimail struct {
	http *http.Client
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	// Now / Random 允许测试注入，保证签名可复算。
	Now    func() time.Time
	Random func() string
}

// NewAlimail 构造通道。
func NewAlimail() *Alimail {
	return &Alimail{
		http:     &http.Client{Timeout: 15 * time.Second},
		Endpoint: "https://dm.aliyuncs.com/",
		Now:      time.Now,
		Random:   randomNonce,
	}
}

// Name 返回通道标识。
func (a *Alimail) Name() string { return "alimail" }

func init() { Register(NewAlimail()) }

// randomNonce 生成 8 位十六进制随机串（SignatureNonce）。
func randomNonce() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// 极端情况下退化成时间串：签名仍然有效，只是随机性差一些。
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b)
}

// aliMailPercentEncode 是阿里云要求的编码：在标准 URL 编码基础上做三处替换。
func aliMailPercentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

// aliMailCanonicalQuery 把参数按名排序并编码成规范化查询串。
func aliMailCanonicalQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, aliMailPercentEncode(k)+"="+aliMailPercentEncode(params[k]))
	}
	return strings.Join(parts, "&")
}

// aliMailSignature 计算签名：Base64(HMAC-SHA1(secret+"&", StringToSign))。
func aliMailSignature(secret string, params map[string]string) string {
	stringToSign := "POST&" + aliMailPercentEncode("/") + "&" + aliMailPercentEncode(aliMailCanonicalQuery(params))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Validate 检查必填配置。
func (a *Alimail) Validate(cfg Config, secret Secret) error {
	if cfg.Field("account_name") == "" {
		return fmt.Errorf("阿里云邮件推送需要发信地址 account_name")
	}
	if secret.Get("access_key_id") == "" || secret.Get("access_key_secret") == "" {
		return fmt.Errorf("阿里云邮件推送需要 access_key_id 与 access_key_secret")
	}
	return nil
}

// Send 调用 SingleSendMail 接口。
func (a *Alimail) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := a.Validate(cfg, secret); err != nil {
		return err
	}
	params := map[string]string{
		"AccessKeyId":      secret.Get("access_key_id"),
		"AccountName":      cfg.Field("account_name"),
		"Action":           "SingleSendMail",
		"AddressType":      "1",
		"Format":           "JSON",
		"FromAlias":        cfg.Field("from_alias"),
		"HtmlBody":         msg.HTML,
		"ReplyToAddress":   "false",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   a.Random(),
		"SignatureVersion": "1.0",
		"Subject":          msg.Subject,
		"Timestamp":        a.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"ToAddress":        msg.To,
		"Version":          "2015-11-23",
	}
	params["Signature"] = aliMailSignature(secret.Get("access_key_secret"), params)

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
		return fmt.Errorf("阿里云邮件推送请求失败: %w", err)
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
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out.Code != "" {
		if out.Code != "" {
			return fmt.Errorf("阿里云邮件推送失败[%s]：%s", out.Code, firstNonEmpty(out.Message, "未知错误"))
		}
		return fmt.Errorf("阿里云邮件推送失败：HTTP %s：%s", resp.Status, truncateBody(string(body)))
	}
	return nil
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
