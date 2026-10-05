package alipaykit

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/url"
	"testing"
)

// 待签名串规则：按名排序、排除 sign/sign_type 与空值、不 URL 编码。
func TestSignContentRules(t *testing.T) {
	params := url.Values{
		"z_last":    {"1"},
		"a_first":   {"2"},
		"sign":      {"ignored"},
		"sign_type": {"RSA2"},
		"empty":     {""},
		"unencoded": {"a b&c=d"},
		"chars":     {"+/~="},
	}
	want := "a_first=2&chars=+/~=&unencoded=a b&c=d&z_last=1"
	if got := SignContent(params); got != want {
		t.Fatalf("SignContent =\n %q\nwant\n %q", got, want)
	}
}

// 签名/验签必须闭环；篡改一个字节必须验签失败。
func TestSignVerifyRoundtrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	content := "app_id=1&method=alipay.system.oauth.token"
	sign, err := SignRSA2(key, content)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// 解开 base64 确认是 PKCS1v15 结构（长度 = 256 字节）。
	raw, err := base64.StdEncoding.DecodeString(sign)
	if err != nil || len(raw) != 256 {
		t.Fatalf("signature malformed: len=%d err=%v", len(raw), err)
	}
	if !VerifyRSA2(&key.PublicKey, content, sign) {
		t.Fatal("verify failed on correct signature")
	}
	if VerifyRSA2(&key.PublicKey, content+"x", sign) {
		t.Fatal("verify passed on tampered content")
	}
}

// 密钥解析：PKCS8 PEM 与纯 base64 两种形态都要支持（支付宝开放平台的
// 密钥工具两种都给）。
func TestParsePrivateKeyForms(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemForm := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	bare := base64.StdEncoding.EncodeToString(der)

	parsedPEM, err := ParsePrivateKey(string(pemForm))
	if err != nil {
		t.Fatalf("pem form: %v", err)
	}
	parsedBare, err := ParsePrivateKey(bare)
	if err != nil {
		t.Fatalf("bare base64 form: %v", err)
	}
	if parsedPEM.N.Cmp(parsedBare.N) != 0 {
		t.Fatal("two forms parsed to different keys")
	}
	if _, err := ParsePrivateKey("not a key"); err == nil {
		t.Fatal("garbage must be rejected")
	}
}
