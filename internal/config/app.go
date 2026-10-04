package config

import (
	"errors"
	"os"
	"strings"
)

// ErrMasterKeyLength is returned when MASTER_KEY_BASE64 does not decode to
// exactly 32 bytes (AES-256).
var ErrMasterKeyLength = errors.New("MASTER_KEY_BASE64 must decode to exactly 32 bytes")

type AppConfig struct {
	Env               string
	HTTPAddr          string
	PublicBaseURL     string
	BootstrapEmail    string
	BootstrapPassword string
	TrustedProxies    []string
	// AdminPath 是后台挂载路径，空值时用 defaultAdminPath。
	// 它同时作为环境变量名 ADMIN_PATH 暴露，方便每个部署换一个。
	AdminPath string
}

func loadApp() (AppConfig, error) {
	return AppConfig{
		Env:               env("APP_ENV", "development"),
		HTTPAddr:          env("HTTP_ADDR", ":8080"),
		PublicBaseURL:     env("PUBLIC_BASE_URL", "http://localhost:8080"),
		BootstrapEmail:    os.Getenv("BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		TrustedProxies:    splitList(os.Getenv("TRUSTED_PROXIES")),
		AdminPath:         env("ADMIN_PATH", defaultAdminPath),
	}, nil
}

func splitList(raw string) []string {
	out := []string{}
	for _, item := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
