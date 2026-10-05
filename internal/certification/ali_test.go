package certification

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/alipaykit"
)

// 生成一对 RSA 密钥，返回 PKCS8 私钥 PEM 与 PKIX 公钥 PEM（支付宝开放平台密钥工具的形态之一）。
func aliTestKeyPEM(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key,
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
}

// 假支付宝网关：响应节点原文用 RSA2 签名，与真实网关的验签规则一致。
func aliTestGateway(t *testing.T, key *rsa.PrivateKey, nodes map[string]string) (*httptest.Server, *[]url.Values) {
	t.Helper()
	forms := &[]url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*forms = append(*forms, r.PostForm)
		method := r.PostForm.Get("method")
		node, ok := nodes[method]
		if !ok {
			t.Errorf("unexpected method %q", method)
			return
		}
		sign, err := alipaykit.SignRSA2(key, node)
		if err != nil {
			t.Errorf("sign: %v", err)
			return
		}
		_, _ = w.Write([]byte(`{"` + strings.ReplaceAll(method, ".", "_") + `_response":` + node + `,"sign":"` + sign + `"}`))
	}))
	return srv, forms
}

func TestAliChallengeAndQuery(t *testing.T) {
	key, priv, pub := aliTestKeyPEM(t)
	srv, forms := aliTestGateway(t, key, map[string]string{
		"alipay.user.certify.open.initialize": `{"code":"10000","msg":"Success","certify_id":"cid-123"}`,
		"alipay.user.certify.open.query":      `{"code":"10000","msg":"Success","passed":"T"}`,
	})
	defer srv.Close()
	a := NewAli()
	a.Endpoint = srv.URL
	cfg := Config{Fields: map[string]string{"app_id": "20210001"}}
	secret := Secret{"private_key": priv, "alipay_public_key": pub}

	ch, err := a.Challenge(context.Background(), cfg, secret, Subject{RealName: "张三", IDNumber: "110101199003077574"})
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if ch.Token != "cid-123" {
		t.Fatalf("token = %q", ch.Token)
	}
	if !strings.HasPrefix(ch.URL, srv.URL+"?") || !strings.Contains(ch.URL, "certify_id") || !strings.Contains(ch.URL, "sign=") {
		t.Fatalf("challenge url = %q", ch.URL)
	}
	if got := (*forms)[0].Get("biz_content"); !strings.Contains(got, "SMART_FACE") || !strings.Contains(got, "cert_name") {
		t.Fatalf("initialize biz_content = %q", got)
	}
	res, err := a.Query(context.Background(), cfg, secret, ch.Token)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !res.Match || res.Pending {
		t.Fatalf("query result = %+v", res)
	}
}
func TestAliQueryPendingAndBadSignature(t *testing.T) {
	key, priv, pub := aliTestKeyPEM(t)
	srv, _ := aliTestGateway(t, key, map[string]string{
		"alipay.user.certify.open.query": `{"code":"10000","msg":"Success","passed":""}`,
	})
	defer srv.Close()
	a := NewAli()
	a.Endpoint = srv.URL
	cfg := Config{Fields: map[string]string{"app_id": "1"}}
	secret := Secret{"private_key": priv, "alipay_public_key": pub}
	res, err := a.Query(context.Background(), cfg, secret, "cid-1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !res.Pending || res.Match {
		t.Fatalf("empty passed must be pending, got %+v", res)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"alipay_user_certify_open_query_response":{"code":"10000","passed":"T"},"sign":"AAAA"}`))
	}))
	defer bad.Close()
	a.Endpoint = bad.URL
	if _, err := a.Query(context.Background(), cfg, secret, "cid-1"); err == nil || !strings.Contains(err.Error(), "验签") {
		t.Fatalf("bad signature must be rejected, got %v", err)
	}
}

func TestAliValidateAndRegistered(t *testing.T) {
	a := NewAli()
	if err := a.Validate(Config{Fields: map[string]string{"app_id": "x"}}, Secret{}); err == nil {
		t.Fatal("missing private_key must be rejected")
	}
	if err := a.Validate(Config{Fields: map[string]string{"app_id": "x"}}, Secret{"private_key": "k"}); err == nil {
		t.Fatal("missing alipay_public_key must be rejected")
	}
	if _, err := a.Verify(context.Background(), Config{}, Secret{}, Subject{}); err != ErrChallengeRequired {
		t.Fatalf("Verify err = %v", err)
	}
	if _, ok := Get("ali"); !ok {
		t.Fatal("ali must be registered")
	}
}
