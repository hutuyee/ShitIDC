// Google 登录（对应魔方 CBAP 插件包 public/plugins/oauth/Google）。
//
//	授权页  https://accounts.google.com/o/oauth2/v2/auth
//	换 token https://www.googleapis.com/oauth2/v4/token（form 请求体）
//	用户资料 https://www.googleapis.com/oauth2/v2/userinfo（Bearer 头）
//
// 主键用 userinfo 的 id（Google 账户稳定标识），邮箱只作为建号参考。
package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Google 实现 Google 登录。
type Google struct {
	// 端点可覆盖，便于测试指向假服务器。
	AuthorizeEndpoint string
	TokenEndpoint     string
	UserInfoEndpoint  string
}

// NewGoogle 构造通道。
func NewGoogle() *Google {
	return &Google{
		AuthorizeEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenEndpoint:     "https://www.googleapis.com/oauth2/v4/token",
		UserInfoEndpoint:  "https://www.googleapis.com/oauth2/v2/userinfo",
	}
}

// Name 返回通道标识。
func (g *Google) Name() string { return "google" }

func init() { Register(NewGoogle()) }

// Validate 检查必填配置。
func (g *Google) Validate(cfg Config, secret Secret) error {
	if cfg.Field("client_id") == "" {
		return fmt.Errorf("Google 登录需要 Client ID")
	}
	if secret.Get("client_secret") == "" {
		return fmt.Errorf("Google 登录需要 Client Secret")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址。参数与参考插件一致（offline + consent）。
func (g *Google) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("client_id")) == "" {
		return "", fmt.Errorf("Google 登录需要 Client ID")
	}
	q := url.Values{}
	q.Set("client_id", cfg.Field("client_id"))
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("access_type", "offline")
	q.Set("response_type", "code")
	q.Set("scope", firstNonEmpty(cfg.Field("scope"), "profile email"))
	q.Set("state", p.State)
	q.Set("include_granted_scopes", "true")
	q.Set("prompt", "consent")
	return g.AuthorizeEndpoint + "?" + q.Encode(), nil
}

// Exchange 用 code 换 access_token 并拉取用户资料。
func (g *Google) Exchange(ctx context.Context, cfg Config, secret Secret, code, redirectURI string) (Identity, error) {
	if err := g.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", cfg.Field("client_id"))
	form.Set("client_secret", secret.Get("client_secret"))
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	tok, err := httpPostForm(ctx, g.TokenEndpoint, form, nil)
	if err != nil {
		return Identity{}, err
	}
	accessToken := stringField(tok, "access_token")
	if accessToken == "" {
		return Identity{}, fmt.Errorf("%w：Google 没有返回 access_token（%s）", ErrExchangeFailed, errField(tok))
	}
	profile, err := httpGetJSON(ctx, g.UserInfoEndpoint, map[string]string{
		"Authorization": "Bearer " + accessToken,
	})
	if err != nil {
		return Identity{}, err
	}
	subject := stringField(profile, "id")
	if subject == "" {
		return Identity{}, fmt.Errorf("%w：Google 没有返回用户 id", ErrExchangeFailed)
	}
	return normalizeIdentity(Identity{
		Provider:  "google",
		Subject:   subject,
		Nickname:  stringField(profile, "name", "email"),
		AvatarURL: stringField(profile, "picture"),
		Email:     stringField(profile, "email"),
		Raw:       profile,
	})
}
