// Package epusdt 实现 USDT 收款通道（Epusdt / BEpusdt）。
//
// 签名算法取自 Epusdt 官方文档 wiki/API.md「签名算法」：
//
//  1. 所有非空参数按参数名 ASCII 字典序排序，拼成 key1=value1&key2=value2
//  2. 末尾直接拼接 api token（不加分隔符）
//  3. MD5，转小写，得到 32 字节的 signature
//
// 注意第 2 步：token 是**直接拼在字符串末尾**的，不是 HMAC、也不加 & 或 :。
// 文档给的例子可以直接复算（见测试），所以这里的实现有据可依。
package epusdt

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/payment"
	"github.com/hutuyee/ShitIDC/internal/security"
)

// Config 是 USDT 通道的配置（放在 payment_providers.config 里）。
type Config struct {
	// Token 是 Epusdt 的「api 接口认证 token」，参与签名。
	Token string `json:"token"`
	// Currency 是计价法币，默认 CNY。Epusdt 按法币金额折算 USDT。
	Currency string `json:"currency"`
	// TradeType 是收款链，如 trc20 / erc20。
	TradeType    string `json:"trade_type"`
	AllowPrivate bool   `json:"allow_private"`
}

// Gateway 实现 payment.Gateway。
type Gateway struct {
	http *http.Client
	// Now 允许测试注入时间，保证请求体可复算。
	Now func() time.Time
}

// New 构造通道。
func New() *Gateway {
	return &Gateway{
		http: &http.Client{Timeout: 20 * time.Second},
		Now:  time.Now,
	}
}

// NewWithHTTPClient 与 New 相同，但允许注入 HTTP 客户端。
// 生产代码用 New 即可；这个注入点是为了测试——SSRF 防护会把回环地址挡在门外，
// 而测试用的假网关就跑在回环地址上。
func NewWithHTTPClient(c *http.Client) *Gateway {
	if c == nil {
		return New()
	}
	return &Gateway{http: c, Now: time.Now}
}

// Method 返回通道标识。
func (g *Gateway) Method() string { return "usdt" }

func init() { payment.Register(New()) }

// parseConfig 解析通道配置。
func parseConfig(cfg payment.ProviderConfig) (Config, error) {
	var c Config
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return c, errors.New("USDT 通道缺少 api token")
	}
	if strings.HasPrefix(secret, "{") {
		if err := json.Unmarshal([]byte(secret), &c); err != nil {
			return c, fmt.Errorf("USDT 通道配置不是合法 JSON: %w", err)
		}
	}
	// 纯字符串凭据就直接当 token 用（最常见的填法）。
	if c.Token == "" {
		c.Token = secret
	}
	if strings.TrimSpace(cfg.GatewayURL) == "" {
		return c, errors.New("USDT 通道缺少网关地址")
	}
	if c.Currency == "" {
		c.Currency = "CNY"
	}
	if c.TradeType == "" {
		c.TradeType = "trc20"
	}
	return c, nil
}

// Sign 按官方算法计算签名：非空参数按名排序拼接，末尾直接接 token，再取 MD5 小写。
func Sign(params map[string]string, token string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "signature" {
			continue
		}
		// 空值不参与签名。
		if strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(params[k])
	}
	sb.WriteString(token)
	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// VerifySign 校验回调签名，使用常量时间比较避免时序泄漏。
func VerifySign(params map[string]string, token string) bool {
	got := strings.ToLower(strings.TrimSpace(params["signature"]))
	if got == "" {
		return false
	}
	want := Sign(params, token)
	return subtleConstantTimeEqual(got, want)
}

// subtleConstantTimeEqual 用标准库做常量时间比较。
func subtleConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- 下单 ----

// createResponse 是 Epusdt 下单接口的返回。
type createResponse struct {
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
	Data       struct {
		TradeID        string `json:"trade_id"`
		OrderID        string `json:"order_id"`
		Amount         string `json:"amount"`
		ActualAmount   string `json:"actual_amount"`
		Token          string `json:"token"`
		ExpirationTime int64  `json:"expiration_time"`
		PaymentURL     string `json:"payment_url"`
	} `json:"data"`
}

// PayURL 调 Epusdt 的 create-transaction 接口，返回收银台地址。
func (g *Gateway) PayURL(ctx context.Context, cfg payment.ProviderConfig, p payment.Prepared) (string, error) {
	c, err := parseConfig(cfg)
	if err != nil {
		return "", err
	}
	amount := strconv.FormatFloat(float64(p.AmountCents)/100, 'f', 2, 64)
	params := map[string]string{
		"order_id":     p.OutTradeNo,
		"amount":       amount,
		"notify_url":   p.NotifyURL,
		"redirect_url": p.ReturnURL,
		"trade_type":   c.TradeType,
		"name":         truncateRunes(p.Subject, 64),
		"currency":     c.Currency,
	}
	params["signature"] = Sign(params, c.Token)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	target := strings.TrimRight(cfg.GatewayURL, "/") + "/api/v1/order/create-transaction"
	if !c.AllowPrivate {
		if err := security.ValidateOutboundURL(target, false); err != nil {
			return "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("USDT 下单请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out createResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("USDT 下单返回无法解析(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// Epusdt 用 status_code=200 表示成功。
	if out.StatusCode != 200 {
		return "", fmt.Errorf("USDT 下单失败: %s (%d)", out.Message, out.StatusCode)
	}
	if strings.TrimSpace(out.Data.PaymentURL) == "" {
		return "", errors.New("USDT 下单成功但没有返回收银台地址")
	}
	return out.Data.PaymentURL, nil
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

// VerifyNotify 校验 Epusdt 的异步通知。
//
// 回调参数（form 或 query 均可）：trade_id / order_id / amount / actual_amount /
// token / block_transaction_id / signature。签名覆盖除 signature 外的所有非空参数。
func (g *Gateway) VerifyNotify(cfg payment.ProviderConfig, input payment.NotifyInput) payment.NotifyResult {
	c, err := parseConfig(cfg)
	if err != nil {
		return payment.NotifyResult{Err: err}
	}
	values := input.Values()
	params := map[string]string{}
	for k := range values {
		params[k] = values.Get(k)
	}
	if !VerifySign(params, c.Token) {
		return payment.NotifyResult{Err: errors.New("USDT 回调签名校验失败")}
	}
	orderID := strings.TrimSpace(params["order_id"])
	if orderID == "" {
		return payment.NotifyResult{Err: errors.New("USDT 回调缺少 order_id")}
	}
	// amount 是法币金额（元），换算成分。
	amountCents, err := yuanToCents(params["amount"])
	if err != nil {
		return payment.NotifyResult{Err: fmt.Errorf("USDT 回调金额非法: %w", err)}
	}
	if amountCents <= 0 {
		return payment.NotifyResult{Err: errors.New("USDT 回调金额非法")}
	}
	return payment.NotifyResult{
		OK:          true,
		TradeNo:     firstNonEmptyStr(params["trade_id"], params["block_transaction_id"], orderID),
		AmountCents: amountCents,
	}
}

// yuanToCents 把「元」字符串转成分，四舍五入到分。
func yuanToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("金额为空")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, errors.New("金额为负数")
	}
	return int64(f*100 + 0.5), nil
}

// firstNonEmptyStr 返回第一个非空字符串。
func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
