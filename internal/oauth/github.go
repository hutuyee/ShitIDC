// GitHub OAuth（Authorization Code 流程）。
//
// 端点是公开且稳定的：
//
//	https://github.com/login/oauth/authorize
//	https://github.com/login/oauth/access_token
//	https://api.github.com/user
//	https://api.github.com/user/emails   （email 可能不公开，要单独取）
//
// 它是本项目里最容易验证的第三方通道：协议完全公开，测试可以对着假服务器跑完整流程。
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub 实现 GitHub 登录。
type GitHub struct {
	http *http.Client
	// AuthorizeEndpoint / TokenEndpoint / APIBase 可覆盖，便于测试指向假服务器。
	AuthorizeEndpoint string
	TokenEndpoint     string
	APIBase           string
}

// NewGitHub 构造通道。
func NewGitHub() *GitHub {
	return &GitHub{
		http:              &http.Client{Timeout: 20 * time.Second},
		AuthorizeEndpoint: "https://github.com/login/oauth/authorize",
		TokenEndpoint:     "https://github.com/login/oauth/access_token",
		APIBase:           "https://api.github.com",
	}
}

// Name 返回通道标识。
func (g *GitHub) Name() string { return "github" }

func init() { Register(NewGitHub()) }

// Validate 检查必填配置。
func (g *GitHub) Validate(cfg Config, secret Secret) error {
	if cfg.Field("client_id") == "" {
		return fmt.Errorf("GitHub 登录需要 client_id")
	}
	if secret.Get("client_secret") == "" {
		return fmt.Errorf("GitHub 登录需要 client_secret")
	}
	return nil
}

// AuthorizeURL 拼出跳转地址。
func (g *GitHub) AuthorizeURL(cfg Config, _ Secret, p AuthorizeParams) (string, error) {
	if strings.TrimSpace(cfg.Field("client_id")) == "" {
		return "", fmt.Errorf("GitHub 登录需要 client_id")
	}
	q := url.Values{}
	q.Set("client_id", cfg.Field("client_id"))
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("scope", firstNonEmpty(cfg.Field("scope"), "read:user user:email"))
	q.Set("state", p.State)
	q.Set("allow_signup", "true")
	return g.AuthorizeEndpoint + "?" + q.Encode(), nil
}

// Exchange 用 code 换 token 并拉取用户资料。
func (g *GitHub) Exchange(ctx context.Context, cfg Config, secret Secret, code, redirectURI string) (Identity, error) {
	if err := g.Validate(cfg, secret); err != nil {
		return Identity{}, err
	}
	form := url.Values{}
	form.Set("client_id", cfg.Field("client_id"))
	form.Set("client_secret", secret.Get("client_secret"))
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Identity{}, err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return Identity{}, fmt.Errorf("%w：token 响应无法解析", ErrExchangeFailed)
	}
	if tok.AccessToken == "" {
		return Identity{}, fmt.Errorf("%w：%s %s", ErrExchangeFailed, tok.Error, tok.ErrorDesc)
	}

	profile, err := g.getJSON(ctx, g.APIBase+"/user", tok.AccessToken, nil)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{
		Provider:  "github",
		Subject:   jsonID(profile["id"]),
		Nickname:  stringField(profile, "name", "login"),
		AvatarURL: stringField(profile, "avatar_url"),
		Email:     stringField(profile, "email"),
		Raw:       profile,
	}
	// 邮箱通常不公开：/user 里拿不到就去 /user/emails 找主邮箱。
	if id.Email == "" {
		if emails, err := g.getJSONList(ctx, g.APIBase+"/user/emails", tok.AccessToken); err == nil {
			for _, e := range emails {
				primary, _ := e["primary"].(bool)
				verified, _ := e["verified"].(bool)
				if primary && verified {
					if s, ok := e["email"].(string); ok {
						id.Email = strings.ToLower(strings.TrimSpace(s))
						break
					}
				}
			}
		}
	}
	return normalizeIdentity(id)
}

// getJSON 拉一个 JSON 对象。
func (g *GitHub) getJSON(ctx context.Context, target, token string, out map[string]any) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w：GitHub 返回 HTTP %d", ErrExchangeFailed, resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w：资料无法解析", ErrExchangeFailed)
	}
	return parsed, nil
}

// getJSONList 拉一个 JSON 数组。
func (g *GitHub) getJSONList(ctx context.Context, target, token string) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w：GitHub 返回 HTTP %d", ErrExchangeFailed, resp.StatusCode)
	}
	var parsed []map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// jsonID 把 JSON 里的数字 ID 转成字符串（GitHub 的 id 是数字）。
func jsonID(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

// stringField 按顺序取第一个非空字符串字段。
func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
