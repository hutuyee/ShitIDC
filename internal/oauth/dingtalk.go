// 钉钉扫码登录（对应魔方 CBAP 插件包 public/plugins/oauth/Dingtalk）。
//
//	授权页  https://login.dingtalk.com/oauth2/auth
//	换 token https://api.dingtalk.com/v1.0/oauth2/userAccessToken（JSON 请求体）
//	用户资料 https://api.dingtalk.com/v1.0/contact/users/me（x-acs-dingtalk-access-token 头）
//
// 回调参数名是 authCode 而不是 code（新版 OAuth2 约定），接入层两种都读；
// 这里 Exchange 收到的是已经归一化后的 code。
package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Dingtalk 实现钉钉登录。
type Dingtalk struct {
	// 端点可覆盖，便于测试指向假服务器。
	AuthorizeEndpoint string
	TokenEndpoint     string
	UserInfoEndpoint  string
}

// NewDingtalk 构造通道。
func NewDingtalk() *Dingtalk {
	return &Dingtalk{
		AuthorizeEndpoint: "https://login.dingtalk.com/oauth2/auth",
		TokenEndpoint:     "https://api.dingtalk.com/v1.0/oauth2/userAccessToken",
		UserInfoEndpoint:  "https://api.dingtalk.com/v1.0/contact/users/me",
	}
}

// Name 返回通道标识。
func (d *Dingtalk) Name() string { return "dingtalk" }

func init() { Register(NewDingtalk()) }

// Validate 检查必填配置。
func (d *Dingtalk) Validate(cfg Config, secret Secret) error {
	if cfg.Field("client_id") == "" {
		return fmt.Errorf("钉钉登录需要 Client ID（原 AppKey）")
	}
	if secret.Get("client_secret") == "" {
		return fmt.Errorf("钉钉登录需要 Client Secret（原 AppSecret）")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址。
func (d *Dingtalk) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("client_id")) == "" {
		return "", fmt.Errorf("钉钉登录需要 Client ID（原 AppKey）")
	}
	q := url.Values{}
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("response_type", "code")
	q.Set("client_id", cfg.Field("client_id"))
	q.Set("scope", firstNonEmpty(cfg.Field("scope"), "openid"))
	q.Set("state", p.State)
	q.Set("prompt", "consent")
	return d.AuthorizeEndpoint + "?" + q.Encode(), nil
}

// Exchange 用 code 换用户级 accessToken 并拉取资料。
func (d *Dingtalk) Exchange(ctx context.Context, cfg Config, secret Secret, code, _ string) (Identity, error) {
	if err := d.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	tok, err := httpPostJSON(ctx, d.TokenEndpoint, map[string]string{
		"clientId":     cfg.Field("client_id"),
		"clientSecret": secret.Get("client_secret"),
		"code":         code,
		"grantType":    "authorization_code",
	}, nil)
	if err != nil {
		return Identity{}, err
	}
	accessToken := stringField(tok, "accessToken")
	if accessToken == "" {
		return Identity{}, fmt.Errorf("%w：钉钉没有返回 accessToken（%s）", ErrExchangeFailed, errField(tok))
	}
	profile, err := httpGetJSON(ctx, d.UserInfoEndpoint, map[string]string{
		"x-acs-dingtalk-access-token": accessToken,
	})
	if err != nil {
		return Identity{}, err
	}
	openID := stringField(profile, "openId")
	if openID == "" {
		return Identity{}, fmt.Errorf("%w：钉钉没有返回 openId（%s）", ErrExchangeFailed, errField(profile))
	}
	return normalizeIdentity(Identity{
		Provider:  "dingtalk",
		Subject:   openID,
		UnionID:   stringField(profile, "unionId"),
		Nickname:  stringField(profile, "nick"),
		AvatarURL: stringField(profile, "avatarUrl"),
		Email:     stringField(profile, "email"),
		Raw:       profile,
	})
}
