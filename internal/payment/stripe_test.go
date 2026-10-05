package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// 手算 Stripe webhook 签名头：HMAC-SHA256(whsec, "<t>.<body>")，v1 为十六进制。
func stripeTestSignatureHeader(secret string, body []byte, ts time.Time) string {
	timestamp := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return fmt.Sprintf("t=%s,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

func stripeNotifyInput(body []byte) NotifyInput {
	return NotifyInput{
		RawBody: body,
		Header:  http.Header{"Stripe-Signature": {stripeTestSignatureHeader("whsec_test", body, time.Now())}},
	}
}

// webhook：签名合法 + checkout.session.completed + payment_status=paid 才算支付成功。
func TestStripeVerifyNotifyPaid(t *testing.T) {
	cfg := ProviderConfig{Method: "stripe", Secret: `{"secret_key":"sk_test_x","webhook_secret":"whsec_test"}`}
	g := StripeGateway{}

	body := []byte(`{"type":"checkout.session.completed","data":{"object":{"id":"cs_test_1","client_reference_id":"INV9","payment_intent":"pi_123","payment_status":"paid","amount_total":8888,"currency":"cny"}}}`)
	res := g.VerifyNotify(cfg, stripeNotifyInput(body))
	if !res.OK || res.TradeNo != "pi_123" || res.AmountCents != 8888 {
		t.Fatalf("notify result = %+v", res)
	}
}

// payment_status=unpaid 的 completed 事件不代表已收款，必须拒绝（参考插件语义）。
func TestStripeVerifyNotifyUnpaid(t *testing.T) {
	cfg := ProviderConfig{Method: "stripe", Secret: `{"secret_key":"sk_test_x","webhook_secret":"whsec_test"}`}
	g := StripeGateway{}

	body := []byte(`{"type":"checkout.session.completed","data":{"object":{"id":"cs_test_2","client_reference_id":"INV9","payment_intent":"pi_123","payment_status":"unpaid","amount_total":8888}}}`)
	if res := g.VerifyNotify(cfg, stripeNotifyInput(body)); res.OK || res.Err == nil {
		t.Fatal("unpaid checkout session must not verify as paid")
	}
}

// 错密钥签名、时间戳超窗、其它事件类型、缺 client_reference_id、缺头、未配置 webhook_secret 全部拒绝。
func TestStripeVerifyNotifyRejects(t *testing.T) {
	cfg := ProviderConfig{Method: "stripe", Secret: `{"secret_key":"sk_test_x","webhook_secret":"whsec_test"}`}
	g := StripeGateway{}

	body := []byte(`{"type":"checkout.session.completed","data":{"object":{"id":"cs_test_1","client_reference_id":"INV9","payment_intent":"pi_123","payment_status":"paid","amount_total":8888}}}`)

	// 用别的密钥签出来的头。
	badSig := NotifyInput{RawBody: body, Header: http.Header{"Stripe-Signature": {stripeTestSignatureHeader("whsec_other", body, time.Now())}}}
	if res := g.VerifyNotify(cfg, badSig); res.OK || res.Err == nil {
		t.Fatal("signature from another secret must fail")
	}

	// 时间戳超出一小时。
	stale := NotifyInput{RawBody: body, Header: http.Header{"Stripe-Signature": {stripeTestSignatureHeader("whsec_test", body, time.Now().Add(-time.Hour))}}}
	if res := g.VerifyNotify(cfg, stale); res.OK || res.Err == nil {
		t.Fatal("stale timestamp must fail")
	}

	// 非 checkout.session.completed 事件。
	other := []byte(`{"type":"payment_intent.succeeded","data":{"object":{"payment_status":"paid","amount_total":8888}}}`)
	if res := g.VerifyNotify(cfg, stripeNotifyInput(other)); res.OK || res.Err == nil {
		t.Fatal("non-checkout events must be ignored")
	}

	// session 缺 client_reference_id。
	noRef := []byte(`{"type":"checkout.session.completed","data":{"object":{"id":"cs_test_3","payment_status":"paid","amount_total":100}}}`)
	if res := g.VerifyNotify(cfg, stripeNotifyInput(noRef)); res.OK || res.Err == nil {
		t.Fatal("session without client_reference_id must fail")
	}

	// 缺签名头与未配置 webhook_secret。
	if res := g.VerifyNotify(cfg, NotifyInput{RawBody: body}); res.OK || res.Err == nil {
		t.Fatal("missing Stripe-Signature header must fail")
	}
	plain := ProviderConfig{Method: "stripe", Secret: "sk_test_x"}
	if res := g.VerifyNotify(plain, stripeNotifyInput(body)); res.OK || res.Err == nil {
		t.Fatal("plain sk_ key without webhook_secret must fail")
	}
}
