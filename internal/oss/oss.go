// Package oss 是对象存储抽象（对应魔方 public/plugins/oss/）。
//
// 参考实现只有一个通道 TencentcloudOss：核心把上传的文件交给它转存到对象存储，
// 下载时由它返回带签名的临时地址（+3 分钟）。这里保持同样的契约：探活、
// 上传、存在性检查、签名下载；具体协议由通道实现承载。
//
// 已实现：腾讯云对象存储（TencentCloud COS）。
package oss

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config 是通道的公开配置：非敏感的放 Fields。
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

// Secret 是通道凭据；具体字段由通道自己定义（腾讯云是 secret_id / secret_key）。
type Secret map[string]string

// Get 安全地取一个凭据项。
func (s Secret) Get(key string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s[key])
}

// ErrNotConfigured 表示通道缺少必填配置。
var ErrNotConfigured = errors.New("对象存储通道未配置完整")

// Provider 是一个对象存储通道实现。
type Provider interface {
	// Name 是通道标识（如 tencentcloud_oss）。
	Name() string
	// Validate 校验必填配置与凭据是否完整。
	Validate(cfg Config, secret Secret) error
	// TestLink 探活：校验桶可访问。
	TestLink(ctx context.Context, cfg Config, secret Secret) error
	// Upload 上传对象；acl 取 "public-read" 或 "private"。
	Upload(ctx context.Context, cfg Config, secret Secret, key string, body []byte, contentType, acl string) error
	// Exists 判断对象是否存在。
	Exists(ctx context.Context, cfg Config, secret Secret, key string) (bool, error)
	// SignedURL 返回有效期内的签名下载地址。
	SignedURL(ctx context.Context, cfg Config, secret Secret, key string, ttl time.Duration) (string, error)
}

var (
	regMu sync.RWMutex
	reg   = map[string]Provider{}
)

// Register 注册通道实现（由具体通道的 init 调用）。
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	reg[strings.ToLower(strings.TrimSpace(p.Name()))] = p
}

// Get 按标识取通道实现。
func Get(name string) (Provider, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := reg[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// Names 返回全部已注册通道标识（排序）。
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for k := range reg {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
