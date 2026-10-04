// Package oauth 实现第三方登录（对应魔方 public/plugins/oauth/）。
//
// 统一走标准的 Authorization Code 流程：
//  1. 前端跳转到 AuthorizeURL（带 state）
//  2. 平台回调我们的地址并带上 code
//  3. 服务端用 code 换 access_token，再拉用户资料
//  4. 按 (provider, subject) 找绑定，找不到就建号或要求绑定
//
// 各平台的差异都在 Provider 实现里收敛，主流程只认 Identity。
package oauth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Identity 是一个第三方身份。
type Identity struct {
	Provider string
	// Subject 是该平台内的唯一标识（微信 openid / QQ openid / GitHub id）。
	Subject string
	// UnionID 是跨应用统一标识；为空表示该平台不提供或没拿到。
	UnionID   string
	Nickname  string
	AvatarURL string
	Email     string
	// Raw 是平台返回的原始资料，便于排查（不参与逻辑）。
	Raw map[string]any
}

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

// Secret 是通道凭据（通常是 client_secret）。
type Secret map[string]string

// Get 安全地取一个凭据项。
func (s Secret) Get(key string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s[key])
}

// AuthorizeParams 是发起授权时需要的信息。
type AuthorizeParams struct {
	// RedirectURI 是我们自己的回调地址，必须与平台上登记的一致。
	RedirectURI string
	// State 用于防 CSRF 与重放。
	State string
}

// Provider 是一个第三方登录通道。
type Provider interface {
	Name() string
	// Validate 在保存配置时做静态校验。
	Validate(cfg Config, secret Secret) error
	// AuthorizeURL 拼出跳转地址。
	AuthorizeURL(cfg Config, secret Secret, p AuthorizeParams) (string, error)
	// Exchange 用 code 换 access_token 并拉取用户资料。
	Exchange(ctx context.Context, cfg Config, secret Secret, code, redirectURI string) (Identity, error)
}

var (
	regMu sync.RWMutex
	reg   = map[string]Provider{}
)

// Register 注册一个通道实现。
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

// ErrExchangeFailed 表示换 token 或拉资料失败。
var ErrExchangeFailed = errors.New("第三方登录换取用户信息失败")

// SafeRedirectTo 校验回调后要跳转的站内路径，挡掉开放重定向。
//
// 只接受以单个斜杠开头的相对路径，并且拒绝两种会被浏览器解释成协议相对 URL 的
// 写法：两个斜杠开头，以及斜杠后跟反斜杠（部分浏览器把反斜杠当斜杠）。
// 这里用 0x5c 表示反斜杠而不是写字符串字面量，避免转义层数一多就写错。
func SafeRedirectTo(raw string) string {
	const slash = "/"
	const backslash = string(rune(0x5c))
	s := strings.TrimSpace(raw)
	if s == "" {
		return slash
	}
	if !strings.HasPrefix(s, slash) {
		return slash
	}
	if strings.HasPrefix(s, slash+slash) || strings.HasPrefix(s, slash+backslash) {
		return slash
	}
	// 路径中间出现反斜杠同样可疑，一并拒绝。
	if strings.Contains(s, backslash) {
		return slash
	}
	// 控制字符可能被用来绕过前缀检查。
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return slash
		}
	}
	return s
}

// normalizeIdentity 做最后的兜底校验：没有 subject 的身份是无意义的。
func normalizeIdentity(id Identity) (Identity, error) {
	id.Subject = strings.TrimSpace(id.Subject)
	if id.Subject == "" {
		return id, fmt.Errorf("%w：平台没有返回用户唯一标识", ErrExchangeFailed)
	}
	id.UnionID = strings.TrimSpace(id.UnionID)
	id.Nickname = strings.TrimSpace(id.Nickname)
	id.AvatarURL = strings.TrimSpace(id.AvatarURL)
	id.Email = strings.ToLower(strings.TrimSpace(id.Email))
	return id, nil
}
