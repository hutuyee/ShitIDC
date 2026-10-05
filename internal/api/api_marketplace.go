package api

// 插件市场与魔方插件导入的管理端接口。
//
// 市场 = 一个可自托管的 JSON 索引 + 若干安装包。安装扩展/主题复用既有
// 上传流程；安装 zjmf-plugin 条目则把魔方 server 插件转换成声明式上游
// 规格（internal/zjmfimport）并创建 custom 供应商。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/archutil"
	"github.com/hutuyee/ShitIDC/internal/extension"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/marketplace"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/provider/custom"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
	"github.com/hutuyee/ShitIDC/internal/theme"
	"github.com/hutuyee/ShitIDC/internal/zjmfimport"
)

// installExtensionZip 校验并落盘一个扩展包（上传与市场安装共用）。
func (a *App) installExtensionZip(ctx context.Context, r io.Reader) (model.Extension, error) {
	files, err := archutil.SafeReadZip(r, 20<<20, 100)
	if err != nil {
		return model.Extension{}, err
	}
	manifestRaw, ok := files["extension.json"]
	if !ok {
		return model.Extension{}, errors.New("扩展包缺少 extension.json")
	}
	manifest, err := extension.ParseManifest(manifestRaw)
	if err != nil {
		return model.Extension{}, err
	}
	wasm, ok := files[manifest.Entry]
	if !ok {
		return model.Extension{}, errors.New("扩展包缺少声明的 entry: " + manifest.Entry)
	}
	dir := filepath.Join(a.Cfg.Storage.Dir, "extensions", manifest.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.Extension{}, errors.New("扩展目录创建失败")
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.Entry), wasm, 0o644); err != nil {
		return model.Extension{}, errors.New("扩展保存失败")
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), manifestRaw, 0o644); err != nil {
		return model.Extension{}, errors.New("扩展保存失败")
	}
	return a.Store.UpsertExtension(ctx, manifest.Name, manifest.Version, manifest.Description, filepath.Join(dir, manifest.Entry), manifest.Permissions, false)
}

// ---- 市场设置 ----

func (a *App) adminMarketplaceSettings(c *gin.Context) {
	v, err := a.Store.GetMarketplaceSettings(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取市场设置失败")
		return
	}
	if v.IndexURL == "" {
		v.IndexURL = marketplace.DefaultIndexURL
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSaveMarketplaceSettings(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		IndexURL string `json:"index_url"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.IndexURL = strings.TrimSpace(in.IndexURL)
	if in.IndexURL == marketplace.DefaultIndexURL {
		in.IndexURL = "" // 与默认一致就不落库，官方索引升级时自动跟随
	}
	if err := a.Store.SaveMarketplaceSettings(c, store.MarketplaceSettings{IndexURL: in.IndexURL}); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存市场设置失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "marketplace.settings", "marketplace", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminMarketplaceIndex 拉取市场索引并标注本机安装状态。
func (a *App) adminMarketplaceIndex(c *gin.Context) {
	settings, err := a.Store.GetMarketplaceSettings(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取市场设置失败")
		return
	}
	indexURL := settings.IndexURL
	if override := strings.TrimSpace(c.Query("url")); override != "" {
		indexURL = override
	}
	idx, err := marketplace.FetchIndex(c, indexURL)
	if err != nil {
		httpx.Fail(c, 502, "MARKETPLACE_UNREACHABLE", err.Error())
		return
	}

	// 本机已安装状态：扩展看注册表、主题看目录、魔方导入看 custom 供应商。
	exts, _ := a.Store.ListExtensions(c)
	extVer := map[string]string{}
	for _, e := range exts {
		extVer[e.Name] = e.Version
	}
	themes, _ := theme.List(a.Cfg.Storage.ThemeDir())
	themeIDs := map[string]bool{}
	for _, th := range themes {
		themeIDs[th.ID] = true
	}
	providers, _ := a.Store.ListProviders(c)
	importedSlug := map[string]string{} // slug → provider public id
	for _, pv := range providers {
		if strings.EqualFold(pv.ProviderType, "custom") {
			if slug := providerSpecSlug(pv); slug != "" {
				importedSlug[slug] = pv.PublicID
			}
		}
	}

	items := []map[string]any{}
	for _, it := range idx.Items {
		entry := map[string]any{"kind": it.Kind, "name": it.Name, "version": it.Version, "title": it.Title, "description": it.Description, "author": it.Author, "homepage": it.Homepage, "permissions": it.Permissions, "tags": it.Tags, "size": it.Size, "download_url": it.DownloadURL}
		switch it.Kind {
		case marketplace.KindExtension:
			if v, ok := extVer[it.Name]; ok {
				entry["installed"] = true
				entry["installed_version"] = v
				entry["upgradable"] = v != it.Version
			}
		case marketplace.KindTheme:
			if themeIDs[it.Name] {
				entry["installed"] = true
			}
		case marketplace.KindZJMFPlugin:
			if pid, ok := importedSlug[it.Name]; ok {
				entry["installed"] = true
				entry["provider_id"] = pid
			}
		}
		items = append(items, entry)
	}
	httpx.OK(c, 200, map[string]any{"source_url": idx.SourceURL, "updated_at": idx.UpdatedAt, "items": items})
}

// adminMarketplaceInstall 从市场安装一个包。
//   - extension / theme：直接安装。
//   - zjmf-plugin：转换规格并创建 custom 供应商，body 里需带 base_url 与
//     token（上游接口地址与密钥），和"上传导入"保持同一套配置义务。
func (a *App) adminMarketplaceInstall(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		URL     string `json:"url"` // 直接指定下载地址（配合 sha256）
		SHA     string `json:"sha256"`
		BaseURL string `json:"base_url"` // zjmf-plugin 用
		Token   string `json:"token"`    // zjmf-plugin 用
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	in.Name = strings.TrimSpace(in.Name)

	item := marketplace.Item{Kind: in.Kind, Name: in.Name, DownloadURL: in.URL, SHA256: in.SHA}
	if in.URL == "" {
		settings, err := a.Store.GetMarketplaceSettings(c)
		if err != nil {
			httpx.Fail(c, 500, "INTERNAL_ERROR", "读取市场设置失败")
			return
		}
		idx, err := marketplace.FetchIndex(c, settings.IndexURL)
		if err != nil {
			httpx.Fail(c, 502, "MARKETPLACE_UNREACHABLE", err.Error())
			return
		}
		found, err := idx.Find(in.Kind, in.Name)
		if err != nil {
			httpx.Fail(c, 404, "MARKETPLACE_ITEM_NOT_FOUND", err.Error())
			return
		}
		item = found
	}

	data, err := marketplace.Download(c, item)
	if err != nil {
		httpx.Fail(c, 400, "MARKETPLACE_DOWNLOAD_FAILED", err.Error())
		return
	}

	switch in.Kind {
	case marketplace.KindExtension:
		v, err := a.installExtensionZip(c, strings.NewReader(string(data)))
		if err != nil {
			httpx.Fail(c, 400, "PACKAGE_INVALID", err.Error())
			return
		}
		_ = a.Store.Audit(c, p.User.ID, "marketplace.install", "extension", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": item.Name, "version": item.Version})
		httpx.OK(c, 201, map[string]any{"kind": in.Kind, "extension": v})
	case marketplace.KindTheme:
		pkg, err := theme.Install(a.Cfg.Storage.ThemeDir(), data)
		if err != nil {
			httpx.Fail(c, 400, "THEME_INVALID", err.Error())
			return
		}
		_ = a.Store.Audit(c, p.User.ID, "marketplace.install", "theme", pkg.ID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": item.Name, "version": item.Version})
		httpx.OK(c, 201, map[string]any{"kind": in.Kind, "theme": pkg})
	case marketplace.KindZJMFPlugin:
		res, err := zjmfimport.FromZipReader(strings.NewReader(string(data)))
		if err != nil {
			httpx.Fail(c, 400, "ZJMF_PLUGIN_INVALID", err.Error())
			return
		}
		pv, err := a.createZJMFProvider(c, p.User.ID, res.Spec, strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.Token))
		if err != nil {
			httpx.Fail(c, 400, "ZJMF_IMPORT_FAILED", err.Error())
			return
		}
		httpx.OK(c, 201, map[string]any{"kind": in.Kind, "provider": pv, "spec": res.Spec})
	default:
		httpx.Fail(c, 400, "INVALID_KIND", "未知的包类型 "+in.Kind)
	}
}

// ---- 魔方插件导入 ----

// providerSpecSlug 从 custom 供应商的 config 里取规格 slug（未导入规格时为空）。
func providerSpecSlug(pv model.Provider) string {
	raw, ok := pv.Config["spec"]
	if !ok {
		return ""
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	var probe struct {
		Slug string `json:"slug"`
	}
	if json.Unmarshal(b, &probe) != nil {
		return ""
	}
	return probe.Slug
}

// createZJMFProvider 把转换出的规格落成 custom 供应商。
// base_url 与 token 必填：导入即配置，之后即可"测试连接"。
func (a *App) createZJMFProvider(c *gin.Context, adminID int64, spec *zjmfimport.Spec, baseURL, token string) (model.Provider, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(token) == "" {
		return model.Provider{}, errors.New("请填写上游接口地址与接口密钥（对应魔方接口设置里的 Hash/密码）")
	}
	// 落库前用运行时构建校验规格 + 连接参数的合法性。
	if _, err := custom.New(*spec, custom.Options{BaseURL: baseURL, Token: token, AllowPrivate: true}); err != nil {
		return model.Provider{}, err
	}
	name := spec.DisplayName
	if name == "" {
		name = spec.Slug
	}
	name = "魔方-" + name
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return model.Provider{}, err
	}
	cfg := map[string]any{"spec": json.RawMessage(specJSON), "allow_private": true, "imported_from": "zjmf-plugin"}
	enc, err := security.Encrypt(a.Cfg.MasterKey, token)
	if err != nil {
		return model.Provider{}, errors.New("MASTER_KEY_BASE64 未正确配置，无法保存接口密钥")
	}
	pv, err := a.Store.CreateProvider(c, name, "custom", baseURL, "", enc, cfg)
	if err != nil {
		return model.Provider{}, err
	}
	_ = a.Store.Audit(c, adminID, "provider.zjmf_import", "provider", pv.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"slug": spec.Slug, "source_version": spec.SourceVersion, "warnings": spec.Warnings})
	return pv, nil
}

// adminZJMFImport 上传一个魔方 server 插件 zip：apply=false 只做转换预览
// （返回规格与警告），apply=true 创建 custom 供应商（需 base_url 与 token）。
func (a *App) adminZJMFImport(c *gin.Context) {
	p, _ := getPrincipal(c)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, 400, "FILE_REQUIRED", "请上传魔方插件 zip")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		httpx.Fail(c, 400, "FILE_INVALID", "文件读取失败")
		return
	}
	defer f.Close()
	res, err := zjmfimport.FromZipReader(f)
	if err != nil {
		httpx.Fail(c, 400, "ZJMF_PLUGIN_INVALID", err.Error())
		return
	}
	if c.PostForm("apply") != "true" {
		httpx.OK(c, 200, map[string]any{"spec": res.Spec})
		return
	}
	if res.Spec.Auth.Scheme == zjmfimport.SchemeUnsupported {
		httpx.Fail(c, 400, "ZJMF_PLUGIN_UNSUPPORTED", "该插件的认证方式无法转换（详见规格警告），请使用 ShitIDC 内置的对应供应商")
		return
	}
	if _, ok := res.Spec.Actions[zjmfimport.ActionCreate]; !ok {
		httpx.Fail(c, 400, "ZJMF_PLUGIN_NO_CREATE", "该插件没有可转换的开通动作（_CreateAccount），导入后无法自动开通")
		return
	}
	pv, err := a.createZJMFProvider(c, p.User.ID, res.Spec, strings.TrimSpace(c.PostForm("base_url")), strings.TrimSpace(c.PostForm("token")))
	if err != nil {
		httpx.Fail(c, 400, "ZJMF_IMPORT_FAILED", err.Error())
		return
	}
	httpx.OK(c, 201, map[string]any{"provider": pv, "spec": res.Spec})
}

// adminGetProviderSpec 查看 custom 供应商的规格。
func (a *App) adminGetProviderSpec(c *gin.Context) {
	pv, _, err := a.Store.GetProviderCredentials(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 404, "PROVIDER_NOT_FOUND", "供应商不存在")
		return
	}
	if !strings.EqualFold(pv.ProviderType, "custom") {
		httpx.Fail(c, 400, "NOT_CUSTOM_PROVIDER", "只有 custom 供应商有可编辑规格")
		return
	}
	httpx.OK(c, 200, pv.Config["spec"])
}

// adminUpdateProviderSpec 更新 custom 供应商的规格（管理员可补充转换器
// 没识别出的路径/参数）。只做规格级校验；连接性由"测试连接"另行验证。
func (a *App) adminUpdateProviderSpec(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	var spec zjmfimport.Spec
	if err := json.Unmarshal(in.Spec, &spec); err != nil {
		httpx.Fail(c, 400, "SPEC_INVALID", "规格不是合法 JSON: "+err.Error())
		return
	}
	spec.Normalize()
	if err := spec.Validate(); err != nil {
		httpx.Fail(c, 400, "SPEC_INVALID", err.Error())
		return
	}

	pv, _, err := a.Store.GetProviderCredentials(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 404, "PROVIDER_NOT_FOUND", "供应商不存在")
		return
	}
	if !strings.EqualFold(pv.ProviderType, "custom") {
		httpx.Fail(c, 400, "NOT_CUSTOM_PROVIDER", "只有 custom 供应商有可编辑规格")
		return
	}
	cfg := pv.Config
	specJSON, _ := json.Marshal(spec)
	cfg["spec"] = json.RawMessage(specJSON)
	if err := a.Store.UpdateProviderConfigOnly(c, c.Param("id"), cfg); err != nil {
		httpx.Fail(c, 400, "PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "provider.spec_update", "provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"slug": spec.Slug})
	httpx.OK(c, 200, map[string]any{"spec": spec})
}
