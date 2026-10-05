// 企业微信登录（对应魔方 CBAP 插件包 public/plugins/oauth/Qyweixin）。
//
// 这是「服务商第三方应用」模式，比普通 OAuth 多一步：
//
//	1. 授权页 https://login.work.weixin.qq.com/wwlogin/sso/login?login_type=ServiceApp&appid={SuiteID}...
//	2. 企业微信把 suite_ticket 推送到服务商后台配置的「指令回调 URL」（每 10 分钟一次）
//	3. 用 suite_id + suite_secret + suite_ticket 换 suite_access_token
//	4. 用 suite_access_token + code 调 service/auth/getuserinfo3rd 拿 openid
//
// 因此 Exchange 需要拿到推送入库的 suite_ticket：实现 SuiteTicketProvider
// 可选接口，由接入层在回调时提供。没有 ticket 时明确报错并提示配置回调 URL，
// 而不是给出一个无法解释的「登录失败」。

package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// SuiteTicketProvider 是可选扩展：需要 suite_ticket 的通道（企业微信服务商应用）实现它。
type SuiteTicketProvider interface {
	// ExchangeWithSuiteTicket 与 Exchange 同义，额外接收由指令回调推送并落库的 suite_ticket。
	ExchangeWithSuiteTicket(ctx context.Context, cfg Config, secret Secret, code, redirectURI, suiteTicket string) (Identity, error)
}

// Qyweixin 实现企业微信登录。
type Qyweixin struct {
	// 端点可覆盖，便于测试指向假服务器。
	AuthorizeEndpoint  string
	SuiteTokenEndpoint string
	UserInfoEndpoint   string
}

// NewQyweixin 构造通道。
func NewQyweixin() *Qyweixin {
	return &Qyweixin{
		AuthorizeEndpoint:  "https://login.work.weixin.qq.com/wwlogin/sso/login",
		SuiteTokenEndpoint: "https://qyapi.weixin.qq.com/cgi-bin/service/get_suite_token",
		UserInfoEndpoint:   "https://qyapi.weixin.qq.com/cgi-bin/service/auth/getuserinfo3rd",
	}
}

// Name 返回通道标识。
func (q *Qyweixin) Name() string { return "qyweixin" }

func init() { Register(NewQyweixin()) }

// Validate 检查必填配置。
// token / aes_key 是「指令回调 URL」校验与解密 suite_ticket 所必需，
// 缺任何一个整个登录流程都走不通，所以与参考插件一样要求填齐。
func (q *Qyweixin) Validate(cfg Config, secret Secret) error {
	if cfg.Field("suite_id") == "" {
		return fmt.Errorf("企业微信登录需要 SuiteID")
	}
	if secret.Get("secret") == "" {
		return fmt.Errorf("企业微信登录需要 SuiteSecret")
	}
	if secret.Get("token") == "" {
		return fmt.Errorf("企业微信登录需要回调 Token（用于校验指令回调）")
	}
	if secret.Get("aes_key") == "" {
		return fmt.Errorf("企业微信登录需要 EncodingAESKey（用于解密指令回调）")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址（ServiceApp 模式，尾缀 #wechat_redirect 与参考实现一致）。
func (q *Qyweixin) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("suite_id")) == "" {
		return "", fmt.Errorf("企业微信登录需要 SuiteID")
	}
	qv := url.Values{}
	qv.Set("login_type", "ServiceApp")
	qv.Set("appid", cfg.Field("suite_id"))
	qv.Set("redirect_uri", p.RedirectURI)
	qv.Set("state", p.State)
	return q.AuthorizeEndpoint + "?" + qv.Encode() + "#wechat_redirect", nil
}

// Exchange 在没有 suite_ticket 时不可用：接口本身要求接入层走
// ExchangeWithSuiteTicket（回调时从库里取 ticket）。
func (q *Qyweixin) Exchange(_ context.Context, _ Config, _ Secret, _, _ string) (Identity, error) {
	return Identity{}, fmt.Errorf("%w：企业微信登录需要先收到指令回调推送的 suite_ticket", ErrExchangeFailed)
}

// ExchangeWithSuiteTicket 用 suite_ticket 换 suite_access_token，再换用户身份。
func (q *Qyweixin) ExchangeWithSuiteTicket(ctx context.Context, cfg Config, secret Secret, code, _ string, suiteTicket string) (Identity, error) {
	if err := q.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	if strings.TrimSpace(suiteTicket) == "" {
		return Identity{}, fmt.Errorf("%w：suite_ticket 为空", ErrExchangeFailed)
	}
	tok, err := httpPostJSON(ctx, q.SuiteTokenEndpoint, map[string]string{
		"suite_id":     cfg.Field("suite_id"),
		"suite_secret": secret.Get("secret"),
		"suite_ticket": suiteTicket,
	}, nil)
	if err != nil {
		return Identity{}, err
	}
	suiteAccessToken := stringField(tok, "suite_access_token")
	if suiteAccessToken == "" {
		return Identity{}, fmt.Errorf("%w：企业微信没有返回 suite_access_token（%s）", ErrExchangeFailed, errField(tok))
	}
	profile, err := httpGetJSON(ctx, q.UserInfoEndpoint+"?"+url.Values{
		"suite_access_token": {suiteAccessToken},
		"code":               {code},
	}.Encode(), nil)
	if err != nil {
		return Identity{}, err
	}
	if ec := jsonID(profile["errcode"]); ec != "" && ec != "0" {
		return Identity{}, fmt.Errorf("%w：企业微信获取用户失败（errcode=%s %s）", ErrExchangeFailed, ec, stringField(profile, "errmsg"))
	}
	// 第三方应用返回 openid；部分场景只有 open_userid（服务商维度）。
	subject := firstNonEmpty(stringField(profile, "openid"), stringField(profile, "open_userid"))
	if subject == "" {
		return Identity{}, fmt.Errorf("%w：企业微信没有返回 openid", ErrExchangeFailed)
	}
	return normalizeIdentity(Identity{
		Provider: "qyweixin",
		Subject:  subject,
		Raw:      profile,
	})
}
