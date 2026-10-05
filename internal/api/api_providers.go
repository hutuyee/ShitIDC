package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/provider/baota"
	"github.com/hutuyee/ShitIDC/internal/provider/bthosts"
	"github.com/hutuyee/ShitIDC/internal/provider/custom"
	"github.com/hutuyee/ShitIDC/internal/provider/magiccube"
	"github.com/hutuyee/ShitIDC/internal/provider/nokvm"
	"github.com/hutuyee/ShitIDC/internal/provider/proxmox"
	"github.com/hutuyee/ShitIDC/internal/provider/virtualizor"
	"github.com/hutuyee/ShitIDC/internal/provider/wlkangle"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/security"
)

// Generic provider resolution for the admin console, mirroring the worker's
// dispatch (cmd/worker resolve): magiccube / proxmox / virtualizor.

// resolveProviderClient builds the provider implementation for a persisted
// upstream provider row (secret already decrypted).
func (a *App) resolveProviderClient(ctx context.Context, pv model.Provider, secret string) (provider.Provider, error) {
	switch strings.ToLower(pv.ProviderType) {
	case "magiccube":
		b, _ := json.Marshal(pv.Config)
		var saved struct {
			AuthMode     string          `json:"auth_mode"`
			TokenPrefix  string          `json:"token_prefix"`
			AllowPrivate bool            `json:"allow_private"`
			Paths        magiccube.Paths `json:"paths"`
		}
		_ = json.Unmarshal(b, &saved)
		return magiccube.New(magiccube.Config{BaseURL: pv.BaseURL, Username: pv.Username, APIKey: secret, AuthMode: saved.AuthMode, TokenPrefix: saved.TokenPrefix, AllowPrivate: saved.AllowPrivate, Paths: saved.Paths})
	case "proxmox":
		b, _ := json.Marshal(pv.Config)
		var cfg proxmox.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("proxmox config decode: %w", err)
		}
		cfg.BaseURL = pv.BaseURL
		cfg.APIToken = secret
		return proxmox.New(cfg)
	case "baota":
		b, _ := json.Marshal(pv.Config)
		var cfg baota.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("baota config decode: %w", err)
		}
		cfg.BaseURL = pv.BaseURL
		cfg.APIKey = secret
		return baota.New(cfg)
	case "virtualizor":
		b, _ := json.Marshal(pv.Config)
		var cfg virtualizor.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("virtualizor config decode: %w", err)
		}
		cfg.BaseURL = pv.BaseURL
		var keys struct {
			APIKey  string `json:"api_key"`
			APIPass string `json:"api_pass"`
		}
		if err := json.Unmarshal([]byte(secret), &keys); err != nil || keys.APIKey == "" {
			return nil, fmt.Errorf("virtualizor secret 需为 JSON {\"api_key\",\"api_pass\"}")
		}
		cfg.APIKey, cfg.APIPass = keys.APIKey, keys.APIPass
		return virtualizor.New(cfg)
	case "nokvm":
		b, _ := json.Marshal(pv.Config)
		var cfg nokvm.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("nokvm config decode: %w", err)
		}
		cfg.BaseURL = pv.BaseURL
		cfg.Token = secret
		return nokvm.New(cfg)
	case "wlkangle":
		b, _ := json.Marshal(pv.Config)
		var cfg wlkangle.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("wlkangle config decode: %w", err)
		}
		cfg.BaseURL = pv.BaseURL
		cfg.Token = secret
		return wlkangle.New(cfg)
	case "bthosts":
		b, _ := json.Marshal(pv.Config)
		var cfg bthosts.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("bthosts config decode: %w", err)
		}
		cfg.BaseURL = pv.BaseURL
		cfg.Token = secret
		return bthosts.New(cfg)
	case "custom":
		// 声明式上游（魔方插件导入产物）：规格在 config.spec，密钥即接口 token。
		if _, ok := pv.Config["spec"]; !ok {
			return nil, fmt.Errorf("custom 供应商缺少 config.spec 规格")
		}
		return custom.FromProvider(pv, secret)
	}
	return nil, fmt.Errorf("unsupported provider_type %q", pv.ProviderType)
}

// providerClientFor loads a persisted provider and builds its client.
func (a *App) providerClientFor(ctx context.Context, publicID string) (model.Provider, provider.Provider, error) {
	pv, encrypted, err := a.Store.GetProviderCredentials(ctx, publicID)
	if err != nil {
		return model.Provider{}, nil, err
	}
	if len(a.Cfg.MasterKey) == 0 {
		return model.Provider{}, nil, fmt.Errorf("服务器未配置 MASTER_KEY_BASE64")
	}
	secret, err := security.Decrypt(a.Cfg.MasterKey, encrypted)
	if err != nil {
		return model.Provider{}, nil, fmt.Errorf("decrypt provider secret: %w", err)
	}
	client, err := a.resolveProviderClient(ctx, pv, secret)
	if err != nil {
		return model.Provider{}, nil, err
	}
	return pv, client, nil
}

// proxmoxConfigFromInput extracts the typed fields the console form sends.
func proxmoxConfigFromInput(cfg map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"node", "api_token_id", "vm_type", "ostemplate", "storage", "bridge", "password"} {
		if v, ok := cfg[k].(string); ok && v != "" {
			out[k] = v
		}
	}
	for _, k := range []string{"template_vmid", "cores", "memory", "disk_gb"} {
		if v, ok := cfg[k].(float64); ok && v > 0 {
			out[k] = v
		}
	}
	if v, ok := cfg["allow_private"].(bool); ok {
		out["allow_private"] = v
	}
	return out
}

var providerTestTimeout = 20 * time.Second

// createInfraProvider stores a proxmox / virtualizor upstream: the typed
// fields arrive in config, the credential travels in the secret field.
func (a *App) createInfraProvider(c *gin.Context, p principal, name, providerType, baseURL, apiKey string, cfgIn map[string]any) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(baseURL) == "" || strings.TrimSpace(apiKey) == "" {
		httpx.Fail(c, 400, "PROVIDER_CONFIG_INVALID", "供应商名称、接口地址和 API Key 不能为空")
		return
	}
	configMap := map[string]any{}
	if cfgIn != nil {
		configMap = cfgIn
	}
	// Validate by building a throwaway client before persisting anything.
	pv := model.Provider{Name: name, ProviderType: providerType, BaseURL: baseURL, Config: configMap}
	if providerType == "proxmox" {
		cfg := proxmoxConfigFromInput(configMap)
		configMap = cfg
		pv.Config = cfg
	}
	probe, err := a.resolveProviderClient(c, pv, apiKey)
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_CONFIG_INVALID", err.Error())
		return
	}
	_ = probe
	secret, err := security.Encrypt(a.Cfg.MasterKey, apiKey)
	if err != nil {
		httpx.Fail(c, 500, "MASTER_KEY_INVALID", "MASTER_KEY_BASE64 未正确配置，无法安全保存上游密钥")
		return
	}
	v, err := a.Store.CreateProvider(c, strings.TrimSpace(name), providerType, strings.TrimSpace(baseURL), "", secret, configMap)
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "provider.create", "provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 201, v)
}
