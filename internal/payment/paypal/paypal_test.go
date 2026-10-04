package paypal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/payment"
)

// 支付金额绝不能有浮点误差，逐条钉死。
func TestAmountToCents(t *testing.T) {
	cases := map[string]int64{
		"10":         1000,
		"10.00":      1000,
		"10.5":       1050,
		"0.01":       1,
		"0.1":        10,
		"0.005":      1, // 第三位四舍五入
		"0.004":      0, // 不到半分
		"123.45":     12345,
		"1000000.99": 100000099,
	}
	for in, want := range cases {
		got, err := amountToCents(in)
		if err != nil {
			t.Fatalf("amountToCents(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("amountToCents(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := amountToCents(""); err == nil {
		t.Fatal("an empty amount must be rejected")
	}
	if _, err := amountToCents("abc"); err == nil {
		t.Fatal("a non-numeric amount must be rejected")
	}
	// 负数要保留符号（退款事件会用到）。
	if got, err := amountToCents("-1.50"); err != nil || got != -150 {
		t.Fatalf("amountToCents(-1.50) = %d/%v, want -150", got, err)
	}
}

func TestParseConfigForms(t *testing.T) {
	// JSON 形式。
	c, err := parseConfig(payment.ProviderConfig{
		Secret: `{"client_id":"id1","secret":"sec1","sandbox":true,"webhook_id":"WH1"}`,
	})
	if err != nil {
		t.Fatalf("json form rejected: %v", err)
	}
	if c.ClientID != "id1" || c.Secret != "sec1" || !c.Sandbox || c.WebhookID != "WH1" {
		t.Fatalf("json form not parsed: %+v", c)
	}
	if c.BaseURL != sandboxBase {
		t.Fatalf("sandbox base = %q, want %q", c.BaseURL, sandboxBase)
	}
	// "clientid:secret" 简写。
	c2, err := parseConfig(payment.ProviderConfig{Secret: "id2:sec2"})
	if err != nil {
		t.Fatalf("colon form rejected: %v", err)
	}
	if c2.ClientID != "id2" || c2.Secret != "sec2" {
		t.Fatalf("colon form not parsed: %+v", c2)
	}
	if c2.BaseURL != liveBase {
		t.Fatalf("live base = %q, want %q", c2.BaseURL, liveBase)
	}
	// 缺凭据必须被拒。
	if _, err := parseConfig(payment.ProviderConfig{}); err == nil {
		t.Fatal("an empty secret must be rejected")
	}
	if _, err := parseConfig(payment.ProviderConfig{Secret: "onlysecret"}); err == nil {
		t.Fatal("a missing client_id must be rejected")
	}
	// MerchantID 填 client_id 的常见后台填法。
	c3, err := parseConfig(payment.ProviderConfig{MerchantID: "id3", Secret: "sec3"})
	if err != nil {
		t.Fatalf("merchant id form rejected: %v", err)
	}
	if c3.ClientID != "id3" {
		t.Fatalf("client id = %q, want id3", c3.ClientID)
	}
}

// fakePayPal 起一个假的 PayPal API，按路径返回不同响应。
// 同时统计 token 请求次数，用来验证 token 被缓存了。
type fakePayPal struct {
	server     *httptest.Server
	tokenHits  int64
	orderBody  []byte
	verifyBody []byte
	lastAuth   string
	lastReqID  string
}

func newFakePayPal(t *testing.T) *fakePayPal {
	t.Helper()
	f := &fakePayPal{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/oauth2/token"):
			atomic.AddInt64(&f.tokenHits, 1)
			_, _ = w.Write([]byte(`{"access_token":"TOKEN-1","expires_in":32400}`))
		case strings.HasSuffix(r.URL.Path, "/v2/checkout/orders"):
			body, _ := io.ReadAll(r.Body)
			f.orderBody = body
			f.lastReqID = r.Header.Get("PayPal-Request-Id")
			_, _ = w.Write([]byte(`{"id":"ORDER-1","links":[{"rel":"self","href":"https://api/self"},{"rel":"approve","href":"https://pay.example.com/approve/ORDER-1"}]}`))
		case strings.HasSuffix(r.URL.Path, "/v1/notifications/verify-webhook-signature"):
			body, _ := io.ReadAll(r.Body)
			f.verifyBody = body
			_, _ = w.Write([]byte(`{"verification_status":"SUCCESS"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePayPal) cfg() payment.ProviderConfig {
	secret, _ := json.Marshal(Config{
		ClientID: "id", Secret: "sec", BaseURL: f.server.URL, WebhookID: "WH-1", AllowPrivate: true,
	})
	return payment.ProviderConfig{Secret: string(secret), Method: "paypal"}
}

func TestPayURLCreatesOrder(t *testing.T) {
	f := newFakePayPal(t)
	g := New()
	got, err := g.PayURL(context.Background(), f.cfg(), payment.Prepared{
		OutTradeNo: "O-100", AmountCents: 1250, Currency: "usd", Subject: "云服务器", ReturnURL: "https://s.example.com/r",
	})
	if err != nil {
		t.Fatalf("pay url: %v", err)
	}
	if got != "https://pay.example.com/approve/ORDER-1" {
		t.Fatalf("pay url = %q, want the approve link", got)
	}
	// 请求体必须带上订单号与规范化后的币种金额。
	var sent map[string]any
	if err := json.Unmarshal(f.orderBody, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent["intent"] != "CAPTURE" {
		t.Fatalf("intent = %v, want CAPTURE", sent["intent"])
	}
	units := sent["purchase_units"].([]any)[0].(map[string]any)
	if units["custom_id"] != "O-100" {
		t.Fatalf("custom_id = %v, want O-100", units["custom_id"])
	}
	amt := units["amount"].(map[string]any)
	if amt["value"] != "12.50" {
		t.Fatalf("amount value = %v, want 12.50", amt["value"])
	}
	if amt["currency_code"] != "USD" {
		t.Fatalf("currency = %v, want USD (upper-cased)", amt["currency_code"])
	}
	// 幂等键必须是我们的订单号。
	if f.lastReqID != "O-100" {
		t.Fatalf("PayPal-Request-Id = %q, want O-100", f.lastReqID)
	}
	// Bearer token 必须带上。
	if f.lastAuth != "Bearer TOKEN-1" {
		t.Fatalf("authorization = %q", f.lastAuth)
	}
}

// token 必须被缓存：每笔订单都取一次 token 又慢又容易被限流。
func TestAccessTokenIsCached(t *testing.T) {
	f := newFakePayPal(t)
	g := New()
	for i := 0; i < 3; i++ {
		if _, err := g.PayURL(context.Background(), f.cfg(), payment.Prepared{
			OutTradeNo: "O-" + string(rune('a'+i)), AmountCents: 100, Currency: "USD",
		}); err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
	}
	if hits := atomic.LoadInt64(&f.tokenHits); hits != 1 {
		t.Fatalf("token endpoint called %d times for 3 orders, want 1 (cached)", hits)
	}
}

func TestPayURLSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/v1/oauth2/token") {
			_, _ = w.Write([]byte(`{"access_token":"T","expires_in":3600}`))
			return
		}
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"name":"UNPROCESSABLE_ENTITY","message":"币种不支持"}`))
	}))
	t.Cleanup(srv.Close)
	secret, _ := json.Marshal(Config{ClientID: "id", Secret: "sec", BaseURL: srv.URL, AllowPrivate: true})
	g := New()
	_, err := g.PayURL(context.Background(), payment.ProviderConfig{Secret: string(secret)}, payment.Prepared{OutTradeNo: "O", AmountCents: 100, Currency: "XYZ"})
	if err == nil {
		t.Fatal("a 422 response must be an error")
	}
	if !strings.Contains(err.Error(), "币种不支持") {
		t.Fatalf("error %q lost the API message", err.Error())
	}
}

// buildWebhook 造一个带正确传输头的 PayPal 回调。
func buildWebhook(t *testing.T, eventType, customID, amount string) payment.NotifyInput {
	t.Helper()
	event := map[string]any{
		"id":         "WH-EVT-1",
		"event_type": eventType,
		"resource": map[string]any{
			"id":        "CAPTURE-9",
			"custom_id": customID,
			"status":    "COMPLETED",
			"amount":    map[string]string{"currency_code": "USD", "value": amount},
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Paypal-Transmission-Id", "TID-1")
	header.Set("Paypal-Transmission-Time", "2024-01-01T00:00:00Z")
	header.Set("Paypal-Transmission-Sig", "SIG-1")
	header.Set("Paypal-Cert-Url", "https://api.paypal.com/cert.pem")
	header.Set("Paypal-Auth-Algo", "SHA256withRSA")
	return payment.NotifyInput{RawBody: body, Header: header}
}

func TestVerifyNotifyHappyPath(t *testing.T) {
	f := newFakePayPal(t)
	g := New()
	res := g.VerifyNotify(f.cfg(), buildWebhook(t, "PAYMENT.CAPTURE.COMPLETED", "O-100", "12.50"))
	if !res.OK {
		t.Fatalf("notify rejected: %v", res.Err)
	}
	if res.AmountCents != 1250 {
		t.Fatalf("amount = %d, want 1250", res.AmountCents)
	}
	if res.TradeNo != "CAPTURE-9" {
		t.Fatalf("trade no = %q, want CAPTURE-9", res.TradeNo)
	}
	// 验签请求必须把传输头与 webhook_id 原样带回给 PayPal。
	var sent map[string]string
	if err := json.Unmarshal(f.verifyBody, &sent); err != nil {
		t.Fatalf("verification body is not JSON: %v", err)
	}
	if sent["transmission_id"] != "TID-1" || sent["webhook_id"] != "WH-1" {
		t.Fatalf("verification body missing fields: %+v", sent)
	}
	if sent["auth_algo"] != "SHA256withRSA" {
		t.Fatalf("auth_algo = %q", sent["auth_algo"])
	}
	// webhook_event 必须是原始请求体，不能是被重新序列化过的。
	if !strings.Contains(sent["webhook_event"], "CAPTURE-9") {
		t.Fatalf("webhook_event lost the raw payload: %q", sent["webhook_event"])
	}
}

func TestVerifyNotifyRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/v1/oauth2/token") {
			_, _ = w.Write([]byte(`{"access_token":"T","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"verification_status":"FAILURE"}`))
	}))
	t.Cleanup(srv.Close)
	secret, _ := json.Marshal(Config{ClientID: "id", Secret: "sec", BaseURL: srv.URL, WebhookID: "WH-1", AllowPrivate: true})
	g := New()
	res := g.VerifyNotify(payment.ProviderConfig{Secret: string(secret)}, buildWebhook(t, "PAYMENT.CAPTURE.COMPLETED", "O-1", "1.00"))
	if res.OK {
		t.Fatal("a FAILURE verification_status must be rejected")
	}
}

func TestVerifyNotifyRequiresHeadersAndWebhookID(t *testing.T) {
	f := newFakePayPal(t)
	g := New()
	input := buildWebhook(t, "PAYMENT.CAPTURE.COMPLETED", "O-1", "1.00")
	// 缺一个传输头就必须被拒，而不是当成通过。
	input.Header.Del("Paypal-Transmission-Sig")
	if res := g.VerifyNotify(f.cfg(), input); res.OK {
		t.Fatal("a callback without the signature header must be rejected")
	}
	// 没配 webhook_id 就无法验签。
	secret, _ := json.Marshal(Config{ClientID: "id", Secret: "sec", BaseURL: f.server.URL, AllowPrivate: true})
	res := g.VerifyNotify(payment.ProviderConfig{Secret: string(secret)}, buildWebhook(t, "PAYMENT.CAPTURE.COMPLETED", "O-1", "1.00"))
	if res.OK {
		t.Fatal("verification without a webhook_id must fail")
	}
}

func TestVerifyNotifyRejectsUnrelatedEvent(t *testing.T) {
	f := newFakePayPal(t)
	g := New()
	// 验签通过但事件类型不是「已收款」：必须返回 ErrTradeStatus 而不是当成付款成功。
	res := g.VerifyNotify(f.cfg(), buildWebhook(t, "CHECKOUT.ORDER.APPROVED", "O-1", "1.00"))
	if res.OK {
		t.Fatal("a non-capture event must not be treated as paid")
	}
	if res.Err == nil {
		t.Fatal("expected an error for an unrelated event")
	}
}

func TestRegistryKnowsPayPal(t *testing.T) {
	p, ok := payment.Get("paypal")
	if !ok {
		t.Fatal("the paypal gateway must be registered")
	}
	if p.Method() != "paypal" {
		t.Fatalf("method = %q, want paypal", p.Method())
	}
}

var _ = url.Values{}
