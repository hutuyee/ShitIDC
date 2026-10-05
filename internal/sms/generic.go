// 通用 HTTP 短信通道。
//
// 众多国内短信平台（以及自建网关）暴露的都是「一个 URL + 一组参数 + 看
// 响应关键字」的朴素 HTTP 接口，为一个平台写一个 Go 通道不划算。这里用
// 模板把这些差异收敛到配置里：
//
//	endpoint         请求地址（必填）
//	method           GET / POST，默认 POST
//	content_type     form / json，默认 form（仅 POST 生效）
//	body_template    请求体模板（默认 phone={{phone}}&content={{content}}）
//	success_keyword  响应包含该关键字才算成功；未配置时 2xx 即成功
//
// 模板占位符：{{phone}} {{code}} {{content}} {{timestamp}} 以及 {{secret:KEY}}
// （KEY 是凭据里的键，{{secret:*}} 在日志与错误里绝不展开）。
//
// 诚实的限制：不支持需要特殊签名算法的平台（那些请写 Go 通道或用 WASM 扩展）。
// 占位符替换在纯文本层进行，GET 请求时再按 k=v&k=v 逐值 URL 编码。
package sms

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GenericHTTP 实现通用 HTTP 短信通道。
type GenericHTTP struct{}

// NewGenericHTTP 构造通道。
func NewGenericHTTP() *GenericHTTP { return &GenericHTTP{} }

// Name 返回通道标识。
func (g *GenericHTTP) Name() string { return "generic" }

func init() { Register(NewGenericHTTP()) }

// Validate 检查必填配置。
func (g *GenericHTTP) Validate(cfg Config, secret Secret) error {
	if cfg.Field("endpoint") == "" {
		return fmt.Errorf("通用短信通道需要 endpoint")
	}
	if cfg.Field("content_template") == "" && cfg.Field("body_template") == "" {
		return fmt.Errorf("通用短信通道需要 content_template（短信文案模板）或 body_template")
	}
	method := strings.ToUpper(firstNonEmptyStr(cfg.Field("method"), http.MethodPost))
	if method != http.MethodGet && method != http.MethodPost {
		return fmt.Errorf("通用短信通道 method 只支持 GET / POST")
	}
	ctype := strings.ToLower(firstNonEmptyStr(cfg.Field("content_type"), "form"))
	if ctype != "form" && ctype != "json" {
		return fmt.Errorf("通用短信通道 content_type 只支持 form / json")
	}
	return nil
}

// Send 渲染模板并发请求。
func (g *GenericHTTP) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := g.Validate(cfg, secret); err != nil {
		return err
	}
	content := renderSMSContent(cfg, msg, "content_template")
	body := renderSMSBody(cfg, secret, msg, content)

	method := strings.ToUpper(firstNonEmptyStr(cfg.Field("method"), http.MethodPost))
	endpoint := cfg.Field("endpoint")
	if method == http.MethodGet {
		endpoint = appendQuery(endpoint, body)
		body = ""
	}

	var req *http.Request
	var err error
	if method == http.MethodPost {
		ctype := strings.ToLower(firstNonEmptyStr(cfg.Field("content_type"), "form"))
		mime := "application/x-www-form-urlencoded"
		if ctype == "json" {
			mime = "application/json"
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mime)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("短信请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("短信发送失败：HTTP %d：%s", resp.StatusCode, truncateSMS(string(raw)))
	}
	if keyword := cfg.Field("success_keyword"); keyword != "" && !strings.Contains(string(raw), keyword) {
		return fmt.Errorf("短信发送失败：响应未包含成功标记 %q：%s", keyword, truncateSMS(string(raw)))
	}
	return nil
}

// renderSMSBody 渲染请求体模板。默认 form 形态。
func renderSMSBody(cfg Config, secret Secret, msg Message, content string) string {
	tpl := cfg.Field("body_template")
	if tpl == "" {
		tpl = "phone={{phone}}&content={{content}}"
	}
	replacements := [][2]string{
		{"{{phone}}", msg.Phone},
		{"{{code}}", msg.Code},
		{"{{content}}", content},
		{"{{timestamp}}", fmt.Sprintf("%d", time.Now().Unix())},
	}
	for k, v := range secret {
		replacements = append(replacements, [2]string{"{{secret:" + k + "}}", v})
	}
	for _, kv := range replacements {
		tpl = strings.ReplaceAll(tpl, kv[0], kv[1])
	}
	// 没被替换掉的 secret 占位符清空，避免凭据键名泄露到请求里。
	if i := strings.Index(tpl, "{{secret:"); i >= 0 {
		if j := strings.Index(tpl[i:], "}}"); j >= 0 {
			tpl = tpl[:i] + tpl[i+j+2:]
		}
	}
	return tpl
}

// appendQuery 把已渲染的 k=v&k=v 模板作为 query 追加到 endpoint，
// 值逐个 URL 编码（分隔符保留）。
func appendQuery(endpoint, body string) string {
	if body == "" {
		return endpoint
	}
	q := url.Values{}
	for _, pair := range strings.Split(body, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		q.Set(k, v)
	}
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return endpoint + sep + q.Encode()
}

// truncateSMS 控制错误信息里带上的响应长度。
func truncateSMS(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
