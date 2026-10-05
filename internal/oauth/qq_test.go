package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// QQ 全流程：authorize → token(form) → me(JSON) → get_user_info。
// 测试服务器按 QQ 互联的真实响应形态返回（token 是 a=1&b=2 文本）。
func TestQQExchangeFullFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2.0/token":
			if r.URL.Query().Get("client_id") != "cid" || r.URL.Query().Get("code") != "thecode" {
				writeStr(w, "errorDescription=bad")
				return
			}
			w.Header().Set("Content-Type", "text/html")
			writeStr(w, "access_token=QTOKEN&expires_in=7776000&refresh_token=R")
		case "/oauth2.0/me":
			if r.URL.Query().Get("access_token") != "QTOKEN" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			writeStr(w, `{"client_id":"cid","openid":"OPENID123"}`)
		case "/user/get_user_info":
			if r.URL.Query().Get("openid") != "OPENID123" || r.URL.Query().Get("oauth_consumer_key") != "cid" {
				writeStr(w, `{"ret":100014,"msg":"openid不匹配"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			writeStr(w, `{"ret":0,"nickname":"隔壁老王","figureurl_qq_1":"http://q.qlogo.cn/1","figureurl":"http://q.qlogo.cn/0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q := NewQQ()
	q.TokenEndpoint = srv.URL + "/oauth2.0/token"
	q.MeEndpoint = srv.URL + "/oauth2.0/me"
	q.UserInfoEndpoint = srv.URL + "/user/get_user_info"

	cfg := Config{Provider: "qq", Fields: map[string]string{"client_id": "cid"}}
	identity, err := q.Exchange(context.Background(), cfg, Secret{"client_secret": "sec"}, "thecode", srv.URL+"/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if identity.Provider != "qq" || identity.Subject != "OPENID123" {
		t.Fatalf("identity = %+v", identity)
	}
	if identity.Nickname != "隔壁老王" || identity.AvatarURL != "http://q.qlogo.cn/1" {
		t.Fatalf("profile fields wrong: %+v", identity)
	}
	if identity.Email != "" {
		t.Fatalf("QQ 不提供邮箱，Email 必须为空，got %q", identity.Email)
	}
}

// 老 JSONP 形态兜底：me 端点对任何参数都回 callback(...)，Exchange 必须仍能解出 openid。
func TestQQExchangeJSONPFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2.0/token":
			writeStr(w, "access_token=QTOKEN&expires_in=7776000")
		case "/oauth2.0/me":
			w.Header().Set("Content-Type", "application/javascript")
			writeStr(w, `callback( {"client_id":"cid","openid":"JSONP_ID"} );`)
		case "/user/get_user_info":
			writeStr(w, `{"ret":0,"nickname":"n"}`)
		}
	}))
	defer srv.Close()

	q := NewQQ()
	q.TokenEndpoint = srv.URL + "/oauth2.0/token"
	q.MeEndpoint = srv.URL + "/oauth2.0/me"
	q.UserInfoEndpoint = srv.URL + "/user/get_user_info"

	identity, err := q.Exchange(context.Background(),
		Config{Fields: map[string]string{"client_id": "cid"}},
		Secret{"client_secret": "sec"}, "c", "cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if identity.Subject != "JSONP_ID" {
		t.Fatalf("subject = %q, want JSONP_ID (JSONP 兜底必须能解出 openid)", identity.Subject)
	}
}

// ret != 0 必须报错而不是当成空资料继续。
func TestQQExchangeRetError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2.0/token":
			writeStr(w, "access_token=QTOKEN")
		case "/oauth2.0/me":
			writeStr(w, `{"openid":"O1"}`)
		case "/user/get_user_info":
			writeStr(w, `{"ret":100016,"msg":"access_token expire"}`)
		}
	}))
	defer srv.Close()

	q := NewQQ()
	q.TokenEndpoint = srv.URL + "/oauth2.0/token"
	q.MeEndpoint = srv.URL + "/oauth2.0/me"
	q.UserInfoEndpoint = srv.URL + "/user/get_user_info"

	_, err := q.Exchange(context.Background(),
		Config{Fields: map[string]string{"client_id": "cid"}},
		Secret{"client_secret": "sec"}, "c", "cb")
	if err == nil || !strings.Contains(err.Error(), "access_token expire") {
		t.Fatalf("want ret!=0 surfaced, got %v", err)
	}
}

func TestQQValidateAndAuthorizeURL(t *testing.T) {
	q := NewQQ()
	if err := q.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing client_id must be rejected")
	}
	cfg := Config{Fields: map[string]string{"client_id": "cid"}}
	if err := q.Validate(cfg, Secret{}); err == nil {
		t.Fatal("missing client_secret must be rejected")
	}
	u, err := q.AuthorizeURL(cfg, Secret{}, AuthorizeParams{RedirectURI: "https://s.example.com/cb", State: "st"})
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	parsed, _ := url.Parse(u)
	if parsed.Host != "graph.qq.com" || parsed.Path != "/oauth2.0/authorize" {
		t.Fatalf("authorize endpoint wrong: %s", u)
	}
	if got := parsed.Query().Get("scope"); got != "snsapi_login" {
		t.Fatalf("scope = %q, want snsapi_login", got)
	}
	if got := parsed.Query().Get("response_type"); got != "code" {
		t.Fatalf("response_type = %q", got)
	}
}

func TestQQRegistered(t *testing.T) {
	if _, ok := Get("qq"); !ok {
		t.Fatal("qq must be registered on package load")
	}
}
