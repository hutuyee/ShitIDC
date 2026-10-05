// 通用 HTTP 邮件通道。
//
// 与短信的 generic 通道同构：把「一个 URL + 模板化请求体 + 成功关键字」的自建
// 邮件网关收敛成配置，避免为一个自建网关写一个 Go 通道。
//
//	endpoint         请求地址（必填）
//	method           GET / POST，默认 POST
//	content_type     form / json，默认 form（仅 POST 生效）
//	body_template    请求体模板（默认 to={{to}}&subject={{subject}}&body={{body}}）
//	success_keyword  响应包含该关键字才算成功；未配置时 2xx 即成功
//
// 模板占位符：{{to}} {{subject}} {{body}} {{timestamp}} 以及 {{secret:KEY}}。
// 未引用的 secret 占位符会被清空，凭据绝不进请求体。
package mail

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GenericHTTP 实现通用 HTTP 邮件通道。
type GenericHTTP struct{}

// NewGenericHTTP 构造通道。
func NewGenericHTTP() *GenericHTTP { return &GenericHTTP{} }

// Name 返回通道标识。
func (g *GenericHTTP) Name() string { return "generic" }

func init() { Register(NewGenericHTTP()) }

// Validate 检查必填配置。
func (g *GenericHTTP) Validate(cfg Config, _ Secret) error {
	if cfg.Field("endpoint") == "" {
		return fmt.Errorf("通用邮件通道需要 endpoint")
	}
	method := strings.ToUpper(firstNonEmpty(cfg.Field("method"), http.MethodPost))
	if method != http.MethodGet && method != http.MethodPost {
		return fmt.Errorf("通用邮件通道 method 只支持 GET / POST")
	}
	ctype := strings.ToLower(firstNonEmpty(cfg.Field("content_type"), "form"))
	if ctype != "form" && ctype != "json" {
		return fmt.Errorf("通用邮件通道 content_type 只支持 form / json")
	}
	return nil
}

// Send 渲染模板并发请求。
func (g *GenericHTTP) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := g.Validate(cfg, secret); err != nil {
		return err
	}
	body := renderMailBody(cfg, secret, msg)
	method := strings.ToUpper(firstNonEmpty(cfg.Field("method"), http.MethodPost))
	endpoint := cfg.Field("endpoint")
	if method == http.MethodGet {
		endpoint = appendMailQuery(endpoint, body)
		body = ""
	}

	var req *http.Request
	var err error
	if method == http.MethodPost {
		ctype := strings.ToLower(firstNonEmpty(cfg.Field("content_type"), "form"))
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
		return fmt.Errorf("邮件请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("邮件发送失败：HTTP %d：%s", resp.StatusCode, truncateBody(string(raw)))
	}
	if keyword := cfg.Field("success_keyword"); keyword != "" && !strings.Contains(string(raw), keyword) {
		return fmt.Errorf("邮件发送失败：响应未包含成功标记 %q：%s", keyword, truncateBody(string(raw)))
	}
	return nil
}

// renderMailBody 渲染请求体模板。
func renderMailBody(cfg Config, secret Secret, msg Message) string {
	tpl := cfg.Field("body_template")
	if tpl == "" {
		tpl = "to={{to}}&subject={{subject}}&body={{body}}"
	}
	replacements := [][2]string{
		{"{{to}}", msg.To},
		{"{{subject}}", msg.Subject},
		{"{{body}}", msg.HTML},
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

// appendMailQuery 把已渲染的 k=v&k=v 模板作为 query 追加到 endpoint。
func appendMailQuery(endpoint, body string) string {
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
