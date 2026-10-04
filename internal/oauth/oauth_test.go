package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// SafeRedirectTo 决定「登录后把用户送去哪」，写错就是开放重定向漏洞。
func TestSafeRedirectToBlocksOpenRedirect(t *testing.T) {
	bad := []string{
		"//evil.com",
		"//evil.com/path",
		"/" + string(rune(0x5c)) + "evil.com",
		"https://evil.com",
		"http://evil.com",
		"javascript:alert(1)",
		"evil.com",
		"/a/" + string(rune(0x5c)) + "b",
		"/ok\r\nSet-Cookie: x=1",
	}
	for _, in := range bad {
		if got := SafeRedirectTo(in); got != "/" {
			t.Fatalf("SafeRedirectTo(%q) = %q, want /", in, got)
		}
	}
	good := map[string]string{
		"":                  "/",
		"   ":               "/",
		"/dashboard":        "/dashboard",
		"/services/abc?x=1": "/services/abc?x=1",
		"/a/b/c":            "/a/b/c",
	}
	for in, want := range good {
		if got := SafeRedirectTo(in); got != want {
			t.Fatalf("SafeRedirectTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeIdentityRequiresSubject(t *testing.T) {
	if _, err := normalizeIdentity(Identity{Provider: "github"}); err == nil {
		t.Fatal("an identity with no subject must be rejected")
	}
	id, err := normalizeIdentity(Identity{Provider: "github", Subject: "  42  ", Email: " A@B.COM "})
	if err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	if id.Subject != "42" {
		t.Fatalf("subject = %q, want it trimmed to 42", id.Subject)
	}
	// 邮箱统一小写：否则同一邮箱会因大小写不同被当成两个账号。
	if id.Email != "a@b.com" {
		t.Fatalf("email = %q, want a@b.com", id.Email)
	}
}

func TestGitHubValidateAndAuthorizeURL(t *testing.T) {
	g := NewGitHub()
	if err := g.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing client_id must be rejected")
	}
	cfg := Config{Fields: map[string]string{"client_id": "cid"}}
	if err := g.Validate(cfg, Secret{}); err == nil {
		t.Fatal("missing client_secret must be rejected")
	}
	if err := g.Validate(cfg, Secret{"client_secret": "sec"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	u, err := g.AuthorizeURL(cfg, Secret{}, AuthorizeParams{RedirectURI: "https://s.example.com/cb", State: "st-1"})
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("client_id") != "cid" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	// state 必须原样带上，否则回调无法防重放。
	if q.Get("state") != "st-1" {
		t.Fatalf("state = %q, want st-1", q.Get("state"))
	}
	if q.Get("redirect_uri") != "https://s.example.com/cb" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("scope") == "" {
		t.Fatal("scope must not be empty")
	}
}

// fakeGitHub 起一个假的 GitHub，覆盖 token 与资料端点。
type fakeGitHub struct {
	server    *httptest.Server
	emailHits int64
	tokenBody string
	userAuth  string
}

func newFakeGitHub(t *testing.T, profileEmail string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			body, _ := io.ReadAll(r.Body)
			f.tokenBody = string(body)
			_, _ = w.Write([]byte(`{"access_token":"GHO_TOKEN","token_type":"bearer"}`))
		case "/user":
			f.userAuth = r.Header.Get("Authorization")
			if profileEmail == "" {
				_, _ = w.Write([]byte(`{"id":42,"login":"octocat","name":"The Octocat","avatar_url":"https://a.example.com/x.png","email":null}`))
			} else {
				_, _ = w.Write([]byte(`{"id":42,"login":"octocat","name":"The Octocat","avatar_url":"https://a.example.com/x.png","email":"` + profileEmail + `"}`))
			}
		case "/user/emails":
			atomic.AddInt64(&f.emailHits, 1)
			_, _ = w.Write([]byte(`[{"email":"other@example.com","primary":false,"verified":true},{"email":"Primary@Example.com","primary":true,"verified":true}]`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) provider() *GitHub {
	g := NewGitHub()
	g.TokenEndpoint = f.server.URL + "/login/oauth/access_token"
	g.AuthorizeEndpoint = f.server.URL + "/login/oauth/authorize"
	g.APIBase = f.server.URL
	return g
}

func TestGitHubExchangeHappyPath(t *testing.T) {
	f := newFakeGitHub(t, "Public@Example.com")
	g := f.provider()
	cfg := Config{Provider: "github", Fields: map[string]string{"client_id": "cid"}}
	secret := Secret{"client_secret": "sec"}
	id, err := g.Exchange(context.Background(), cfg, secret, "CODE-1", "https://s.example.com/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	// 数字 ID 必须转成字符串当 subject。
	if id.Subject != "42" {
		t.Fatalf("subject = %q, want 42", id.Subject)
	}
	if id.Provider != "github" {
		t.Fatalf("provider = %q", id.Provider)
	}
	if id.Nickname != "The Octocat" {
		t.Fatalf("nickname = %q, want the display name", id.Nickname)
	}
	if id.Email != "public@example.com" {
		t.Fatalf("email = %q, want it lower-cased", id.Email)
	}
	// access_token 必须通过 Authorization 头发出去，不能放在 query 里。
	if f.userAuth != "Bearer GHO_TOKEN" {
		t.Fatalf("authorization header = %q", f.userAuth)
	}
	// token 请求要带上 code 与 client_secret。
	form, err := url.ParseQuery(f.tokenBody)
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("code") != "CODE-1" || form.Get("client_id") != "cid" || form.Get("client_secret") != "sec" {
		t.Fatalf("token request body = %q", f.tokenBody)
	}
}

// 邮箱不公开时必须去 /user/emails 找主邮箱，且只认 primary+verified 的那个。
func TestGitHubExchangeFallsBackToPrimaryEmail(t *testing.T) {
	f := newFakeGitHub(t, "")
	g := f.provider()
	cfg := Config{Fields: map[string]string{"client_id": "cid"}}
	id, err := g.Exchange(context.Background(), cfg, Secret{"client_secret": "sec"}, "C", "https://s/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if atomic.LoadInt64(&f.emailHits) != 1 {
		t.Fatalf("the emails endpoint was called %d times, want 1", f.emailHits)
	}
	if id.Email != "primary@example.com" {
		t.Fatalf("email = %q, want the primary verified one", id.Email)
	}
}

func TestGitHubExchangeSurfacesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"bad_verification_code","error_description":"The code passed is incorrect"}`))
	}))
	t.Cleanup(srv.Close)
	g := NewGitHub()
	g.TokenEndpoint = srv.URL
	cfg := Config{Fields: map[string]string{"client_id": "cid"}}
	_, err := g.Exchange(context.Background(), cfg, Secret{"client_secret": "sec"}, "BAD", "https://s/cb")
	if err == nil {
		t.Fatal("a token error response must be an error")
	}
	if !strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("error %q lost the platform error code", err.Error())
	}
}

func TestRegistryKnowsProviders(t *testing.T) {
	if _, ok := Get("github"); !ok {
		t.Fatal("github must be registered")
	}
	names := Names()
	found := false
	for _, n := range names {
		if n == "github" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Names() = %v, want it to contain github", names)
	}
}
