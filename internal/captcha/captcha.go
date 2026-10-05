// Package captcha 是可插拔的人机验证通道（对应魔方 public/plugins/captcha/）。
//
// 通道只负责「把浏览器拿到的票据送去上游校验」；票据的获取在前端由各平台
// 的 JS 完成（前端按 /auth/captcha 返回的 provider 与公开参数渲染组件）。
// 未配置任何通道时，接口层回退内置图形验证码（internal/security）。
package captcha

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Config 是通道的非敏感配置。
type Config struct {
	Provider string            `json:"provider"`
	Fields   map[string]string `json:"fields"`
}

// Field 安全地取一个配置项。
func (c Config) Field(key string) string {
	if c.Fields == nil {
		return ""
	}
	return strings.TrimSpace(c.Fields[key])
}

// Secret 是通道凭据。
type Secret map[string]string

// Get 安全地取一个凭据项。
func (s Secret) Get(key string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s[key])
}

// Token 是前端拿到的验证票据。
// Value 对应 reCAPTCHA 的 response 或腾讯的 ticket；Randstr 只有腾讯用。
type Token struct {
	Value    string
	Randstr  string
	RemoteIP string
}

// Challenge 是给前端的渲染描述：provider 决定组件形态，Params 是不怕公开的
// 参数（site_key / captcha_app_id），凭据绝不出现。
type Challenge struct {
	Provider string            `json:"provider"`
	Params   map[string]string `json:"params,omitempty"`
}

// Provider 是一个人机验证通道。
type Provider interface {
	Name() string
	// Validate 在保存配置时做静态校验，不必真的调用上游。
	Validate(cfg Config, secret Secret) error
	// Challenge 返回前端渲染所需的公开信息。
	Challenge(cfg Config) Challenge
	// Verify 校验票据；返回错误表示未通过（错误文案直接展示给用户）。
	Verify(ctx context.Context, cfg Config, secret Secret, token Token) error
}

var (
	regMu sync.RWMutex
	reg   = map[string]Provider{}
)

// Register 注册一个通道实现，供 init 调用。
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	reg[strings.ToLower(p.Name())] = p
}

// Get 按标识取通道实现。
func Get(name string) (Provider, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := reg[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// Names 列出已注册的通道标识。
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for name := range reg {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
