package oauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/alipaykit"
)

// 生成一对测试密钥：私钥给通道签名，公钥给假网关验签。
// 签名验证用真实收到的表单重算待签名串——验证的不是「实现签名了没有」，
// 而是「签的是发出去的那份字节」。
func alipayTestKeys(t *testing.T) (string, *rsa.PublicKey) {
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
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		t.Fatal("fixture public key is not RSA")
	}
	return string(privPEM), pub
}

// 支付宝全流程：auth_code → alipay.system.oauth.token → alipay.user.info.share。
func TestAlipayExchangeFullFlow(t *testing.T) {
	privPEM, pub := alipayTestKeys(t)

	var sawAuthCode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			http.Error(w, "bad", 400)
			return
		}
		form := r.PostForm
		// 先验签：对收到的表单按网关规范重算待签名串。
		sign := form.Get("sign")
		if !alipaykit.VerifyRSA2(pub, alipaykit.SignContent(form), sign) {
			t.Errorf("request signature invalid: form=%v", form)
			http.Error(w, "bad sign", 400)
			return
		}
		switch form.Get("method") {
		case "alipay.system.oauth.token":
			var biz struct {
				GrantType string `json:"grant_type"`
				Code      string `json:"code"`
			}
			if err := json.Unmarshal([]byte(form.Get("biz_content")), &biz); err != nil {
				t.Errorf("biz_content: %v", err)
			}
			if biz.GrantType != "authorization_code" || biz.Code == "" {
				t.Errorf("oauth token biz wrong: %s", form.Get("biz_content"))
			}
			sawAuthCode = biz.Code
			writeStr(w, `{"alipay_system_oauth_token_response":{"access_token":"ATOKEN","expires_in":31536000,"user_id":"2088102145486208"}}`)
		case "alipay.user.info.share":
			var biz struct {
				AuthToken string `json:"auth_token"`
			}
			_ = json.Unmarshal([]byte(form.Get("biz_content")), &biz)
			if biz.AuthToken != "ATOKEN" {
				t.Errorf("auth_token = %q, want ATOKEN", biz.AuthToken)
			}
			writeStr(w, `{"alipay_user_info_share_response":{"user_id":"2088102145486208","nick_name":"支付宝昵称","avatar":"http://tfsimg.alipay.com/avatar.jpg","gender":"M"}}`)
		default:
			http.Error(w, "unknown method", 400)
		}
	}))
	defer srv.Close()

	a := NewAlipay()
	a.GatewayEndpoint = srv.URL

	identity, err := a.Exchange(context.Background(),
		Config{Fields: map[string]string{"app_id": "2021000000000001"}},
		Secret{"app_private_key": privPEM}, "THE_AUTH_CODE", "cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if sawAuthCode != "THE_AUTH_CODE" {
		t.Fatalf("auth_code = %q, want THE_AUTH_CODE", sawAuthCode)
	}
	if identity.Subject != "2088102145486208" {
		t.Fatalf("subject = %q, want user_id", identity.Subject)
	}
	if identity.Nickname != "支付宝昵称" || identity.AvatarURL != "http://tfsimg.alipay.com/avatar.jpg" {
		t.Fatalf("profile wrong: %+v", identity)
	}
}

// error_response 必须把 sub_msg 带出来，而不是吞成一句「失败」。
func TestAlipayErrorResponseSurfaces(t *testing.T) {
	privPEM, pub := alipayTestKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = pub // 签名在错误场景下也应当有效，但这里不再验签
		writeStr(w, `{"error_response":{"code":"40002","msg":"权限不足","sub_code":"isv.missing-auth","sub_msg":"无isv.missing-auth权限"}}`)
	}))
	defer srv.Close()

	a := NewAlipay()
	a.GatewayEndpoint = srv.URL
	_, err := a.Exchange(context.Background(),
		Config{Fields: map[string]string{"app_id": "app"}},
		Secret{"app_private_key": privPEM}, "c", "cb")
	if err == nil || !strings.Contains(err.Error(), "无isv.missing-auth权限") {
		t.Fatalf("want sub_msg surfaced, got %v", err)
	}
}

// 响应缺少期望节点（比如网关被劫持返回别的 method 的响应）必须报错。
func TestAlipayMissingResponseNode(t *testing.T) {
	privPEM, _ := alipayTestKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"alipay_user_info_share_response":{}}`)
	}))
	defer srv.Close()

	a := NewAlipay()
	a.GatewayEndpoint = srv.URL
	_, err := a.Exchange(context.Background(),
		Config{Fields: map[string]string{"app_id": "app"}},
		Secret{"app_private_key": privPEM}, "c", "cb")
	if err == nil || !strings.Contains(err.Error(), "alipay_system_oauth_token_response") {
		t.Fatalf("want missing-node error, got %v", err)
	}
}

// AuthorizeURL 与 Validate。
func TestAlipayValidateAndAuthorizeURL(t *testing.T) {
	a := NewAlipay()
	if err := a.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing app_id must be rejected")
	}
	cfg := Config{Fields: map[string]string{"app_id": "app"}}
	if err := a.Validate(cfg, Secret{}); err == nil {
		t.Fatal("missing app_private_key must be rejected")
	}
	u, err := a.AuthorizeURL(cfg, Secret{}, AuthorizeParams{RedirectURI: "https://s.example.com/cb", State: "st"})
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	parsed, _ := url.Parse(u)
	if parsed.Host != "openauth.alipay.com" || parsed.Path != "/oauth2/publicAppAuthorize.htm" {
		t.Fatalf("authorize endpoint wrong: %s", u)
	}
	if parsed.Query().Get("scope") != "auth_user" || parsed.Query().Get("app_id") != "app" {
		t.Fatalf("query wrong: %v", parsed.Query())
	}
}

func TestAlipayRegistered(t *testing.T) {
	if _, ok := Get("alipay"); !ok {
		t.Fatal("alipay must be registered on package load")
	}
}

// 保证测试自身用到的 base64/pem 组合是真实可解析的（防止夹具悄悄坏掉）。
func TestAlipayTestKeysAreParseable(t *testing.T) {
	privPEM, pub := alipayTestKeys(t)
	if _, err := alipaykit.ParsePrivateKey(privPEM); err != nil {
		t.Fatalf("fixture private key not parseable: %v", err)
	}
	if pub.N == nil || pub.N.BitLen() != 2048 {
		t.Fatal("fixture public key malformed")
	}
}
