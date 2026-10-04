package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type SecurityConfig struct {
	MasterKey          []byte
	CookieSecure       bool
	CookieDomain       string
	SessionTTL         time.Duration
	RateLimitPerMinute int
	// TokenRateLimitPerMinute caps requests per API token per minute.
	// 0 disables the per-token limit (global IP limit still applies).
	TokenRateLimitPerMinute int
}

func loadSecurity() (SecurityConfig, error) {
	// MASTER_KEY_FILE supports Docker secrets / mounted key files (KMS-lite);
	// MASTER_KEY_BASE64 stays the primary path.
	rawKey := os.Getenv("MASTER_KEY_BASE64")
	if rawKey == "" && os.Getenv("MASTER_KEY_FILE") != "" {
		b, ferr := os.ReadFile(os.Getenv("MASTER_KEY_FILE"))
		if ferr != nil {
			return SecurityConfig{}, ferr
		}
		rawKey = strings.TrimSpace(string(b))
	}
	key, err := MasterKeyBase64(rawKey)
	if err != nil {
		return SecurityConfig{}, err
	}
	cookieSecure, _ := strconv.ParseBool(env("COOKIE_SECURE", "false"))
	sessionHours, _ := strconv.Atoi(env("SESSION_TTL_HOURS", "168"))
	rate, _ := strconv.Atoi(env("RATE_LIMIT_PER_MINUTE", "120"))
	tokenRate, _ := strconv.Atoi(env("TOKEN_RATE_LIMIT_PER_MINUTE", "300"))
	return SecurityConfig{
		MasterKey:               key,
		CookieSecure:            cookieSecure,
		CookieDomain:            os.Getenv("COOKIE_DOMAIN"),
		SessionTTL:              time.Duration(sessionHours) * time.Hour,
		RateLimitPerMinute:      rate,
		TokenRateLimitPerMinute: tokenRate,
	}, nil
}
