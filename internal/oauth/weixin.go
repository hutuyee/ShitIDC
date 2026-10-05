// 微信开放平台扫码登录（对应魔方 public/plugins/oauth/weixin）。
//
// 端点（协议公开）：
//
//	https://open.weixin.qq.com/connect/qrconnect   网站应用授权页（snsapi_login）
//	https://api.weixin.qq.com/sns/oauth2/access_token
//	https://api.weixin.qq.com/sns/userinfo
//
// 微信是四个通道里唯一返回 unionid 的：同一开放平台主体下的多个应用共享
// unionid，所以绑定表以 (provider, subject) 为主键之外还存了 unionid 备查。
// 微信不返回邮箱。
package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// WeChat 实现微信扫码登录。
type WeChat struct {
	AuthorizeEndpoint string
	TokenEndpoint     string
	UserInfoEndpoint  string
}

// NewWeChat 构造通道。
func NewWeChat() *WeChat {
	return &WeChat{
		AuthorizeEndpoint: "https://open.weixin.qq.com/connect/qrconnect",
		TokenEndpoint:     "https://api.weixin.qq.com/sns/oauth2/access_token",
		UserInfoEndpoint:  "https://api.weixin.qq.com/sns/userinfo",
	}
}

// Name 返回通道标识。
func (w *WeChat) Name() string { return "weixin" }

func init() { Register(NewWeChat()) }

// Validate 检查必填配置。
func (w *WeChat) Validate(cfg Config, secret Secret) error {
	if cfg.Field("client_id") == "" {
		return fmt.Errorf("微信登录需要 client_id（appid）")
	}
	if secret.Get("client_secret") == "" {
		return fmt.Errorf("微信登录需要 client_secret（AppSecret）")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址。
func (w *WeChat) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("client_id")) == "" {
		return "", fmt.Errorf("微信登录需要 client_id（appid）")
	}
	qv := url.Values{}
	qv.Set("response_type", "code")
	qv.Set("appid", cfg.Field("client_id"))
	// 微信要求 redirect_uri 本身先 urlencode 一次——url.Values.Encode 会做。
	qv.Set("redirect_uri", p.RedirectURI)
	qv.Set("state", p.State)
	qv.Set("scope", firstNonEmpty(cfg.Field("scope"), "snsapi_login"))
	return w.AuthorizeEndpoint + "?" + qv.Encode(), nil
}

// Exchange 用 code 换 token 并拉取用户资料。
func (w *WeChat) Exchange(ctx context.Context, cfg Config, secret Secret, code, redirectURI string) (Identity, error) {
	if err := w.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	token, err := httpGetJSON(ctx, w.TokenEndpoint+"?"+url.Values{
		"appid":      {cfg.Field("client_id")},
		"secret":     {secret.Get("client_secret")},
		"code":       {code},
		"grant_type": {"authorization_code"},
	}.Encode(), nil)
	if err != nil {
		return Identity{}, err
	}
	// 微信错误统一是 {"errcode":40029,"errmsg":"invalid code"}，HTTP 状态仍是 200。
	if _, bad := token["errcode"]; bad && token["access_token"] == nil {
		return Identity{}, fmt.Errorf("%w：微信 %s", ErrExchangeFailed, errField(token))
	}
	accessToken := stringField(token, "access_token")
	openID := stringField(token, "openid")
	if accessToken == "" || openID == "" {
		return Identity{}, fmt.Errorf("%w：微信没有返回 access_token / openid", ErrExchangeFailed)
	}

	profile, err := httpGetJSON(ctx, w.UserInfoEndpoint+"?"+url.Values{
		"access_token": {accessToken},
		"openid":       {openID},
		"lang":         {"zh_CN"},
	}.Encode(), nil)
	if err != nil {
		return Identity{}, err
	}
	if _, bad := profile["errcode"]; bad && profile["openid"] == nil {
		return Identity{}, fmt.Errorf("%w：微信 %s", ErrExchangeFailed, errField(profile))
	}
	id := Identity{
		Provider:  "weixin",
		Subject:   stringField(profile, "openid"),
		UnionID:   stringField(profile, "unionid"),
		Nickname:  stringField(profile, "nickname"),
		AvatarURL: stringField(profile, "headimgurl"),
		Raw:       profile,
	}
	// userinfo 偶尔不回 unionid 而 token 响应里有，取先拿到的那个。
	if id.UnionID == "" {
		id.UnionID = stringField(token, "unionid")
	}
	if id.Subject == "" {
		id.Subject = openID
	}
	return normalizeIdentity(id)
}
