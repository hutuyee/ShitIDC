package wechatpay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hutuyee/ShitIDC/internal/payment"
)

// 这些测试用真实的 RSA/AES 运算跑一遍握手：商户私钥签名 → 平台证书验签；
// APIv3 密钥加密回调 resource → 网关解密。签名串少一个换行、GCM 参数写错都会失败。
// 现实中商户私钥与微信支付平台证书是两套密钥，测试也照此区分。

func testKeyPair(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "wechatpay-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func privateKeyPEM(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func encryptResource(t *testing.T, apiV3Key, nonce, associatedData, plaintext string) string {
	t.Helper()
	block, err := aes.NewCipher([]byte(apiV3Key))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nil, []byte(nonce), []byte(plaintext), []byte(associatedData)))
}

// callbackPlaintext 用字符串拼接而不是模板，避免把变量名写进 JSON。
func callbackPlaintext(tradeState string, total int64) string {
	return `{"out_trade_no":"O123","transaction_id":"4200001","trade_state":"` + tradeState +
		`","amount":{"total":` + strconv.FormatInt(total, 10) + `}}`
}

// buildCallback 构造一个已签名的微信支付回调。
func buildCallback(t *testing.T, merchantKey *rsa.PrivateKey, platformCertPEM, apiV3Key, tradeState string, total int64) (payment.ProviderConfig, payment.NotifyInput) {
	t.Helper()
	const nonce = "nonce1234567"
	const aad = "transaction"
	ciphertext := encryptResource(t, apiV3Key, nonce, aad, callbackPlaintext(tradeState, total))
	envelope, err := json.Marshal(map[string]any{
		"event_type": "TRANSACTION.SUCCESS",
		"resource": map[string]any{
			"algorithm":       "AEAD_AES_256_GCM",
			"ciphertext":      ciphertext,
			"nonce":           nonce,
			"associated_data": aad,
		},
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sig, err := signCallback(merchantKey, timestamp, "cbnonce", string(envelope))
	if err != nil {
		t.Fatalf("sign callback: %v", err)
	}
	secret, err := json.Marshal(Config{
		AppID: "wxapp", MchID: "1900000001", SerialNo: "SERIAL1",
		PrivateKeyPEM: privateKeyPEM(t, merchantKey), APIv3Key: apiV3Key, PlatformCert: platformCertPEM,
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	header := http.Header{}
	header.Set("Wechatpay-Timestamp", timestamp)
	header.Set("Wechatpay-Nonce", "cbnonce")
	header.Set("Wechatpay-Signature", sig)
	return payment.ProviderConfig{Method: methodName, Secret: string(secret)},
		payment.NotifyInput{RawBody: envelope, Header: header}
}

// 验签用的是“签名方的公钥”。真实场景里签名方是微信支付，其公钥就在平台证书里，
// 所以测试用同一个密钥对生成证书与签名；跨密钥的拒绝场景单独在下面覆盖。
func TestSignAndVerifyRoundTrip(t *testing.T) {
	key, certPEM := testKeyPair(t)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	const nonce = "abc123"
	body := `{"out_trade_no":"O1"}`
	sig, err := signCallback(key, timestamp, nonce, body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := verifyWithPlatformCert(certPEM, timestamp, nonce, body, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := verifyWithPlatformCert(certPEM, timestamp, nonce, `{"out_trade_no":"O2"}`, sig); err == nil {
		t.Fatal("tampered body was accepted")
	}
	if err := verifyWithPlatformCert(certPEM, "1", nonce, body, sig); err == nil {
		t.Fatal("tampered timestamp was accepted")
	}
	if err := verifyWithPlatformCert(certPEM, timestamp, "other", body, sig); err == nil {
		t.Fatal("tampered nonce was accepted")
	}
	// 换一套密钥签的签名不能被这张证书验证通过（防止用任意自签证书冒充）。
	otherKey, otherCert := testKeyPair(t)
	otherSig, _ := signCallback(otherKey, timestamp, nonce, body)
	if err := verifyWithPlatformCert(certPEM, timestamp, nonce, body, otherSig); err == nil {
		t.Fatal("a signature from an unrelated key was accepted")
	}
	if err := verifyWithPlatformCert(otherCert, timestamp, nonce, body, sig); err == nil {
		t.Fatal("a signature was accepted under an unrelated certificate")
	}
}

func TestVerifyNotifyDecryptsAndChecksTradeState(t *testing.T) {
	// 一处密钥对同时扮演“签名方（微信支付）”与“商户私钥”，
	// 这样平台证书正好对应签名私钥。
	key, certPEM := testKeyPair(t)
	const apiV3Key = "0123456789abcdef0123456789abcdef"
	cfg, input := buildCallback(t, key, certPEM, apiV3Key, "SUCCESS", 1000)
	res := New().VerifyNotify(cfg, input)
	if !res.OK {
		t.Fatalf("verified callback rejected: %v", res.Err)
	}
	if res.AmountCents != 1000 {
		t.Fatalf("amount = %d, want 1000", res.AmountCents)
	}
	if res.TradeNo != "4200001" {
		t.Fatalf("trade no = %q, want 4200001", res.TradeNo)
	}
}

func TestVerifyNotifyRejectsBadTradeState(t *testing.T) {
	key, certPEM := testKeyPair(t)
	const apiV3Key = "0123456789abcdef0123456789abcdef"
	cfg, input := buildCallback(t, key, certPEM, apiV3Key, "NOTPAY", 100)
	if res := New().VerifyNotify(cfg, input); res.OK {
		t.Fatal("a NOTPAY callback was treated as paid")
	}
}

func TestVerifyNotifyRejectsMissingHeaders(t *testing.T) {
	key, certPEM := testKeyPair(t)
	cfg, _ := buildCallback(t, key, certPEM, "0123456789abcdef0123456789abcdef", "SUCCESS", 1)
	if res := New().VerifyNotify(cfg, payment.NotifyInput{RawBody: []byte("{}"), Header: http.Header{}}); res.OK {
		t.Fatal("callback without signature headers was accepted")
	}
}

func TestVerifyNotifyRejectsStaleTimestamp(t *testing.T) {
	key, certPEM := testKeyPair(t)
	const apiV3Key = "0123456789abcdef0123456789abcdef"
	cfg, input := buildCallback(t, key, certPEM, apiV3Key, "SUCCESS", 1)
	old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	sig, _ := signCallback(key, old, "cbnonce", string(input.RawBody))
	header := http.Header{}
	header.Set("Wechatpay-Timestamp", old)
	header.Set("Wechatpay-Nonce", "cbnonce")
	header.Set("Wechatpay-Signature", sig)
	if res := New().VerifyNotify(cfg, payment.NotifyInput{RawBody: input.RawBody, Header: header}); res.OK {
		t.Fatal("stale callback was accepted (replay protection missing)")
	}
}

func TestVerifyNotifyRejectsWrongAPIv3Key(t *testing.T) {
	key, certPEM := testKeyPair(t)
	const apiV3Key = "0123456789abcdef0123456789abcdef"
	_, input := buildCallback(t, key, certPEM, apiV3Key, "SUCCESS", 1)
	wrongSecret, _ := json.Marshal(Config{
		AppID: "wxapp", MchID: "1900000001", SerialNo: "SERIAL1",
		PrivateKeyPEM: privateKeyPEM(t, key), APIv3Key: "ffffffffffffffffffffffffffffffff", PlatformCert: certPEM,
	})
	res := New().VerifyNotify(payment.ProviderConfig{Method: methodName, Secret: string(wrongSecret)}, input)
	if res.OK {
		t.Fatal("callback decrypted with the wrong APIv3 key")
	}
}

func TestParseConfigRequiresFields(t *testing.T) {
	if _, err := parseConfig(payment.ProviderConfig{Secret: ""}); err == nil {
		t.Fatal("empty secret must be rejected")
	}
	if _, err := parseConfig(payment.ProviderConfig{Secret: "not-json"}); err == nil {
		t.Fatal("non-JSON secret must be rejected")
	}
	cfg, err := parseConfig(payment.ProviderConfig{MerchantID: "1900000001", Secret: `{"app_id":"a","serial_no":"s","private_key_pem":"k"}`})
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.MchID != "1900000001" {
		t.Fatalf("mch id = %q, want it taken from the merchant column", cfg.MchID)
	}
}

func TestRefundNoIsIdempotentPerRefund(t *testing.T) {
	a := refundNo(payment.RefundRequest{RefundID: "11111111-2222-3333-4444-555555555555"})
	b := refundNo(payment.RefundRequest{RefundID: "11111111-2222-3333-4444-555555555555"})
	if a != b {
		t.Fatalf("same refund id produced different refund numbers: %s vs %s", a, b)
	}
	c := refundNo(payment.RefundRequest{RefundID: "99999999-2222-3333-4444-555555555555"})
	if a == c {
		t.Fatal("different refund ids must produce different refund numbers")
	}
	if len(a) > 64 {
		t.Fatalf("refund number too long: %d", len(a))
	}
}

func TestTruncateCountsRunes(t *testing.T) {
	if got := truncate("香港VPS标准型", 3); got != "香港V" {
		t.Fatalf("truncate = %q, want the first 3 runes", got)
	}
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate should not pad: %q", got)
	}
}
