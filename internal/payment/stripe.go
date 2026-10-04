package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// StripeGateway implements Stripe Checkout ( Stripe v1 REST API).
//
// Provider fields:
//   - MerchantID: unused (kept for display)
//   - GatewayURL: optional API override, default https://api.stripe.com
//   - Secret:     JSON {"secret_key":"sk_live_...","webhook_secret":"whsec_..."}
//     (a plain sk_... string is accepted; webhook verification is then off)
type StripeGateway struct{}

func (StripeGateway) Method() string { return "stripe" }

type stripeSecrets struct {
	SecretKey     string `json:"secret_key"`
	WebhookSecret string `json:"webhook_secret"`
}

func parseStripeSecret(raw string) (stripeSecrets, error) {
	var s stripeSecrets
	if err := json.Unmarshal([]byte(raw), &s); err != nil || s.SecretKey == "" {
		// Plain secret key without a webhook secret.
		if strings.HasPrefix(strings.TrimSpace(raw), "sk_") {
			return stripeSecrets{SecretKey: strings.TrimSpace(raw)}, nil
		}
		if err != nil && !strings.Contains(raw, "secret_key") {
			return stripeSecrets{}, errors.New("stripe 密钥需为 JSON {\"secret_key\",\"webhook_secret\"} 或 sk_ 开头字符串")
		}
		return stripeSecrets{}, errors.New("stripe secret_key 缺失")
	}
	return s, nil
}

func (StripeGateway) apiBase(cfg ProviderConfig) string {
	if u := strings.TrimRight(cfg.GatewayURL, "/"); u != "" && u != "https://api.stripe.com" {
		return u
	}
	return "https://api.stripe.com"
}

func stripeRequest(ctx context.Context, apiBase, path, secret string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(secret, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrGatewayAPI{Detail: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return body, ErrGatewayAPI{Detail: fmt.Sprintf("stripe %s: HTTP %d: %s", path, resp.StatusCode, truncate(body, 300))}
	}
	return body, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

// PayURL creates a Checkout Session and returns its hosted payment URL.
func (g StripeGateway) PayURL(ctx context.Context, cfg ProviderConfig, p Prepared) (string, error) {
	s, err := parseStripeSecret(cfg.Secret)
	if err != nil {
		return "", err
	}
	if p.AmountCents <= 0 {
		return "", errors.New("支付金额必须大于 0")
	}
	successURL := p.ReturnURL + "?out_trade_no=" + url.QueryEscape(p.OutTradeNo) + "&session_id={CHECKOUT_SESSION_ID}"
	cancelURL := p.ReturnURL + "?out_trade_no=" + url.QueryEscape(p.OutTradeNo) + "&cancelled=1"
	form := url.Values{
		"mode":                                          {"payment"},
		"success_url":                                   {successURL},
		"cancel_url":                                    {cancelURL},
		"client_reference_id":                           {p.OutTradeNo},
		"metadata[out_trade_no]":                        {p.OutTradeNo},
		"line_items[0][quantity]":                       {"1"},
		"line_items[0][price_data][currency]":           {strings.ToLower(p.Currency)},
		"line_items[0][price_data][unit_amount]":        {strconv.FormatInt(p.AmountCents, 10)},
		"line_items[0][price_data][product_data][name]": {p.Subject},
	}
	if s.WebhookSecret == "" {
		// Without a webhook secret there is no trustworthy async callback;
		// success_url polling verifies the session server-side instead.
		form.Set("metadata[no_webhook]", "true")
	}
	body, err := stripeRequest(ctx, g.apiBase(cfg), "/v1/checkout/sessions", s.SecretKey, form)
	if err != nil {
		return "", err
	}
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
		Err *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", ErrGatewayAPI{Detail: "decode session: " + truncate(body, 200)}
	}
	if out.Err != nil {
		return "", ErrGatewayAPI{Detail: out.Err.Message}
	}
	if out.URL == "" {
		return "", ErrGatewayAPI{Detail: "stripe session has no redirect url"}
	}
	return out.URL, nil
}

// VerifyNotify checks the Stripe-Signature header (t=...,v1=...) against the
// exact raw body, then parses the checkout.session.completed event.
func (g StripeGateway) VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult {
	fail := func(err error) NotifyResult { return NotifyResult{OK: false, Err: err} }
	s, err := parseStripeSecret(cfg.Secret)
	if err != nil {
		return fail(err)
	}
	sigHeader := input.Header.Get("Stripe-Signature")
	if sigHeader == "" || s.WebhookSecret == "" {
		return fail(errors.New("缺少 Stripe-Signature 头或未配置 webhook_secret"))
	}
	if !verifyStripeSignature(sigHeader, s.WebhookSecret, input.RawBody, 5*time.Minute) {
		return fail(errors.New("Stripe webhook 签名验证失败"))
	}
	var evt struct {
		Type string `json:"type"`
		Data struct {
			Obj struct {
				ID                string `json:"id"`
				ClientReferenceID string `json:"client_reference_id"`
				PaymentIntent     string `json:"payment_intent"`
				AmountTotal       int64  `json:"amount_total"`
				Currency          string `json:"currency"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(input.RawBody, &evt); err != nil {
		return fail(errors.New("webhook body 解析失败"))
	}
	if evt.Type != "checkout.session.completed" {
		return fail(fmt.Errorf("忽略事件 %s", evt.Type))
	}
	if evt.Data.Obj.ClientReferenceID == "" {
		return fail(errors.New("session 缺少 client_reference_id"))
	}
	return NotifyResult{OK: true, TradeNo: firstNonEmpty(evt.Data.Obj.PaymentIntent, evt.Data.Obj.ID), AmountCents: evt.Data.Obj.AmountTotal}
}

// verifyStripeSignature validates one v1 signature with a timestamp drift
// budget, per Stripe's webhook signing scheme: HMAC-SHA256(secret, "t.body").
func verifyStripeSignature(header, secret string, body []byte, tolerance time.Duration) bool {
	var t string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			t = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if t == "" || len(sigs) == 0 {
		return false
	}
	ts, err := strconv.ParseInt(t, 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > tolerance || time.Unix(ts, 0).After(time.Now().Add(tolerance)) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t + "."))
	mac.Write(body)
	expected := mac.Sum(nil)
	ok := false
	for _, sig := range sigs {
		got, err := hex.DecodeString(sig)
		if err != nil {
			continue
		}
		if hmac.Equal(expected, got) {
			ok = true
		}
	}
	return ok
}

// Refund issues a gateway-side refund (自动退款). Idempotent per our refund id.
func (g StripeGateway) Refund(ctx context.Context, cfg ProviderConfig, r RefundRequest) (string, error) {
	s, err := parseStripeSecret(cfg.Secret)
	if err != nil {
		return "", err
	}
	form := url.Values{"payment_intent": {r.TradeNo}, "amount": {strconv.FormatInt(r.AmountCents, 10)}}
	body, err := stripeRequest(ctx, g.apiBase(cfg), "/v1/refunds", s.SecretKey, form)
	if err != nil {
		return "", err
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Err    *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", ErrGatewayAPI{Detail: "decode refund: " + truncate(body, 200)}
	}
	if out.Err != nil {
		return "", ErrGatewayAPI{Detail: out.Err.Message}
	}
	if out.Status != "succeeded" && out.Status != "pending" {
		return "", ErrGatewayAPI{Detail: "refund status " + out.Status}
	}
	return out.ID, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func init() {
	Register(StripeGateway{})
}
