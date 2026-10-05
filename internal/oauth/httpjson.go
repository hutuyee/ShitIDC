package oauth

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

// 各标准 OAuth 通道共用的 HTTP 小工具。GitHub 的实现在 github.go（方法挂在
// GitHub 上，先行存在）；这里提供包级版本，QQ / 微信 / 微博共用。

// oauthHTTP 是通道共用的 HTTP 客户端。超时取上限：回调处理外层还有 20s 的
// context 截断，这里只是兜底，避免个别平台慢响应把 worker 拖死。
var oauthHTTP = &http.Client{Timeout: 20 * time.Second}

// httpGetJSON 发 GET，把响应解析成 JSON 对象。
// token 非空时作为 Bearer 头带上（QQ / 微博的部分接口也接受 query 传 token，
// 这里统一走更标准的头部；平台不支持时由调用方自行拼 query）。
func httpGetJSON(ctx context.Context, target string, headers map[string]string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w：平台返回 HTTP %d", ErrExchangeFailed, resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w：平台响应不是合法 JSON：%s", ErrExchangeFailed, truncateForLog(body))
	}
	return parsed, nil
}

// httpPostForm 发 form 编码的 POST，把响应解析成 JSON 对象。
func httpPostForm(ctx context.Context, target string, form url.Values, headers map[string]string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w：平台返回 HTTP %d", ErrExchangeFailed, resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w：平台响应不是合法 JSON：%s", ErrExchangeFailed, truncateForLog(body))
	}
	return parsed, nil
}

// httpPostJSON 发 JSON 编码的 POST，把响应解析成 JSON 对象。
// 钉钉与企业微信的 token 端点都用 JSON 请求体。
func httpPostJSON(ctx context.Context, target string, payload any, headers map[string]string) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w：平台响应不是合法 JSON（HTTP %d）：%s", ErrExchangeFailed, resp.StatusCode, truncateForLog(body))
	}
	return parsed, nil
}

// unwrapJSONP 剥掉 JSONP 包裹：QQ 的部分端点不认 fmt=json 时会返回
// `callback( {...} );` 形态。剥不出来就原样返回。
func unwrapJSONP(body string) string {
	s := strings.TrimSpace(body)
	s = strings.TrimSuffix(s, ";")
	if open := strings.Index(s, "("); open >= 0 && strings.HasSuffix(s, ")") {
		inner := strings.TrimSpace(s[open+1 : len(s)-1])
		if strings.HasPrefix(inner, "{") {
			return inner
		}
	}
	return s
}

// httpGetRaw 发 GET 并返回原始响应体（QQ 的 token / me 端点返回的不是 JSON）。
func httpGetRaw(ctx context.Context, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w：平台返回 HTTP %d", ErrExchangeFailed, resp.StatusCode)
	}
	return string(body), nil
}

// parseQQTokenResponse 解析 QQ token 端点的三种已知形态：
//
//	access_token=...&expires_in=...            （默认 form）
//	callback( {"access_token":...} );          （JSONP 包裹的 JSON）
//	{"error":...}                              （JSON）
func parseQQTokenResponse(body string) map[string]string {
	s := strings.TrimSpace(body)
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			out := map[string]string{}
			for k, v := range m {
				out[k] = fmt.Sprintf("%v", v)
			}
			return out
		}
	}
	if strings.Contains(s, "callback(") {
		s = unwrapJSONP(s)
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			out := map[string]string{}
			for k, v := range m {
				out[k] = fmt.Sprintf("%v", v)
			}
			return out
		}
	}
	return parseFormResponse(s)
}

// parseFormResponse 解析 `a=1&b=2` 形态的响应（QQ 的 token 端点默认返回这种）。
func parseFormResponse(body string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(body), "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// parseJSONObject 解析一个 JSON 对象字符串，失败返回 ok=false。
func parseJSONObject(s string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, false
	}
	return m, true
}

// errField 从平台错误响应里挑一句人话：error_description / msg / errmsg / Error。
func errField(m map[string]any) string {
	if s := stringField(m, "error_description", "msg", "errmsg", "Error", "error"); s != "" {
		return s
	}
	if v, ok := m["errcode"]; ok {
		return fmt.Sprintf("errcode=%v", v)
	}
	return "未知错误"
}

// truncateForLog 控制错误信息里带上的原始响应长度，避免把日志撑爆。
func truncateForLog(body []byte) string {
	const max = 200
	s := strings.TrimSpace(string(body))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
