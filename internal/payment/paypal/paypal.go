// Package paypal 实现 PayPal REST Orders v2 收款通道。
//
// 用到的官方接口：
//
//	POST {base}/v1/oauth2/token                          取 access token（client_credentials）
//	POST {base}/v2/checkout/orders                       创建订单，取 approve 链接
//	GET  {base}/v2/checkout/orders/{id}                  查订单状态与成交金额
//	POST {base}/v1/notifications/verify-webhook-signature 回调验签（服务器侧）
//
// 沙箱与生产只是 base 不同：
//
//	https://api-m.sandbox.paypal.com
//	https://api-m.paypal.com
//
// 回调验签用的是 PayPal 自己的 verify-webhook-signature 接口，而不是本地推算签名串。
// 这样做的理由：本地推算需要自己算 CRC32、自己拉证书链，任何一步写错都会表现为
// 「验签失败」而难以定位；直接问 PayPal 则把这段不确定性交还给官方接口。
// 代价是每笔回调多一次出网请求——对支付回调这个量级完全可以接受。
package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hutuyee/ShitIDC/internal/payment"
	"github.com/hutuyee/ShitIDC/internal/security"
)

// Config 是 PayPal 通道的配置。
type Config struct {
	ClientID string `json:"client_id"`
	Secret   string `json:"secret"`
	// Sandbox 为 true 时走沙箱地址；也可以用 BaseURL 显式覆盖。
	Sandbox bool   `json:"sandbox"`
	BaseURL string `json:"base_url"`
	// WebhookID 是 PayPal 开发者后台里这个 webhook 的 ID，验签必填。
	WebhookID string `json:"webhook_id"`
	// BrandName 显示在 PayPal 收银台上的商户名。
	BrandName    string `json:"brand_name"`
	AllowPrivate bool   `json:"allow_private"`
}

// Gateway 实现 payment.Gateway。
type Gateway struct {
	http *http.Client
	Now  func() time.Time
	// tokenCache 缓存 access token：PayPal 的 token 有效期远长于一次下单，
	// 每笔订单都重新取一次既慢又容易触发限流。
	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// New 构造通道。
func New() *Gateway {
	return &Gateway{http: &http.Client{Timeout: 20 * time.Second}, Now: time.Now}
}

// NewWithHTTPClient 与 New 相同但可注入 HTTP 客户端，供测试使用。
func NewWithHTTPClient(c *http.Client) *Gateway {
	if c == nil {
		return New()
	}
	return &Gateway{http: c, Now: time.Now}
}

// Method 返回通道标识。
func (g *Gateway) Method() string { return "paypal" }

func init() { payment.Register(New()) }

// sandboxBase / liveBase 是官方端点。
const (
	sandboxBase = "https://api-m.sandbox.paypal.com"
	liveBase    = "https://api-m.paypal.com"
)

// parseConfig 解析通道凭据。Secret 可以是 JSON，也可以是 "clientid:secret" 这种简写。
func parseConfig(cfg payment.ProviderConfig) (Config, error) {
	var c Config
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return c, errors.New("PayPal 通道缺少凭据")
	}
	if strings.HasPrefix(secret, "{") {
		if err := json.Unmarshal([]byte(secret), &c); err != nil {
			return c, fmt.Errorf("PayPal 配置不是合法 JSON: %w", err)
		}
	} else if i := strings.Index(secret, ":"); i > 0 {
		// "client_id:secret" 简写。
		c.ClientID = strings.TrimSpace(secret[:i])
		c.Secret = strings.TrimSpace(secret[i+1:])
	} else {
		c.ClientID = strings.TrimSpace(cfg.MerchantID)
		c.Secret = secret
	}
	// MerchantID 里填了 client_id 时以它为准（后台表单更直观）。
	if strings.TrimSpace(cfg.MerchantID) != "" && !strings.Contains(cfg.MerchantID, "@") {
		if c.ClientID == "" {
			c.ClientID = strings.TrimSpace(cfg.MerchantID)
		}
	}
	if c.ClientID == "" || c.Secret == "" {
		return c, errors.New("PayPal 需要 client_id 与 secret")
	}
	if c.BaseURL == "" {
		if c.Sandbox {
			c.BaseURL = sandboxBase
		} else {
			c.BaseURL = liveBase
		}
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c, nil
}

// accessToken 取（或复用）OAuth2 access token。
func (g *Gateway) accessToken(ctx context.Context, c Config) (string, error) {
	g.mu.Lock()
	if g.token != "" && g.Now().Before(g.tokenExp) {
		tok := g.token
		g.mu.Unlock()
		return tok, nil
	}
	g.mu.Unlock()

	form := strings.NewReader("grant_type=client_credentials")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/oauth2/token", form)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.ClientID, c.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("PayPal 取 token 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("PayPal 取 token 被拒(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("PayPal token 返回无法解析: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("PayPal 没有返回 access_token")
	}
	// 提前 60 秒过期，避免边界上用到刚失效的 token。
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	g.mu.Lock()
	g.token = out.AccessToken
	g.tokenExp = g.Now().Add(ttl - 60*time.Second)
	g.mu.Unlock()
	return out.AccessToken, nil
}

// ---- 下单 ----

// PayURL 创建 PayPal 订单并返回 approve 链接。
// 用 custom_id 带上我们的订单号：回调里可以据此精确定位订单，不必依赖元数据。
func (g *Gateway) PayURL(ctx context.Context, cfg payment.ProviderConfig, prepared payment.Prepared) (string, error) {
	c, err := parseConfig(cfg)
	if err != nil {
		return "", err
	}
	if !c.AllowPrivate {
		if err := security.ValidateOutboundURL(c.BaseURL, false); err != nil {
			return "", err
		}
	}
	token, err := g.accessToken(ctx, c)
	if err != nil {
		return "", err
	}

	currency := strings.ToUpper(strings.TrimSpace(prepared.Currency))
	if currency == "" {
		currency = "USD"
	}
	// PayPal 的金额是两位小数的字符串。
	amount := strconv.FormatFloat(float64(prepared.AmountCents)/100, 'f', 2, 64)

	payload := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{{
			"reference_id": prepared.OutTradeNo,
			"custom_id":    prepared.OutTradeNo,
			"description":  truncateRunes(prepared.Subject, 127),
			"amount": map[string]string{
				"currency_code": currency,
				"value":         amount,
			},
		}},
		"payment_source": map[string]any{
			"paypal": map[string]any{
				"experience_context": map[string]string{
					"return_url":  prepared.ReturnURL,
					"cancel_url":  prepared.ReturnURL,
					"brand_name":  truncateRunes(c.BrandName, 127),
					"user_action": "PAY_NOW",
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v2/checkout/orders", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	// 幂等键用我们的订单号：重试创建不会产生两笔 PayPal 订单。
	httpReq.Header.Set("PayPal-Request-Id", prepared.OutTradeNo)
	resp, err := g.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("PayPal 下单请求失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("PayPal 下单被拒(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		ID    string `json:"id"`
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("PayPal 下单返回无法解析: %w", err)
	}
	for _, l := range out.Links {
		if l.Rel == "approve" || l.Rel == "payer-action" {
			return l.Href, nil
		}
	}
	return "", fmt.Errorf("PayPal 下单成功但没有 approve 链接(order=%s)", out.ID)
}

// truncateRunes 按字符截断，避免把中文截坏。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// ---- 回调 ----

// VerifyNotify 校验 PayPal webhook 并提取成交信息。
//
// 验签走官方 verify-webhook-signature 接口：把传输头原样回传，由 PayPal 判定真伪。
// 为什么不本地推算签名串：那需要自己算 CRC32、自己拉证书链，任何一步写错都只表现为
// 「验签失败」而极难定位。多一次出网请求换取确定性，对回调这个量级完全值得。
func (g *Gateway) VerifyNotify(cfg payment.ProviderConfig, input payment.NotifyInput) payment.NotifyResult {
	c, err := parseConfig(cfg)
	if err != nil {
		return payment.NotifyResult{Err: err}
	}
	if c.WebhookID == "" {
		return payment.NotifyResult{Err: errors.New("PayPal 通道未配置 webhook_id，无法验签")}
	}
	// PayPal 把传输信息放在这些头里，缺一不可。
	transmissionID := input.Header.Get("Paypal-Transmission-Id")
	transmissionTime := input.Header.Get("Paypal-Transmission-Time")
	transmissionSig := input.Header.Get("Paypal-Transmission-Sig")
	certURL := input.Header.Get("Paypal-Cert-Url")
	authAlgo := input.Header.Get("Paypal-Auth-Algo")
	if transmissionID == "" || transmissionTime == "" || transmissionSig == "" || certURL == "" || authAlgo == "" {
		return payment.NotifyResult{Err: errors.New("PayPal 回调缺少验签所需的传输头")}
	}
	if len(input.RawBody) == 0 {
		return payment.NotifyResult{Err: errors.New("PayPal 回调缺少请求体，无法验签")}
	}

	// 先解析事件，验签通过后才采信内容。
	var event struct {
		ID        string `json:"id"`
		EventType string `json:"event_type"`
		Resource  struct {
			ID       string `json:"id"`
			CustomID string `json:"custom_id"`
			Status   string `json:"status"`
			Amount   struct {
				CurrencyCode string `json:"currency_code"`
				Value        string `json:"value"`
			} `json:"amount"`
			PurchaseUnits []struct {
				CustomID string `json:"custom_id"`
				Amount   struct {
					CurrencyCode string `json:"currency_code"`
					Value        string `json:"value"`
				} `json:"amount"`
			} `json:"purchase_units"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(input.RawBody, &event); err != nil {
		return payment.NotifyResult{Err: fmt.Errorf("PayPal 回调无法解析: %w", err)}
	}

	// 验签在解析之后、采信之前。
	verifyBody, err := json.Marshal(map[string]string{
		"transmission_id":   transmissionID,
		"transmission_time": transmissionTime,
		"cert_url":          certURL,
		"auth_algo":         authAlgo,
		"transmission_sig":  transmissionSig,
		"webhook_id":        c.WebhookID,
		"webhook_event":     string(input.RawBody),
	})
	if err != nil {
		return payment.NotifyResult{Err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	token, err := g.accessToken(ctx, c)
	if err != nil {
		return payment.NotifyResult{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/notifications/verify-webhook-signature", bytes.NewReader(verifyBody))
	if err != nil {
		return payment.NotifyResult{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.http.Do(req)
	if err != nil {
		return payment.NotifyResult{Err: fmt.Errorf("PayPal 验签请求失败: %w", err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return payment.NotifyResult{Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payment.NotifyResult{Err: fmt.Errorf("PayPal 验签被拒(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))}
	}
	var verdict struct {
		Status string `json:"verification_status"`
	}
	if err := json.Unmarshal(body, &verdict); err != nil {
		return payment.NotifyResult{Err: fmt.Errorf("PayPal 验签返回无法解析: %w", err)}
	}
	if !strings.EqualFold(verdict.Status, "SUCCESS") {
		return payment.NotifyResult{Err: fmt.Errorf("PayPal 回调验签未通过: %s", verdict.Status)}
	}

	// 只处理「已收款」事件。PAYMENT.CAPTURE.COMPLETED 是 Orders v2 的标准完成事件。
	switch event.EventType {
	case "PAYMENT.CAPTURE.COMPLETED", "CHECKOUT.ORDER.COMPLETED":
	default:
		return payment.NotifyResult{Err: fmt.Errorf("%w: %s", payment.ErrTradeStatus, event.EventType)}
	}

	// 金额优先取 capture 的 amount；某些事件只在 purchase_units 上带金额。
	value := event.Resource.Amount.Value
	customID := event.Resource.CustomID
	if value == "" && len(event.Resource.PurchaseUnits) > 0 {
		value = event.Resource.PurchaseUnits[0].Amount.Value
		if customID == "" {
			customID = event.Resource.PurchaseUnits[0].CustomID
		}
	}
	amountCents, err := amountToCents(value)
	if err != nil {
		return payment.NotifyResult{Err: fmt.Errorf("PayPal 回调金额非法: %w", err)}
	}
	if amountCents <= 0 {
		return payment.NotifyResult{Err: errors.New("PayPal 回调金额非法")}
	}
	return payment.NotifyResult{
		OK:          true,
		TradeNo:     event.Resource.ID,
		AmountCents: amountCents,
	}
}

// amountToCents 把 PayPal 的两位小数金额字符串转成分。
// 刻意用字符串解析而不是浮点：支付金额不能有浮点误差。
func amountToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("金额为空")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	units, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, err
	}
	var cents int64
	if hasFrac {
		switch {
		case len(fracPart) == 0:
			cents = 0
		case len(fracPart) == 1:
			v, err := strconv.ParseInt(fracPart, 10, 64)
			if err != nil {
				return 0, err
			}
			cents = v * 10
		default:
			// 只取前两位，第三位用于四舍五入。
			two := fracPart[:2]
			v, err := strconv.ParseInt(two, 10, 64)
			if err != nil {
				return 0, err
			}
			cents = v
			if len(fracPart) > 2 && fracPart[2] >= '5' {
				cents++
			}
		}
	}
	total := units*100 + cents
	if neg {
		total = -total
	}
	return total, nil
}
