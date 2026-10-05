package magiccube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/security"
)

type Paths struct {
	Login     string `json:"login"`
	Products  string `json:"products"`
	Test      string `json:"test"`
	Create    string `json:"create"`
	Suspend   string `json:"suspend"`
	Unsuspend string `json:"unsuspend"`
	Terminate string `json:"terminate"`
	Renew     string `json:"renew"`
	// ChangePackage 是上游的改配（升降级）路径；留空表示不支持在线改配。
	ChangePackage string `json:"change_package"`
}

type Config struct {
	BaseURL      string `json:"base_url"`
	Username     string `json:"username"`
	APIKey       string `json:"api_key,omitempty"`
	AuthMode     string `json:"auth_mode"`
	TokenPrefix  string `json:"token_prefix"`
	UserHeader   string `json:"user_header,omitempty"`
	APIKeyHeader string `json:"api_key_header,omitempty"`
	AllowPrivate bool   `json:"allow_private"`
	Paths        Paths  `json:"paths"`
}

type RemoteProduct struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	PriceCents   int64          `json:"price_cents"`
	Currency     string         `json:"currency"`
	BillingCycle string         `json:"billing_cycle"`
	Raw          map[string]any `json:"raw"`
}

type Client struct {
	cfg   Config
	http  *http.Client
	mu    sync.Mutex
	token string
}

func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("magiccube base_url is required")
	}
	if err := security.ValidateOutboundURL(cfg.BaseURL, cfg.AllowPrivate); err != nil {
		return nil, err
	}
	if cfg.Paths.Login == "" {
		cfg.Paths.Login = "/v1/login_api"
	}
	if cfg.Paths.Products == "" {
		cfg.Paths.Products = "/v1/products"
	}
	if cfg.Paths.Test == "" {
		cfg.Paths.Test = cfg.Paths.Products
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = "legacy_login"
	}
	if cfg.TokenPrefix == "" {
		cfg.TokenPrefix = "Bearer"
	}
	return &Client{cfg: cfg, http: security.SafeHTTPClient(cfg.AllowPrivate, 15*time.Second)}, nil
}

func (c *Client) TestConnection(ctx context.Context) error {
	if strings.EqualFold(c.cfg.AuthMode, "legacy_login") {
		if _, err := c.login(ctx); err != nil {
			return err
		}
	}
	_, err := c.do(ctx, http.MethodGet, c.cfg.Paths.Test, nil, "")
	return err
}

func (c *Client) ListProducts(ctx context.Context) ([]RemoteProduct, error) {
	body, err := c.do(ctx, http.MethodGet, c.cfg.Paths.Products, nil, "")
	if err != nil {
		return nil, err
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("decode magiccube products: %w", err)
	}
	currency := findCurrency(root)
	if currency == "" {
		currency = "CNY"
	}
	maps := collectProductMaps(root)
	out := make([]RemoteProduct, 0, len(maps))
	seen := map[string]bool{}
	for _, m := range maps {
		id := firstString(m, "id", "product_id", "productid", "pid")
		name := firstString(m, "name", "product_name", "productname", "product")
		if id == "" || name == "" || seen[id] {
			continue
		}
		seen[id] = true
		p := RemoteProduct{
			ID:           id,
			Name:         name,
			Description:  firstString(m, "description", "desc", "product_description"),
			PriceCents:   extractPriceCents(m),
			Currency:     firstNonEmpty(firstString(m, "currency", "currency_code"), currency),
			BillingCycle: normalizeCycle(firstString(m, "billingcycle", "billing_cycle", "cycle")),
			Raw:          m,
		}
		if p.BillingCycle == "" {
			p.BillingCycle = "monthly"
		}
		out = append(out, p)
	}
	return out, nil
}

func (c *Client) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	if c.cfg.Paths.Create == "" {
		return nil, fmt.Errorf("magiccube automatic create path is not configured for this upstream; product sync works, but provisioning needs the upstream version's resource API path")
	}
	body, err := c.do(ctx, http.MethodPost, c.cfg.Paths.Create, req, req.RequestID)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode magiccube response: %w", err)
	}
	id := extractID(raw)
	if id == "" {
		return nil, fmt.Errorf("magiccube response does not contain an instance id; adjust adapter mapping")
	}
	return &provider.Instance{ID: id, Data: raw}, nil
}

func (c *Client) Suspend(ctx context.Context, id string) error {
	return c.action(ctx, c.cfg.Paths.Suspend, id)
}
func (c *Client) Unsuspend(ctx context.Context, id string) error {
	return c.action(ctx, c.cfg.Paths.Unsuspend, id)
}
func (c *Client) Terminate(ctx context.Context, id string) error {
	return c.action(ctx, c.cfg.Paths.Terminate, id)
}
func (c *Client) Renew(ctx context.Context, req provider.RenewRequest) error {
	return c.action(ctx, c.cfg.Paths.Renew, req.InstanceID)
}

func (c *Client) action(ctx context.Context, path, id string) error {
	if path == "" {
		return fmt.Errorf("magiccube action path is not configured for this upstream version")
	}
	_, err := c.do(ctx, http.MethodPost, path, map[string]any{"id": id}, "")
	return err
}

// ChangePackage asks the upstream to move an existing instance to another plan
// (魔方 server modules implement this as _ChangePackage). The path stays
// operator-configurable for the same reason as the other resource actions: the
// magic cube resource API is version-specific, and a wrong guess is worse than
// an explicit "not configured".
func (c *Client) ChangePackage(ctx context.Context, req provider.ChangePackageRequest) error {
	if c.cfg.Paths.ChangePackage == "" {
		return provider.ErrChangePackageUnsupported
	}
	_, err := c.do(ctx, http.MethodPost, c.cfg.Paths.ChangePackage, map[string]any{
		"id":            req.InstanceID,
		"product_ref":   req.ProductRef,
		"billing_cycle": req.BillingCycle,
		"price_cents":   req.PriceCents,
	}, "")
	return err
}

func (c *Client) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	if strings.TrimSpace(c.cfg.Username) == "" || strings.TrimSpace(c.cfg.APIKey) == "" {
		return "", fmt.Errorf("magiccube username and API key are required")
	}
	base, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse("/" + strings.TrimLeft(c.cfg.Paths.Login, "/"))
	if err != nil {
		return "", err
	}
	target := base.ResolveReference(rel).String()
	if err := security.ValidateOutboundURL(target, c.cfg.AllowPrivate); err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("account", c.cfg.Username)
	form.Set("password", c.cfg.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("magiccube login HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", fmt.Errorf("decode magiccube login response: %w", err)
	}
	token := firstString(raw, "jwt", "token", "access_token")
	if token == "" {
		return "", fmt.Errorf("magiccube login did not return jwt/token")
	}
	c.token = token
	return token, nil
}

func (c *Client) do(ctx context.Context, method, path string, payload any, requestID string) ([]byte, error) {
	base, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	rel, err := url.Parse("/" + strings.TrimLeft(path, "/"))
	if err != nil {
		return nil, err
	}
	target := base.ResolveReference(rel).String()
	if err := security.ValidateOutboundURL(target, c.cfg.AllowPrivate); err != nil {
		return nil, err
	}
	var r io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if requestID != "" {
		req.Header.Set("Idempotency-Key", requestID)
		req.Header.Set("X-Request-ID", requestID)
	}
	if err := c.applyAuth(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("magiccube HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (c *Client) applyAuth(ctx context.Context, req *http.Request) error {
	switch strings.ToLower(c.cfg.AuthMode) {
	case "legacy_login", "jwt":
		token, err := c.login(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", strings.TrimSpace(c.cfg.TokenPrefix)+" "+token)
	case "basic":
		req.SetBasicAuth(c.cfg.Username, c.cfg.APIKey)
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	case "headers":
		userHeader := firstNonEmpty(c.cfg.UserHeader, "X-API-User")
		keyHeader := firstNonEmpty(c.cfg.APIKeyHeader, "X-API-Key")
		req.Header.Set(userHeader, c.cfg.Username)
		req.Header.Set(keyHeader, c.cfg.APIKey)
	default:
		return fmt.Errorf("unsupported magiccube auth_mode %q", c.cfg.AuthMode)
	}
	return nil
}

func collectProductMaps(v any) []map[string]any {
	var out []map[string]any
	var walk func(any)
	walk = func(x any) {
		switch value := x.(type) {
		case []any:
			for _, item := range value {
				walk(item)
			}
		case map[string]any:
			id := firstString(value, "id", "product_id", "productid", "pid")
			name := firstString(value, "name", "product_name", "productname")
			if id != "" && name != "" {
				out = append(out, value)
			}
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(v)
	return out
}

func findCurrency(v any) string {
	var result string
	var walk func(any)
	walk = func(x any) {
		if result != "" {
			return
		}
		switch value := x.(type) {
		case []any:
			for _, item := range value {
				walk(item)
			}
		case map[string]any:
			if code := firstString(value, "currency", "currency_code", "code"); len(code) == 3 {
				result = strings.ToUpper(code)
				return
			}
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(v)
	return result
}

func extractPriceCents(m map[string]any) int64 {
	for _, key := range []string{"price", "amount", "monthly", "monthly_price"} {
		if value, ok := m[key]; ok {
			if cents, ok := numericToCents(value); ok {
				return cents
			}
		}
	}
	for _, key := range []string{"pricing", "prices", "price_list"} {
		if nested, ok := m[key]; ok {
			switch value := nested.(type) {
			case map[string]any:
				for _, cycle := range []string{"monthly", "annually", "yearly", "quarterly"} {
					if cents, ok := numericToCents(value[cycle]); ok {
						return cents
					}
				}
			case []any:
				for _, item := range value {
					if price, ok := item.(map[string]any); ok {
						for _, k := range []string{"price", "amount"} {
							if cents, ok := numericToCents(price[k]); ok {
								return cents
							}
						}
					}
				}
			}
		}
	}
	return 0
}

func numericToCents(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n*100 + 0.5), true
	case json.Number:
		f, err := n.Float64()
		return int64(f*100 + 0.5), err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(n, "¥")), 64)
		return int64(f*100 + 0.5), err == nil
	default:
		return 0, false
	}
}

func normalizeCycle(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "month", "monthly", "月付":
		return "monthly"
	case "year", "yearly", "annually", "annual", "年付":
		return "yearly"
	case "quarter", "quarterly", "季付":
		return "quarterly"
	case "semiannually", "semiannual", "半年付":
		return "semiannually"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok && value != nil {
			switch v := value.(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			case float64:
				return strconv.FormatFloat(v, 'f', -1, 64)
			case json.Number:
				return v.String()
			default:
				s := fmt.Sprint(v)
				if s != "<nil>" && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractID(m map[string]any) string {
	for _, key := range []string{"id", "service_id", "hostid", "host_id", "product_id"} {
		if v, ok := m[key]; ok {
			return fmt.Sprint(v)
		}
	}
	if data, ok := m["data"].(map[string]any); ok {
		return extractID(data)
	}
	return ""
}
