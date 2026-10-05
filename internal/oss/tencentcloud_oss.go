// 腾讯云对象存储（COS）——对应魔方 public/plugins/oss/tencentcloud_oss。
//
// 参考插件走官方 SDK 的 HeadBucket / PutObject / GetObjectUrl；这里用标准库
// 实现同一套协议（COS 请求签名 v5）：
//
//	KeyTime      = "起始时间;结束时间"（Unix 秒）
//	SignKey      = hex(HMAC-SHA1(SecretKey, KeyTime))
//	HttpString   = Method + "\n" + UriPathname + "\n" + HttpParameters + "\n" + HttpHeaders + "\n"
//	StringToSign = "sha1\n" + KeyTime + "\n" + hex(SHA1(HttpString)) + "\n"
//	Signature    = hex(HMAC-SHA1(SignKey, StringToSign))
//
// 路径 / 参数 / 头三部分都要按 COS 规则做 URL 编码：保留 '/' 做路径分隔，
// 空格编成 %20（不是 '+'）。
package oss

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// TencentCloudCOS 是腾讯云对象存储通道。
type TencentCloudCOS struct {
	// Endpoint 可覆盖默认的 https://{bucket}.cos.{region}.myqcloud.com（测试用）。
	Endpoint string
	// Now 可注入时钟（测试用）；默认 time.Now。
	Now func() time.Time
	// Client 可注入 HTTP 客户端（测试用）；默认 30 秒超时。
	Client *http.Client
}

// NewTencentCloud 创建腾讯云对象存储通道。
func NewTencentCloud() *TencentCloudCOS { return &TencentCloudCOS{} }

func init() { Register(NewTencentCloud()) }

// Name 返回通道标识。
func (p *TencentCloudCOS) Name() string { return "tencentcloud_oss" }

// Validate 校验必填配置与凭据。
func (p *TencentCloudCOS) Validate(cfg Config, secret Secret) error {
	if cfg.Field("bucket") == "" || cfg.Field("region") == "" {
		return fmt.Errorf("%w：需要 bucket 与 region", ErrNotConfigured)
	}
	if secret.Get("secret_id") == "" || secret.Get("secret_key") == "" {
		return fmt.Errorf("%w：需要 secret_id 与 secret_key", ErrNotConfigured)
	}
	return nil
}

func (p *TencentCloudCOS) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *TencentCloudCOS) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// baseURL 返回桶的访问地址。
func (p *TencentCloudCOS) baseURL(cfg Config) string {
	if ep := strings.TrimSpace(p.Endpoint); ep != "" {
		return strings.TrimRight(ep, "/")
	}
	return "https://" + cfg.Field("bucket") + ".cos." + cfg.Field("region") + ".myqcloud.com"
}

// signature 生成 COS v5 签名查询串（Authorization 头与预签名 URL 通用）。
func (p *TencentCloudCOS) signature(cfg Config, secret Secret, method, uri string, headers map[string]string, valid time.Duration) string {
	now := p.now()
	keyTime := fmt.Sprintf("%d;%d", now.Unix(), now.Add(valid).Unix())
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, k+"="+cosEncode(headers[k], false))
	}
	httpString := strings.ToLower(method) + "\n" + uri + "\n\n" + strings.Join(parts, "&") + "\n"
	signKey := hmacSHA1Hex([]byte(secret.Get("secret_key")), []byte(keyTime))
	signature := hmacSHA1Hex([]byte(signKey), []byte("sha1\n"+keyTime+"\n"+sha1Hex(httpString)+"\n"))
	return "q-sign-algorithm=sha1&q-ak=" + cosEncode(secret.Get("secret_id"), false) +
		"&q-sign-time=" + cosEncode(keyTime, false) +
		"&q-key-time=" + cosEncode(keyTime, false) +
		"&q-header-list=" + cosEncode(strings.Join(names, ";"), false) +
		"&q-url-param-list=&q-signature=" + signature
}

// do 发一次带签名的请求；非 2xx 返回 *cosError。
func (p *TencentCloudCOS) do(ctx context.Context, cfg Config, secret Secret, method, key string, body []byte, contentType, acl string) (*http.Response, error) {
	if err := p.Validate(cfg, secret); err != nil {
		return nil, err
	}
	uri := "/" + cosEncode(key, true)
	rawURL := p.baseURL(cfg) + uri
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"host": u.Host}
	if acl != "" {
		req.Header.Set("x-cos-acl", acl)
		headers["x-cos-acl"] = acl
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
		headers["content-type"] = contentType
	}

	req.Header.Set("Authorization", p.signature(cfg, secret, method, uri, headers, 15*time.Minute))
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return nil, &cosError{Status: resp.StatusCode, Method: method, Key: key, Body: strings.TrimSpace(string(b))}
}

// TestLink 探活：HEAD / 校验桶可访问。
func (p *TencentCloudCOS) TestLink(ctx context.Context, cfg Config, secret Secret) error {
	resp, err := p.do(ctx, cfg, secret, http.MethodHead, "", nil, "", "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Exists 判断对象是否存在。
func (p *TencentCloudCOS) Exists(ctx context.Context, cfg Config, secret Secret, key string) (bool, error) {
	resp, err := p.do(ctx, cfg, secret, http.MethodHead, key, nil, "", "")
	if err != nil {
		if ce, ok := err.(*cosError); ok && ce.Status == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	resp.Body.Close()
	return true, nil
}

// Upload 上传对象；acl 取 "public-read" 或 "private"。
func (p *TencentCloudCOS) Upload(ctx context.Context, cfg Config, secret Secret, key string, body []byte, contentType, acl string) error {
	resp, err := p.do(ctx, cfg, secret, http.MethodPut, key, body, contentType, acl)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// SignedURL 返回有效期内的签名下载地址。
func (p *TencentCloudCOS) SignedURL(ctx context.Context, cfg Config, secret Secret, key string, ttl time.Duration) (string, error) {
	if err := p.Validate(cfg, secret); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = 3 * time.Minute
	}
	uri := "/" + cosEncode(key, true)
	return p.baseURL(cfg) + uri + "?" + p.signature(cfg, secret, http.MethodGet, uri, nil, ttl), nil
}

// cosError 是非 2xx 响应。
type cosError struct {
	Status int
	Method string
	Key    string
	Body   string
}

func (e *cosError) Error() string {
	key := e.Key
	if key == "" {
		key = "/"
	}
	return fmt.Sprintf("COS %s %s 失败：HTTP %d %s", e.Method, key, e.Status, e.Body)
}

// cosEncode 按 COS 规则做 URL 编码：空格编成 %20，路径保留 '/'。
func cosEncode(s string, keepSlash bool) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	const upperHex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 || (c == '/' && keepSlash) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[c>>4])
		b.WriteByte(upperHex[c&0x0F])
	}
	return b.String()
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA1Hex(key, data []byte) string {
	m := hmac.New(sha1.New, key)
	m.Write(data)
	return hex.EncodeToString(m.Sum(nil))
}
