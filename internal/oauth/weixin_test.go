package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 微信全流程：authorize(qrconnect) → access_token(带 unionid) → userinfo。
func TestWeChatExchangeFullFlow(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/sns/oauth2/access_token":
			if r.URL.Query().Get("appid") != "wxid" || r.URL.Query().Get("secret") != "wsec" || r.URL.Query().Get("code") != "c1" {
				writeStr(w, `{"errcode":40029,"errmsg":"invalid code"}`)
				return
			}
			writeStr(w, `{"access_token":"WTOKEN","expires_in":7200,"openid":"WOID","unionid":"WUNION"}`)
		case "/sns/userinfo":
			if r.URL.Query().Get("openid") != "WOID" {
				writeStr(w, `{"errcode":40003,"errmsg":"invalid openid"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			writeStr(w, `{"openid":"WOID","unionid":"WUNION","nickname":"王小明","headimgurl":"http://wx.qlogo.cn/132"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	w := NewWeChat()
	w.TokenEndpoint = srv.URL + "/sns/oauth2/access_token"
	w.UserInfoEndpoint = srv.URL + "/sns/userinfo"

	identity, err := w.Exchange(context.Background(),
		Config{Fields: map[string]string{"client_id": "wxid"}},
		Secret{"client_secret": "wsec"}, "c1", "cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if identity.Subject != "WOID" {
		t.Fatalf("subject = %q", identity.Subject)
	}
	// unionid 是微信独有的跨应用标识，必须透传到 Identity。
	if identity.UnionID != "WUNION" {
		t.Fatalf("unionid = %q, want WUNION", identity.UnionID)
	}
	if identity.Nickname != "王小明" || identity.AvatarURL != "http://wx.qlogo.cn/132" {
		t.Fatalf("profile fields wrong: %+v", identity)
	}
	if identity.Email != "" {
		t.Fatalf("微信不返回邮箱，Email 必须为空")
	}
	_ = gotUA
}

// 微信错误响应 HTTP 状态仍是 200，errcode 藏在 JSON 里——必须显式检查。
func TestWeChatErrcodeSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		writeStr(w, `{"errcode":40163,"errmsg":"code been used, hints: [req_id]"}`)
	}))
	defer srv.Close()

	w := NewWeChat()
	w.TokenEndpoint = srv.URL + "/sns/oauth2/access_token"
	w.UserInfoEndpoint = srv.URL + "/sns/userinfo"

	_, err := w.Exchange(context.Background(),
		Config{Fields: map[string]string{"client_id": "wxid"}},
		Secret{"client_secret": "wsec"}, "used", "cb")
	if err == nil || !strings.Contains(err.Error(), "code been used") {
		t.Fatalf("want errcode surfaced, got %v", err)
	}
}

// redirect_uri 必须被 urlencode（微信文档要求），scope 默认 snsapi_login。
func TestWeChatAuthorizeURL(t *testing.T) {
	w := NewWeChat()
	u, err := w.AuthorizeURL(Config{Fields: map[string]string{"client_id": "wxid"}}, Secret{},
		AuthorizeParams{RedirectURI: "https://s.example.com/api/v1/auth/oauth/weixin/callback", State: "st"})
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	parsed, _ := url.Parse(u)
	if parsed.Host != "open.weixin.qq.com" || parsed.Path != "/connect/qrconnect" {
		t.Fatalf("authorize endpoint wrong: %s", u)
	}
	q := parsed.Query()
	if q.Get("appid") != "wxid" || q.Get("scope") != "snsapi_login" || q.Get("state") != "st" {
		t.Fatalf("query wrong: %v", q)
	}
	// ParseQuery 已经解过码：能原样还原说明编码正确。
	if q.Get("redirect_uri") != "https://s.example.com/api/v1/auth/oauth/weixin/callback" {
		t.Fatalf("redirect_uri lost: %q", q.Get("redirect_uri"))
	}
}

func TestWeChatRegistered(t *testing.T) {
	if _, ok := Get("weixin"); !ok {
		t.Fatal("weixin must be registered on package load")
	}
}
