package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 微博全流程：POST access_token → users/show.json。
// 关键回归点：subject 必须是响应里的 uid。魔方参考实现把 access_token 当
// openid 回传，token 一换代 subject 就漂移，绑定关系随之失效——这里钉死正确行为。
func TestWeiboExchangeUsesUIDAsSubject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/access_token":
			// 微博要求 POST form。
			if r.Method != http.MethodPost {
				t.Errorf("token endpoint must be POST, got %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.PostFormValue("client_id") != "wbid" || r.PostFormValue("code") != "c1" {
				writeStr(w, `{"error":"invalid_grant","error_code":21325,"error_description":"invalid authorization code"}`)
				return
			}
			writeStr(w, `{"access_token":"WBTOKEN","expires_in":157679997,"uid":"1985740951","remind_in":"157679999"}`)
		case "/2/users/show.json":
			if r.URL.Query().Get("uid") != "1985740951" {
				writeStr(w, `{"error":"User does not exists!","error_code":20013}`)
				return
			}
			writeStr(w, `{"id":1985740951,"screen_name":"微博昵称","profile_image_url":"http://tp1.sinaimg.cn/50x50.jpg","avatar_large":"http://tp1.sinaimg.cn/180x180.jpg","avatar_hd":"http://tp1.sinaimg.cn/hd.jpg"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	wb := NewWeibo()
	wb.TokenEndpoint = srv.URL + "/oauth2/access_token"
	wb.UserInfoEndpoint = srv.URL + "/2/users/show.json"

	identity, err := wb.Exchange(context.Background(),
		Config{Fields: map[string]string{"client_id": "wbid"}},
		Secret{"client_secret": "wbsec"}, "c1", "cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if identity.Subject != "1985740951" {
		t.Fatalf("subject = %q, want uid 1985740951（access_token 不能当 subject）", identity.Subject)
	}
	if identity.Subject == "WBTOKEN" {
		t.Fatal("subject 是 access_token——魔方参考实现的错误被复刻了")
	}
	if identity.Nickname != "微博昵称" {
		t.Fatalf("nickname = %q", identity.Nickname)
	}
	// 头像取最高清的 avatar_hd。
	if identity.AvatarURL != "http://tp1.sinaimg.cn/hd.jpg" {
		t.Fatalf("avatar = %q, want avatar_hd", identity.AvatarURL)
	}
}

func TestWeiboErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		writeStr(w, `{"request":"/oauth2/access_token","error_uri":"/oauth2/access_token","error":"invalid_grant","error_description":"code 已经使用"}`)
	}))
	defer srv.Close()

	wb := NewWeibo()
	wb.TokenEndpoint = srv.URL + "/oauth2/access_token"
	wb.UserInfoEndpoint = srv.URL + "/2/users/show.json"

	_, err := wb.Exchange(context.Background(),
		Config{Fields: map[string]string{"client_id": "wbid"}},
		Secret{"client_secret": "wbsec"}, "used", "cb")
	if err == nil || !strings.Contains(err.Error(), "code 已经使用") {
		t.Fatalf("want error_description surfaced, got %v", err)
	}
}

func TestWeiboValidateAndAuthorizeURL(t *testing.T) {
	wb := NewWeibo()
	if err := wb.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing client_id must be rejected")
	}
	cfg := Config{Fields: map[string]string{"client_id": "wbid"}}
	if err := wb.Validate(cfg, Secret{}); err == nil {
		t.Fatal("missing client_secret must be rejected")
	}
	// scope 不配置时不拼 scope 参数（微博用应用登记的默认授权范围）。
	u, err := wb.AuthorizeURL(cfg, Secret{}, AuthorizeParams{RedirectURI: "https://s.example.com/cb", State: "st"})
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	parsed, _ := url.Parse(u)
	if parsed.Host != "api.weibo.com" || parsed.Path != "/oauth2/authorize" {
		t.Fatalf("authorize endpoint wrong: %s", u)
	}
	if got := parsed.Query().Get("scope"); got != "" {
		t.Fatalf("scope = %q, want empty（用应用默认授权范围）", got)
	}
}

func TestWeiboRegistered(t *testing.T) {
	if _, ok := Get("weibo"); !ok {
		t.Fatal("weibo must be registered on package load")
	}
}
