// Package mail 的通道抽象（对应魔方 public/plugins/mail/）。
//
// 魔方把「怎么发邮件」做成插件：SMTP、阿里云邮件推送、赛邮、宝塔邮局……
// ShitIDC 早期只有内置 SMTP 一条路，这里把它扩展成注册表：
//
//   - 内置 SMTP（system_settings 里的历史配置）作为兜底通道继续可用；
//   - 其余通道（alimail / subemail / btmail / idcsmartmail / generic）走
//     mail_providers 表，凭据加密入库，后台可增删与设默认；
//   - 解析顺序是「启用中的通道优先，未配置通道时回退到内置 SMTP」，
//     因此老部署升级后行为不变。
//
// 通道实现保持无状态：渲染、签名、请求都在 Provider 内部完成。
package mail

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Message 是一封待发送的邮件。正文是 HTML（与既有 SMTP 发送一致）。
type Message struct {
	To      string
	Subject string
	HTML    string
}

// Config 是通道的非敏感配置，敏感项放 Secret。
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

// Secret 是通道凭据（如 access_key_id / access_key_secret / 密码）。
type Secret map[string]string

// Get 安全地取一个凭据项。
func (s Secret) Get(key string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s[key])
}

// ErrNotConfigured 表示通道缺少必填配置。
var ErrNotConfigured = errors.New("邮件通道未配置完整")

// Sender 是一个邮件发送通道实现。
type Sender interface {
	// Name 是通道标识，与 mail_providers.provider 对应。
	Name() string
	// Validate 在保存配置时做一次静态校验，不必真的发信。
	Validate(cfg Config, secret Secret) error
	// Send 真正把邮件发出去。
	Send(ctx context.Context, cfg Config, secret Secret, msg Message) error
}

var (
	regMu sync.RWMutex
	reg   = map[string]Sender{}
)

// Register 注册一个通道实现，供 init 调用。
func Register(p Sender) {
	regMu.Lock()
	defer regMu.Unlock()
	reg[strings.ToLower(p.Name())] = p
}

// Get 按标识取通道实现。
func Get(name string) (Sender, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := reg[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// Names 列出已注册的通道标识，便于后台下拉展示。
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

// truncateBody 控制错误信息里带上的响应长度。
func truncateBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
