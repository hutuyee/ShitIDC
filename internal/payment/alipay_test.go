package payment

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hutuyee/ShitIDC/internal/alipaykit"
)

// alipayTestSecrets generates an in-test RSA key pair and returns the
// provider Secret JSON plus the private key for signing callbacks.
func alipayTestSecrets(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	blob, _ := json.Marshal(alipaySecrets{MerchantPrivateKey: string(privPEM), AlipayPublicKey: string(pubPEM)})
	return string(blob), key
}

// alipaySignCallback signs an async notify payload the way the gateway
// would: canonical string -> RSA2 -> base64, then appends sign/sign_type.
func alipaySignCallback(t *testing.T, key *rsa.PrivateKey, values url.Values) url.Values {
	t.Helper()
	sign, err := alipaykit.SignRSA2(key, alipaykit.SignContent(values))
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign", sign)
	values.Set("sign_type", "RSA2")
	return values
}

func TestAlipayNotifyVerify(t *testing.T) {
	secret, key := alipayTestSecrets(t)
	g := AlipayGateway{}
	cfg := ProviderConfig{Method: "alipay", GatewayURL: "https://openapi.alipay.com/gateway.do", MerchantID: "2021000000000000", Secret: secret}

	good := url.Values{
		"app_id":       {cfg.MerchantID},
		"trade_no":     {"2024100422001000000000000001"},
		"out_trade_no": {"O0001"},
		"trade_status": {"TRADE_SUCCESS"},
		"total_amount": {"12.30"},
		"buyer_id":     {"2088000000000000"},
	}
	signed := alipaySignCallback(t, key, (cloneValues(good)))
	res := g.VerifyNotify(cfg, NotifyInput{PostForm: signed, Header: http.Header{}})
	if !res.OK {
		t.Fatalf("valid notify rejected: %v", res.Err)
	}
	if res.AmountCents != 1230 {
		t.Fatalf("amount = %d, want 1230", res.AmountCents)
	}
	if res.TradeNo == "" {
		t.Fatal("trade_no missing")
	}

	// Tampered amount must fail the signature check.
	tampered := (cloneValues(signed))
	tampered.Set("total_amount", "0.01")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: tampered}); res.OK {
		t.Fatal("tampered notify accepted")
	}
	// Missing sign must fail.
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: (cloneValues(good))}); res.OK {
		t.Fatal("unsigned notify accepted")
	}
	// Non-success status must be rejected even with a valid signature.
	bad := (cloneValues(good))
	bad.Set("trade_status", "WAIT_BUYER_PAY")
	signedBad := alipaySignCallback(t, key, bad)
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: signedBad}); res.OK {
		t.Fatal("WAIT_BUYER_PAY accepted")
	}
}

func TestAlipayPayURLBuildsSignedRedirect(t *testing.T) {
	secret, _ := alipayTestSecrets(t)
	g := AlipayGateway{}
	cfg := ProviderConfig{Method: "alipay", GatewayURL: "https://openapi.alipay.com/gateway.do", MerchantID: "2021000000000000", Secret: secret}
	payURL, err := g.PayURL(nil, cfg, Prepared{OutTradeNo: "O0001", Subject: "香港 VPS", AmountCents: 5000, Currency: "CNY", NotifyURL: "https://s.example/api/v1/pay/notify/alipay", ReturnURL: "https://s.example/"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(payURL, cfg.GatewayURL+"?") {
		t.Fatalf("unexpected url: %s", payURL)
	}
	if !strings.Contains(payURL, "sign=") || !strings.Contains(payURL, "biz_content=") {
		t.Fatalf("url missing signature/biz_content: %s", payURL)
	}
}

func TestAlipayMoneyRoundTrip(t *testing.T) {
	if got := alipayMoney(1230); got != "12.30" {
		t.Fatalf("alipayMoney(1230) = %s", got)
	}
	if got := alipayMoney(5); got != "0.05" {
		t.Fatalf("alipayMoney(5) = %s", got)
	}
	cents, err := alipayYuanToCents("0.05")
	if err != nil || cents != 5 {
		t.Fatalf("alipayYuanToCents(0.05) = %d, %v", cents, err)
	}
}

func TestStripeWebhookSignature(t *testing.T) {
	// Independent construction of Stripe's scheme: hex(hmac_sha256(secret, t + "." + body)).
	body := []byte(`{"type":"checkout.session.completed"}`)
	secret := "whsec_test"
	signedAt := time.Now().Unix() // within tolerance
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(signedAt, 10) + "."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	header := "t=" + strconv.FormatInt(signedAt, 10) + ",v1=" + sig
	if !verifyStripeSignature(header, secret, body, 5*time.Minute) {
		t.Fatal("valid stripe signature rejected")
	}
	// Wrong secret.
	if verifyStripeSignature(header, "whsec_other", body, 5*time.Minute) {
		t.Fatal("signature from another secret accepted")
	}
	// Tampered body.
	if verifyStripeSignature(header, secret, []byte(`{"type":"x"}`), 5*time.Minute) {
		t.Fatal("tampered body accepted")
	}
	// Stale timestamp beyond tolerance.
	old := "1000000000"
	mac2 := hmac.New(sha256.New, []byte(secret))
	mac2.Write([]byte(old + "."))
	mac2.Write(body)
	stale := "t=1000000000,v1=" + hex.EncodeToString(mac2.Sum(nil))
	if verifyStripeSignature(stale, secret, body, 5*time.Minute) {
		t.Fatal("stale signature accepted (replay)")
	}
}

// cloneValues copies a form without depending on newer stdlib helpers.
func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
