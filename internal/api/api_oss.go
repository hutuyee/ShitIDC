package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/oss"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 对象存储（对应魔方 public/plugins/oss/ 的 TencentcloudOss）。
//
// 与短信 / 邮件通道同构：多条配置、一个默认、凭据加密入库。配置后工单附件
// 上传会转存对象存储、下载返回签名地址；未配置时维持本机存储。

// ossTarget 是一次可用的对象存储目标。
type ossTarget struct {
	provider store.OssProvider
	impl     oss.Provider
	cfg      oss.Config
	secret   oss.Secret
}

// ossSecret 解密通道凭据。
func (a *App) ossSecret(enc string) (oss.Secret, error) {
	if strings.TrimSpace(enc) == "" {
		return oss.Secret{}, nil
	}
	if len(a.Cfg.MasterKey) == 0 {
		return nil, errors.New("服务器未配置 MASTER_KEY_BASE64，无法解密对象存储凭据")
	}
	plain, err := security.Decrypt(a.Cfg.MasterKey, enc)
	if err != nil {
		return nil, errors.New("解密对象存储凭据失败")
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, errors.New("对象存储凭据不是合法 JSON")
	}
	return oss.Secret(out), nil
}

// activeOSSTarget 读取启用中的对象存储通道；未配置返回 (nil, nil)。
func (a *App) activeOSSTarget(ctx context.Context) (*ossTarget, error) {
	pv, secretEnc, err := a.Store.ActiveOssProvider(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	impl, ok := oss.Get(pv.Provider)
	if !ok {
		return nil, errors.New("未知的对象存储通道：" + pv.Provider)
	}
	secret, err := a.ossSecret(secretEnc)
	if err != nil {
		return nil, err
	}
	return &ossTarget{provider: pv, impl: impl, cfg: oss.Config{Provider: pv.Provider, Fields: pv.Config}, secret: secret}, nil
}

// ---- 管理端：对象存储通道 ----

// adminListOssProviders 列出所有对象存储通道。
func (a *App) adminListOssProviders(c *gin.Context) {
	items, err := a.Store.ListOssProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "OSS_PROVIDERS_FAILED", "读取对象存储通道失败")
		return
	}
	httpx.OK(c, 200, map[string]any{
		"providers": items,
		"available": oss.Names(),
	})
}

// adminCreateOssProvider 新增对象存储通道。
// secret 是通道凭据 JSON（如 {"secret_id":"..","secret_key":".."}）。
func (a *App) adminCreateOssProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Name      string            `json:"name"`
		Provider  string            `json:"provider"`
		Config    map[string]string `json:"config"`
		Secret    map[string]string `json:"secret"`
		IsDefault bool              `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if in.Name == "" || in.Provider == "" {
		httpx.Fail(c, 400, "INVALID_OSS_PROVIDER", "通道名称与类型不能为空")
		return
	}
	impl, ok := oss.Get(in.Provider)
	if !ok {
		httpx.Fail(c, 400, "OSS_PROVIDER_UNKNOWN", "未知的对象存储通道："+in.Provider)
		return
	}
	cfg := oss.Config{Provider: in.Provider, Fields: in.Config}
	secret := oss.Secret(in.Secret)
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 400, "OSS_PROVIDER_INVALID", err.Error())
		return
	}

	secretJSON, err := json.Marshal(secret)
	if err != nil {
		httpx.Fail(c, 400, "OSS_PROVIDER_INVALID", "凭据格式错误")
		return
	}
	enc := ""
	if len(secret) > 0 {
		if len(a.Cfg.MasterKey) == 0 {
			httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法加密对象存储凭据")
			return
		}
		enc, err = security.Encrypt(a.Cfg.MasterKey, string(secretJSON))
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密对象存储凭据失败")
			return
		}
	}
	v, err := a.Store.CreateOssProvider(c, in.Name, in.Provider, in.Config, enc, in.IsDefault)
	if err != nil {
		httpx.Fail(c, 400, "OSS_PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oss_provider.create", "oss_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "provider": v.Provider})
	httpx.OK(c, 201, v)
}

// adminSetDefaultOssProvider 把某个通道设为默认。
func (a *App) adminSetDefaultOssProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.SetOssProviderDefault(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "OSS_PROVIDER_NOT_FOUND", "对象存储通道不存在")
			return
		}
		httpx.Fail(c, 400, "OSS_PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oss_provider.set_default", "oss_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteOssProvider 删除通道。
func (a *App) adminDeleteOssProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteOssProvider(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "OSS_PROVIDER_NOT_FOUND", "对象存储通道不存在")
			return
		}
		httpx.Fail(c, 400, "OSS_PROVIDER_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oss_provider.delete", "oss_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminTestOssProvider 探活指定通道（HEAD 桶）。
func (a *App) adminTestOssProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	pv, secretEnc, err := a.Store.GetOssProvider(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "OSS_PROVIDER_NOT_FOUND", "对象存储通道不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "OSS_PROVIDER_READ_FAILED", "读取对象存储通道失败")
		return
	}
	impl, ok := oss.Get(pv.Provider)
	if !ok {
		httpx.Fail(c, 400, "OSS_PROVIDER_UNKNOWN", "未知的对象存储通道："+pv.Provider)
		return
	}
	secret, err := a.ossSecret(secretEnc)
	if err != nil {
		httpx.Fail(c, 500, "OSS_SECRET_INVALID", err.Error())
		return
	}
	cfg := oss.Config{Provider: pv.Provider, Fields: pv.Config}
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 400, "OSS_PROVIDER_INVALID", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	terr := impl.TestLink(ctx, cfg, secret)
	a.Store.TouchOssProvider(c, pv.PublicID, terr == nil, errText(terr))
	if terr != nil {
		httpx.Fail(c, 502, "OSS_TEST_FAILED", terr.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "oss_provider.test", "oss_provider", pv.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
