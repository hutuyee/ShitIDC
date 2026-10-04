// Package config loads process configuration. One file per concern
// (第一阶段: app / database / redis / security / mail / storage) — never
// pile every setting into a single blob.
package config

import (
	"encoding/base64"
	"strings"
	"time"
)

// Config aggregates every subsystem's settings.
type Config struct {
	App      AppConfig
	DB       DatabaseConfig
	Redis    RedisConfig
	Security SecurityConfig
	Mail     MailConfig
	Storage  StorageConfig

	// Flattened aliases kept for the existing call sites (worker/scheduler/api).
	AppEnv             string
	HTTPAddr           string
	PublicBaseURL      string
	PostgresDSN        string
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	CookieSecure       bool
	CookieDomain       string
	MasterKey          []byte
	SessionTTL         time.Duration
	RateLimitPerMinute int
	BootstrapEmail     string
	BootstrapPassword  string
	TrustedProxies     []string
	// AdminPath 是后台 API 的挂载前缀（默认 /admin）。
	// 把它做成可配置是为了两件事：一是让默认部署不那么容易被扫到，
	// 二是允许反向代理按自己的规则改写路径。改这里的同时要把前台
	// VITE_ADMIN_PATH 改成同样的值，否则前端会调到不存在的地址。
	AdminPath string
}

// Load reads and validates the environment. Returns an error when a secret
// is malformed; secrets never live in config files (env / Docker secrets /
// Vault only).
func Load() (Config, error) {
	app, err := loadApp()
	if err != nil {
		return Config{}, err
	}
	sec, err := loadSecurity()
	if err != nil {
		return Config{}, err
	}
	mail := loadMail()
	storage := loadStorage()
	redis := loadRedis()
	db := loadDatabase()

	return Config{
		App:      app,
		DB:       db,
		Redis:    redis,
		Security: sec,
		Mail:     mail,
		Storage:  storage,

		AppEnv:             app.Env,
		HTTPAddr:           app.HTTPAddr,
		PublicBaseURL:      app.PublicBaseURL,
		BootstrapEmail:     app.BootstrapEmail,
		BootstrapPassword:  app.BootstrapPassword,
		TrustedProxies:     app.TrustedProxies,
		PostgresDSN:        db.DSN,
		RedisAddr:          redis.Addr,
		RedisPassword:      redis.Password,
		RedisDB:            redis.DB,
		CookieSecure:       sec.CookieSecure,
		CookieDomain:       sec.CookieDomain,
		MasterKey:          sec.MasterKey,
		SessionTTL:         sec.SessionTTL,
		RateLimitPerMinute: sec.RateLimitPerMinute,
		AdminPath:          NormalizeAdminPath(app.AdminPath),
	}, nil
}

// defaultAdminPath 是后台 API 的默认前缀。
//
// 用 /admin-panel 而不是 /admin：默认值就不该是最容易被扫到的那个。
// /admin 仍然作为兼容前缀挂载（见 api.New），老链接不会失效。
const defaultAdminPath = "/admin-panel"

// NormalizeAdminPath 清洗后台路径：必须是单段、不带尾斜杠的绝对路径。
// 导出是为了让 NewRouter 也能用：Config 有可能不是经 Load() 构造的（例如测试），
// 空值会让后台路由挂到根路径上，与用户路由直接冲突。
// 拒绝嵌套路径与查询串，避免出现「配错了但表面上还能用」的情况。
func NormalizeAdminPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return defaultAdminPath
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if p == "" || p == "/" {
		return defaultAdminPath
	}
	// 只允许单段：出现第二个斜杠说明配错了（例如写成了 /x/admin）。
	if strings.Contains(strings.TrimPrefix(p, "/"), "/") {
		return defaultAdminPath
	}
	for _, r := range p {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return defaultAdminPath
		}
	}
	return p
}

// MasterKeyBase64 decodes a required-32-byte base64 master key.
func MasterKeyBase64(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(decoded) != 32 {
		return nil, ErrMasterKeyLength
	}
	return decoded, nil
}
