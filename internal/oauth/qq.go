// QQ 互联登录（对应魔方 public/plugins/oauth/qq）。
//
// 端点（graph.qq.com，协议公开）：
//
//	https://graph.qq.com/oauth2.0/authorize          授权页
//	https://graph.qq.com/oauth2.0/token              换 token（默认返回 a=1&b=2 形态）
//	https://graph.qq.com/oauth2.0/me?fmt=json        取 openid（老接口是 JSONP，fmt=json 直接回 JSON；仍保留 JSONP 兜底）
//	https://graph.qq.com/user/get_user_info          取昵称与头像
//
// QQ 不提供邮箱，Identity.Email 恒为空——主流程会引导绑定。
package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// QQ 实现 QQ 互联登录。
type QQ struct {
	// 三个端点可覆盖，便于测试指向假服务器。
	AuthorizeEndpoint string
	TokenEndpoint     string
	MeEndpoint        string
	UserInfoEndpoint  string
}

// NewQQ 构造通道。
func NewQQ() *QQ {
	return &QQ{
		AuthorizeEndpoint: "https://graph.qq.com/oauth2.0/authorize",
		TokenEndpoint:     "https://graph.qq.com/oauth2.0/token",
		MeEndpoint:        "https://graph.qq.com/oauth2.0/me",
		UserInfoEndpoint:  "https://graph.qq.com/user/get_user_info",
	}
}

// Name 返回通道标识。
func (q *QQ) Name() string { return "qq" }

func init() { Register(NewQQ()) }

// Validate 检查必填配置。
func (q *QQ) Validate(cfg Config, secret Secret) error {
	if cfg.Field("client_id") == "" {
		return fmt.Errorf("QQ 登录需要 client_id（appid）")
	}
	if secret.Get("client_secret") == "" {
		return fmt.Errorf("QQ 登录需要 client_secret（appkey）")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址。
func (q *QQ) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("client_id")) == "" {
		return "", fmt.Errorf("QQ 登录需要 client_id（appid）")
	}
	qv := url.Values{}
	qv.Set("response_type", "code")
	qv.Set("client_id", cfg.Field("client_id"))
	qv.Set("redirect_uri", p.RedirectURI)
	qv.Set("state", p.State)
	qv.Set("scope", firstNonEmpty(cfg.Field("scope"), "snsapi_login"))
	return q.AuthorizeEndpoint + "?" + qv.Encode(), nil
}

// Exchange 用 code 换 token，取 openid，再取用户资料。
func (q *QQ) Exchange(ctx context.Context, cfg Config, secret Secret, code, redirectURI string) (Identity, error) {
	if err := q.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", cfg.Field("client_id"))
	form.Set("client_secret", secret.Get("client_secret"))
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	raw, err := httpGetRaw(ctx, q.TokenEndpoint+"?"+form.Encode())
	if err != nil {
		return Identity{}, err
	}
	token := parseQQTokenResponse(raw)
	accessToken := token["access_token"]
	if accessToken == "" {
		return Identity{}, fmt.Errorf("%w：QQ 没有返回 access_token（%s）", ErrExchangeFailed, firstNonEmpty(token["error_description"], token["msg"], truncateForLog([]byte(raw))))
	}

	me, meErr := httpGetJSON(ctx, q.MeEndpoint+"?"+url.Values{
		"access_token": {accessToken},
		"fmt":          {"json"},
	}.Encode(), nil)
	if meErr != nil || me == nil || me["openid"] == nil {
		// 兼容 JSONP 形态：me 端点不认 fmt=json 时回 callback(...)，剥壳重解。
		if raw, errRaw := httpGetRaw(ctx, q.MeEndpoint+"?"+url.Values{"access_token": {accessToken}}.Encode()); errRaw == nil {
			if parsed, ok := parseJSONObject(unwrapJSONP(raw)); ok {
				me = parsed
			}
		} else if meErr != nil {
			return Identity{}, meErr
		}
	}
	openID := stringField(me, "openid")
	if openID == "" {
		return Identity{}, fmt.Errorf("%w：QQ 没有返回 openid", ErrExchangeFailed)
	}

	profile, err := httpGetJSON(ctx, q.UserInfoEndpoint+"?"+url.Values{
		"access_token":       {accessToken},
		"openid":             {openID},
		"oauth_consumer_key": {cfg.Field("client_id")},
	}.Encode(), nil)
	if err != nil {
		return Identity{}, err
	}
	// ret != 0 表示业务失败（token 过期、openid 与 appid 不匹配等）。
	// ret 在 JSON 里是数字，jsonID 把数字/字符串统一转成可比的字符串。
	if code := jsonID(profile["ret"]); code != "" && code != "0" {
		return Identity{}, fmt.Errorf("%w：QQ 用户资料获取失败（ret=%s %s）", ErrExchangeFailed, code, firstNonEmpty(stringField(profile, "msg"), stringField(profile, "msg_zh")))
	}
	id := Identity{
		Provider:  "qq",
		Subject:   openID,
		Nickname:  stringField(profile, "nickname"),
		AvatarURL: firstNonEmpty(stringField(profile, "figureurl_qq_1"), stringField(profile, "figureurl_1"), stringField(profile, "figureurl")),
		Raw:       profile,
	}
	return normalizeIdentity(id)
}
