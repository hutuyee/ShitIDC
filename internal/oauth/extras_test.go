package oauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// 钉钉：授权地址带 client_id/state；Exchange 走 JSON token + 头部资料。
func TestDingtalkFlow(t *testing.T) {
	d := NewDingtalk()
	u, err := d.AuthorizeURL(Config{Fields: map[string]string{"client_id": "cid"}}, Secret{"client_secret": "cs"}, AuthorizeParams{RedirectURI: "https://x/cb", State: "st"})
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	parsed, _ := url.Parse(u)
	if parsed.Query().Get("client_id") != "cid" || parsed.Query().Get("state") != "st" || parsed.Query().Get("redirect_uri") != "https://x/cb" {
		t.Fatalf("unexpected authorize URL: %s", u)
	}

	var tokenReq map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			raw := readAll(t, r.Body)
			if err := json.Unmarshal([]byte(raw), &tokenReq); err != nil {
				t.Fatalf("token payload not json: %v", err)
			}
			_, _ = w.Write([]byte(`{"accessToken":"at","expireIn":7200}`))
		case "/me":
			if r.Header.Get("x-acs-dingtalk-access-token") != "at" {
				t.Fatalf("missing/incorrect dingtalk token header")
			}
			_, _ = w.Write([]byte(`{"openId":"open-1","unionId":"union-1","nick":"钉钉用户","email":"User@Example.com","avatarUrl":"https://a/b.png"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	d.TokenEndpoint = srv.URL + "/token"
	d.UserInfoEndpoint = srv.URL + "/me"
	id, err := d.Exchange(context.Background(), Config{Fields: map[string]string{"client_id": "cid"}}, Secret{"client_secret": "cs"}, "code1", "")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tokenReq["clientId"] != "cid" || tokenReq["clientSecret"] != "cs" || tokenReq["grantType"] != "authorization_code" {
		t.Fatalf("unexpected token payload: %#v", tokenReq)
	}
	if id.Provider != "dingtalk" || id.Subject != "open-1" || id.UnionID != "union-1" || id.Nickname != "钉钉用户" || id.Email != "user@example.com" {
		t.Fatalf("unexpected identity: %#v", id)
	}
}

// Google：授权参数与参考插件一致；Exchange 换 token 后按 id 取主键。
func TestGoogleFlow(t *testing.T) {
	g := NewGoogle()
	u, err := g.AuthorizeURL(Config{Fields: map[string]string{"client_id": "cid"}}, Secret{}, AuthorizeParams{RedirectURI: "https://x/cb", State: "st"})
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	q, _ := url.ParseQuery(strings.SplitN(u, "?", 2)[1])
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("scope") != "profile email" {
		t.Fatalf("unexpected google authorize params: %v", q)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			form, _ := url.ParseQuery(readAll(t, r.Body))
			if form.Get("code") != "code1" || form.Get("grant_type") != "authorization_code" {
				t.Fatalf("unexpected token form: %v", form)
			}
			_, _ = w.Write([]byte(`{"access_token":"at"}`))
		case "/me":
			if r.Header.Get("Authorization") != "Bearer at" {
				t.Fatal("missing bearer token")
			}
			_, _ = w.Write([]byte(`{"id":"g-1","name":"G 用户","email":"g@example.com","picture":"https://a/g.png"}`))
		}
	}))
	t.Cleanup(srv.Close)
	g.TokenEndpoint = srv.URL + "/token"
	g.UserInfoEndpoint = srv.URL + "/me"
	id, err := g.Exchange(context.Background(), Config{Fields: map[string]string{"client_id": "cid"}}, Secret{"client_secret": "cs"}, "code1", "https://x/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if id.Subject != "g-1" || id.Email != "g@example.com" || id.AvatarURL != "https://a/g.png" {
		t.Fatalf("unexpected identity: %#v", id)
	}
}

// 企业微信：ServiceApp 授权 + suite_ticket 两步换身份。
func TestQyweixinFlow(t *testing.T) {
	q := NewQyweixin()
	u, err := q.AuthorizeURL(Config{Fields: map[string]string{"suite_id": "suite1"}}, Secret{}, AuthorizeParams{RedirectURI: "https://x/cb", State: "st"})
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	if !strings.HasPrefix(u, q.AuthorizeEndpoint) || !strings.Contains(u, "login_type=ServiceApp") || !strings.Contains(u, "appid=suite1") || !strings.HasSuffix(u, "#wechat_redirect") {
		t.Fatalf("unexpected authorize URL: %s", u)
	}

	var suiteForm map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/suite_token" {
			raw := readAll(t, r.Body)
			if err := json.Unmarshal([]byte(raw), &suiteForm); err != nil {
				t.Fatalf("suite payload not json: %v", err)
			}
			_, _ = w.Write([]byte(`{"suite_access_token":"sat","expires_in":7200}`))
			return
		}
		if got := r.URL.Query().Get("suite_access_token"); got != "sat" {
			t.Fatalf("unexpected suite_access_token: %q", got)
		}
		if r.URL.Query().Get("code") != "code1" {
			t.Fatalf("unexpected code: %q", r.URL.Query().Get("code"))
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","openid":"qy-open-1"}`))
	}))
	t.Cleanup(srv.Close)
	q.SuiteTokenEndpoint = srv.URL + "/suite_token"
	q.UserInfoEndpoint = srv.URL + "/getuserinfo3rd"
	secret := Secret{"secret": "ss", "token": "tk", "aes_key": testAESKey}
	cfg := Config{Fields: map[string]string{"suite_id": "suite1"}}
	id, err := q.ExchangeWithSuiteTicket(context.Background(), cfg, secret, "code1", "https://x/cb", "ticket-1")
	if err != nil {
		t.Fatalf("ExchangeWithSuiteTicket: %v", err)
	}
	if suiteForm["suite_id"] != "suite1" || suiteForm["suite_secret"] != "ss" || suiteForm["suite_ticket"] != "ticket-1" {
		t.Fatalf("unexpected suite token payload: %#v", suiteForm)
	}
	if id.Provider != "qyweixin" || id.Subject != "qy-open-1" {
		t.Fatalf("unexpected identity: %#v", id)
	}
	// 没有 ticket 时 Exchange 必须明确报错（提示走回调）。
	if _, err := q.Exchange(context.Background(), cfg, secret, "c", ""); err == nil {
		t.Fatal("Exchange without suite_ticket should fail")
	}
}

// 企业微信回调加解密：与官方口径的往返必须成立；签名不对必须拒绝。
func TestQyweixinCryptRoundTrip(t *testing.T) {
	const token = "callback-token"
	msg := `<xml><SuiteTicket><![CDATA[ticket-abc]]></SuiteTicket></xml>`
	encrypt, sig, ts, nonce := encryptFixture(t, token, msg, "corp1")
	plain, err := QyweixinVerifyURL(token, testAESKey, sig, ts, nonce, encrypt)
	if err != nil {
		t.Fatalf("VerifyURL: %v", err)
	}
	if plain != msg {
		t.Fatalf("plain mismatch: %q", plain)
	}
	body := `<xml><Encrypt><![CDATA[` + encrypt + `]]></Encrypt></xml>`
	decrypted, err := QyweixinDecryptMessage(token, testAESKey, sig, ts, nonce, body)
	if err != nil {
		t.Fatalf("DecryptMessage: %v", err)
	}
	if got := QyweixinParseSuiteTicket(decrypted); got != "ticket-abc" {
		t.Fatalf("SuiteTicket = %q", got)
	}
	// 篡改签名必须拒绝
	if _, err := QyweixinVerifyURL(token, testAESKey, strings.Repeat("0", 40), ts, nonce, encrypt); err == nil {
		t.Fatal("bad signature accepted")
	}
	// 换一个 token 也必须拒绝
	if _, err := QyweixinVerifyURL("other-token", testAESKey, sig, ts, nonce, encrypt); err == nil {
		t.Fatal("signature verified with wrong token")
	}
}

// testAESKey 是 43 位合法 EncodingAESKey（base64 补 = 后 32 字节）。
const testAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

// encryptFixture 按微信口径构造一条密文（仅测试用）。
func encryptFixture(t *testing.T, token, msg, receiveID string) (encrypt, sig, timestamp, nonce string) {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(testAESKey + "=")
	if err != nil || len(key) != 32 {
		t.Fatalf("bad test AES key")
	}
	payload := make([]byte, 16)
	for i := range payload {
		payload[i] = byte(i)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(msg)))
	payload = append(payload, lenBuf[:]...)
	payload = append(payload, []byte(msg)...)
	payload = append(payload, []byte(receiveID)...)
	pad := 32 - len(payload)%32
	if pad == 0 {
		pad = 32
	}
	for i := 0; i < pad; i++ {
		payload = append(payload, byte(pad))
	}
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(payload))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(out, payload)
	encrypt = base64.StdEncoding.EncodeToString(out)
	timestamp, nonce = "1700000000", "nonce"
	sig = qyweixinSignature(token, timestamp, nonce, encrypt)
	return
}
