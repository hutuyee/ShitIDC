// 微博登录（对应魔方 public/plugins/oauth/weibo）。
//
// 端点（协议公开）：
//
//	https://api.weibo.com/oauth2/authorize        授权页
//	https://api.weibo.com/oauth2/access_token     POST 换 token，响应带 uid
//	https://api.weibo.com/2/users/show.json       取昵称与头像
//
// 修掉魔方参考实现的一个错误：它把 access_token 当 openid 回传
// （`'openid' => $access_token['access_token']`），会导致 subject 跟着 token
// 换代而漂移——正确的主键是响应里的 uid。这里按官方协议用 uid。
package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Weibo 实现微博登录。
type Weibo struct {
	AuthorizeEndpoint string
	TokenEndpoint     string
	UserInfoEndpoint  string
}

// NewWeibo 构造通道。
func NewWeibo() *Weibo {
	return &Weibo{
		AuthorizeEndpoint: "https://api.weibo.com/oauth2/authorize",
		TokenEndpoint:     "https://api.weibo.com/oauth2/access_token",
		UserInfoEndpoint:  "https://api.weibo.com/2/users/show.json",
	}
}

// Name 返回通道标识。
func (w *Weibo) Name() string { return "weibo" }

func init() { Register(NewWeibo()) }

// Validate 检查必填配置。
func (w *Weibo) Validate(cfg Config, secret Secret) error {
	if cfg.Field("client_id") == "" {
		return fmt.Errorf("微博登录需要 client_id（App Key）")
	}
	if secret.Get("client_secret") == "" {
		return fmt.Errorf("微博登录需要 client_secret（App Secret）")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址。scope 不传时微博用应用登记的默认授权范围。
func (w *Weibo) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("client_id")) == "" {
		return "", fmt.Errorf("微博登录需要 client_id（App Key）")
	}
	qv := url.Values{}
	qv.Set("client_id", cfg.Field("client_id"))
	qv.Set("redirect_uri", p.RedirectURI)
	qv.Set("state", p.State)
	if scope := cfg.Field("scope"); scope != "" {
		qv.Set("scope", scope)
	}
	return w.AuthorizeEndpoint + "?" + qv.Encode(), nil
}

// Exchange 用 code 换 token 并拉取用户资料。
func (w *Weibo) Exchange(ctx context.Context, cfg Config, secret Secret, code, redirectURI string) (Identity, error) {
	if err := w.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	token, err := httpPostForm(ctx, w.TokenEndpoint, url.Values{
		"code":          {code},
		"client_id":     {cfg.Field("client_id")},
		"client_secret": {secret.Get("client_secret")},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}, nil)
	if err != nil {
		return Identity{}, err
	}
	accessToken := stringField(token, "access_token")
	uid := stringField(token, "uid")
	if accessToken == "" || uid == "" {
		return Identity{}, fmt.Errorf("%w：微博 %s", ErrExchangeFailed, firstNonEmpty(errField(token), "没有返回 access_token / uid"))
	}

	profile, err := httpGetJSON(ctx, w.UserInfoEndpoint+"?"+url.Values{
		"access_token": {accessToken},
		"uid":          {uid},
	}.Encode(), nil)
	if err != nil {
		return Identity{}, err
	}
	if profile["error"] != nil || profile["error_code"] != nil {
		return Identity{}, fmt.Errorf("%w：微博 %s", ErrExchangeFailed, errField(profile))
	}
	id := Identity{
		Provider:  "weibo",
		Subject:   uid,
		Nickname:  stringField(profile, "screen_name"),
		AvatarURL: firstNonEmpty(stringField(profile, "avatar_hd"), stringField(profile, "avatar_large"), stringField(profile, "profile_image_url")),
		Raw:       profile,
	}
	return normalizeIdentity(id)
}
