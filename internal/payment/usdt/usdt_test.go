package epusdt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/payment"
)

// 官方文档 wiki/API.md 给出的例子，签名必须能复算出同样的值。
// 文档原文：order_id=20220201030210321, amount=42,
// notify_url=http://example.com/notify, redirect_url=http://example.com/redirect,
// token=epusdt_password_xasddawqe -> signature=1cd4b52df5587cfb1968b0c0c6e156cd
func TestSignMatchesOfficialExample(t *testing.T) {
	params := map[string]string{
		"order_id":     "20220201030210321",
		"amount":       "42",
		"notify_url":   "http://example.com/notify",
		"redirect_url": "http://example.com/redirect",
	}
	got := Sign(params, "epusdt_password_xasddawqe")
	if got != "1cd4b52df5587cfb1968b0c0c6e156cd" {
		t.Fatalf("Sign() = %s, want the documented 1cd4b52df5587cfb1968b0c0c6e156cd", got)
	}
}

func TestSignIgnoresEmptyValuesAndSignature(t *testing.T) {
	base := map[string]string{"order_id": "1", "amount": "2"}
	want := Sign(base, "tok")
	withEmpty := map[string]string{"order_id": "1", "amount": "2", "redirect_url": ""}
	if got := Sign(withEmpty, "tok"); got != want {
		t.Fatalf("empty values changed the signature: %s vs %s", got, want)
	}
	withSig := map[string]string{"order_id": "1", "amount": "2", "signature": "deadbeef"}
	if got := Sign(withSig, "tok"); got != want {
		t.Fatalf("the signature field changed the signature: %s vs %s", got, want)
	}
}

func TestSignSortsByASCII(t *testing.T) {
	a := Sign(map[string]string{"order_id": "1", "amount": "2", "trade_type": "trc20"}, "tok")
	b := Sign(map[string]string{"trade_type": "trc20", "amount": "2", "order_id": "1"}, "tok")
	if a != b {
		t.Fatal("signature depends on map order, so parameters are not being sorted")
	}
}

func TestSignIsLowercase32Hex(t *testing.T) {
	got := Sign(map[string]string{"a": "1"}, "tok")
	if len(got) != 32 {
		t.Fatalf("signature length = %d, want 32", len(got))
	}
	if got != strings.ToLower(got) {
		t.Fatalf("signature %s is not lower-case", got)
	}
}

func TestVerifySign(t *testing.T) {
	params := map[string]string{"order_id": "1", "amount": "2"}
	params["signature"] = Sign(params, "tok")
	if !VerifySign(params, "tok") {
		t.Fatal("a valid signature was rejected")
	}
	if VerifySign(params, "wrong-token") {
		t.Fatal("a signature was accepted under the wrong token")
	}
	tampered := map[string]string{"order_id": "1", "amount": "3", "signature": params["signature"]}
	if VerifySign(tampered, "tok") {
		t.Fatal("a tampered amount was accepted")
	}
	if VerifySign(map[string]string{"order_id": "1"}, "tok") {
		t.Fatal("a callback with no signature was accepted")
	}
}

func TestParseConfig(t *testing.T) {
	if _, err := parseConfig(payment.ProviderConfig{GatewayURL: "https://e.example.com"}); err == nil {
		t.Fatal("a missing token must be rejected")
	}
	if _, err := parseConfig(payment.ProviderConfig{Secret: "tok"}); err == nil {
		t.Fatal("a missing gateway url must be rejected")
	}
	c, err := parseConfig(payment.ProviderConfig{GatewayURL: "https://e.example.com", Secret: "raw-token"})
	if err != nil {
		t.Fatalf("plain token rejected: %v", err)
	}
	if c.Token != "raw-token" {
		t.Fatalf("token = %q, want raw-token", c.Token)
	}
	if c.Currency != "CNY" || c.TradeType != "trc20" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	c2, err := parseConfig(payment.ProviderConfig{
		GatewayURL: "https://e.example.com",
		Secret:     `{"token":"t2","currency":"USD","trade_type":"erc20"}`,
	})
	if err != nil {
		t.Fatalf("json secret rejected: %v", err)
	}
	if c2.Token != "t2" || c2.Currency != "USD" || c2.TradeType != "erc20" {
		t.Fatalf("json secret not parsed: %+v", c2)
	}
}

// captureEpusdt 起一个假网关，记录表单并返回可控响应。
// 返回的 baseURL 就是假网关地址，直接当 GatewayURL 用。
func captureEpusdt(t *testing.T, respond string) (*Gateway, *url.Values, string) {
	t.Helper()
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respond))
	}))
	t.Cleanup(srv.Close)
	return New(), &captured, srv.URL
}

func TestPayURLSignsRequest(t *testing.T) {
	g, captured, base := captureEpusdt(t, `{"status_code":200,"message":"ok","data":{"trade_id":"T1","payment_url":"https://pay.example.com/T1"}}`)
	// 假网关在回环地址上，必须显式允许内网目标——SSRF 防护默认会拦截。
	cfg := payment.ProviderConfig{GatewayURL: base, Secret: `{"token":"tok","allow_private":true}`, Method: "usdt", MerchantID: "m"}
	// AllowPrivate 关掉 SSRF 拦截：假网关就在回环地址上。
	payURL, err := g.PayURL(context.Background(), cfg, payment.Prepared{
		OutTradeNo: "O-1", AmountCents: 4200, Subject: "云服务器",
		NotifyURL: "https://s.example.com/n", ReturnURL: "https://s.example.com/r",
	})
	if err != nil {
		t.Fatalf("pay url: %v", err)
	}
	if payURL != "https://pay.example.com/T1" {
		t.Fatalf("pay url = %q, want the gateway cashier url", payURL)
	}
	form := *captured
	if form.Get("order_id") != "O-1" {
		t.Fatalf("order_id = %q", form.Get("order_id"))
	}
	if form.Get("amount") != "42.00" {
		t.Fatalf("amount = %q, want 42.00", form.Get("amount"))
	}
	params := map[string]string{}
	for k := range form {
		if k == "signature" {
			continue
		}
		params[k] = form.Get(k)
	}
	if got, want := form.Get("signature"), Sign(params, "tok"); got != want {
		t.Fatalf("signature %s does not verify (want %s)", got, want)
	}
}

func TestPayURLSurfacesGatewayError(t *testing.T) {
	g, _, base := captureEpusdt(t, `{"status_code":10001,"message":"签名错误"}`)
	_, err := g.PayURL(context.Background(), payment.ProviderConfig{GatewayURL: base, Secret: `{"token":"tok","allow_private":true}`}, payment.Prepared{OutTradeNo: "O", AmountCents: 100})
	if err == nil {
		t.Fatal("a non-200 status_code must be an error")
	}
	if !strings.Contains(err.Error(), "签名错误") {
		t.Fatalf("error %q lost the gateway message", err.Error())
	}
}

func TestYuanToCents(t *testing.T) {
	cases := map[string]int64{"42": 4200, "42.00": 4200, "0.01": 1, "99.99": 9999}
	for in, want := range cases {
		got, err := yuanToCents(in)
		if err != nil {
			t.Fatalf("yuanToCents(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("yuanToCents(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := yuanToCents("abc"); err == nil {
		t.Fatal("a non-numeric amount must be rejected")
	}
	if _, err := yuanToCents("-1"); err == nil {
		t.Fatal("a negative amount must be rejected")
	}
}

func TestVerifyNotifyHappyPath(t *testing.T) {
	g := New()
	cfg := payment.ProviderConfig{GatewayURL: "https://e.example.com", Secret: "tok"}
	params := map[string]string{
		"trade_id": "T-9", "order_id": "O-9", "amount": "12.34", "actual_amount": "1.8", "token": "USDT",
	}
	params["signature"] = Sign(params, "tok")
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	res := g.VerifyNotify(cfg, payment.NotifyInput{PostForm: values})
	if !res.OK {
		t.Fatalf("notify rejected: %v", res.Err)
	}
	if res.AmountCents != 1234 {
		t.Fatalf("amount = %d, want 1234", res.AmountCents)
	}
	if res.TradeNo != "T-9" {
		t.Fatalf("trade no = %q, want T-9", res.TradeNo)
	}
	// 改掉金额后必须被拒——否则可以花 1 分钱开通一台机器。
	tampered := url.Values{}
	for k, v := range params {
		tampered.Set(k, v)
	}
	tampered.Set("amount", "0.01")
	if r := g.VerifyNotify(cfg, payment.NotifyInput{PostForm: tampered}); r.OK {
		t.Fatal("a tampered amount was accepted")
	}
}
