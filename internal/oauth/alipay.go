// 支付宝登录（对应魔方 public/plugins/oauth/alipay）。
//
// 与网站支付走同一个 openapi 网关协议（gateway.do + RSA2 签名），签名与密钥
// 解析直接复用 internal/alipaykit——支付与登录共用一份实现：
//
//	https://openauth.alipay.com/oauth2/publicAppAuthorize.htm   授权页
//	alipay.system.oauth.token   auth_code 换 access_token（POST gateway.do）
//	alipay.user.info.share      取会员公开信息（昵称 / 头像 / user_id）
//
// 回调参数是 auth_code 而不是 code；API 层在 code 为空时读 auth_code。
//
// 关于响应验签：参考实现（以及多数开源集成）不校验 alipay 响应里的 sign，
// 靠 TLS 保证传输安全。这里保持一致，但网关地址固定官方 HTTPS 端点
// （可通过 GatewayEndpoint 覆盖供测试），不做 HTTP 明文网关的兜底。
package oauth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/alipaykit"
)

// Alipay 实现支付宝登录。
type Alipay struct {
	// AuthorizeEndpoint / GatewayEndpoint 可覆盖，便于测试指向假服务器。
	AuthorizeEndpoint string
	GatewayEndpoint   string
	http              *http.Client
}

// NewAlipay 构造通道。
func NewAlipay() *Alipay {
	return &Alipay{
		AuthorizeEndpoint: "https://openauth.alipay.com/oauth2/publicAppAuthorize.htm",
		GatewayEndpoint:   "https://openapi.alipay.com/gateway.do",
		http:              &http.Client{Timeout: 20 * time.Second},
	}
}

// Name 返回通道标识。
func (a *Alipay) Name() string { return "alipay" }

func init() { Register(NewAlipay()) }

// Validate 检查必填配置。
func (a *Alipay) Validate(cfg Config, secret Secret) error {
	if cfg.Field("app_id") == "" {
		return fmt.Errorf("支付宝登录需要 app_id")
	}
	if secret.Get("app_private_key") == "" {
		return fmt.Errorf("支付宝登录需要开发者私钥 app_private_key")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址。
func (a *Alipay) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("app_id")) == "" {
		return "", fmt.Errorf("支付宝登录需要 app_id")
	}
	qv := url.Values{}
	qv.Set("app_id", cfg.Field("app_id"))
	qv.Set("scope", "auth_user")
	qv.Set("redirect_uri", p.RedirectURI)
	qv.Set("state", p.State)
	return a.AuthorizeEndpoint + "?" + qv.Encode(), nil
}

// Exchange 用 auth_code 换 token 并拉取会员公开信息。
// 参数 code 在支付宝场景里实为 auth_code（API 层已做映射）。
func (a *Alipay) Exchange(ctx context.Context, cfg Config, secret Secret, code, _ string) (Identity, error) {
	if err := a.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	privateKey, err := alipaykit.ParsePrivateKey(secret.Get("app_private_key"))
	if err != nil {
		return Identity{}, err
	}

	tokenResp, err := a.postGateway(ctx, cfg, privateKey, url.Values{
		"method":     {"alipay.system.oauth.token"},
		"grant_type": {"authorization_code"},
		"code":       {code},
	})
	if err != nil {
		return Identity{}, err
	}
	accessToken := stringField(tokenResp, "access_token")
	if accessToken == "" {
		return Identity{}, fmt.Errorf("%w：支付宝 %s", ErrExchangeFailed, firstNonEmpty(stringField(tokenResp, "sub_msg"), stringField(tokenResp, "msg"), "没有返回 access_token"))
	}
	userID := stringField(tokenResp, "user_id")

	infoResp, err := a.postGateway(ctx, cfg, privateKey, url.Values{
		"method":     {"alipay.user.info.share"},
		"auth_token": {accessToken},
	})
	if err != nil {
		return Identity{}, err
	}
	id := Identity{
		Provider:  "alipay",
		Subject:   firstNonEmpty(stringField(infoResp, "user_id"), userID),
		Nickname:  stringField(infoResp, "nick_name"),
		AvatarURL: stringField(infoResp, "avatar"),
		Raw:       infoResp,
	}
	return normalizeIdentity(id)
}

// postGateway 组一个网关请求：公共参数 + biz_content，RSA2 签名后 POST，
// 返回 `<method 带点转下划线>_response` 节点的内容；error_response 则报错。
func (a *Alipay) postGateway(ctx context.Context, cfg Config, privateKey *rsa.PrivateKey, biz url.Values) (map[string]any, error) {
	bizMap := map[string]string{}
	for _, k := range []string{"grant_type", "code", "auth_token"} {
		if v := biz.Get(k); v != "" {
			bizMap[k] = v
		}
	}
	params := url.Values{
		"app_id":      {cfg.Field("app_id")},
		"method":      {biz.Get("method")},
		"format":      {"JSON"},
		"charset":     {"utf-8"},
		"sign_type":   {"RSA2"},
		"timestamp":   {time.Now().Format("2006-01-02 15:04:05")},
		"version":     {"1.0"},
		"biz_content": {mustJSON(bizMap)},
	}
	content := alipaykit.SignContent(params)
	sign, err := alipaykit.SignRSA2(privateKey, content)
	if err != nil {
		return nil, fmt.Errorf("%w：支付宝签名失败：%v", ErrExchangeFailed, err)
	}
	params.Set("sign", sign)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.GatewayEndpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w：支付宝网关响应无法解析：%s", ErrExchangeFailed, truncateForLog(body))
	}
	if er, ok := parsed["error_response"].(map[string]any); ok {
		return nil, fmt.Errorf("%w：支付宝 %s", ErrExchangeFailed, firstNonEmpty(stringField(er, "sub_msg"), stringField(er, "msg"), errField(er)))
	}
	node := strings.ReplaceAll(biz.Get("method"), ".", "_") + "_response"
	out, _ := parsed[node].(map[string]any)
	if out == nil {
		return nil, fmt.Errorf("%w：支付宝响应缺少 %s 节点", ErrExchangeFailed, node)
	}
	return out, nil
}

// mustJSON 是不会失败的 JSON 编码（入参恒为 string map）。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
