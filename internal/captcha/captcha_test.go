package captcha

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// writeStr 是测试里写响应的小工具。
func writeStr(w http.ResponseWriter, s string) {
	_, _ = io.WriteString(w, s)
}

// 谷歌：表单字段（secret / response / remoteip）与成功判据。
func TestGoogleVerifyFlow(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		writeStr(w, `{"success":true}`)
	}))
	defer srv.Close()

	g := NewGoogle()
	g.Endpoint = srv.URL
	cfg := Config{Fields: map[string]string{"site_key": "site-1"}}
	secret := Secret{"secret_key": "sk-1"}
	if err := g.Verify(context.Background(), cfg, secret, Token{Value: "tok", RemoteIP: "1.2.3.4"}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotForm.Get("secret") != "sk-1" || gotForm.Get("response") != "tok" || gotForm.Get("remoteip") != "1.2.3.4" {
		t.Fatalf("form = %v", gotForm)
	}
	if ch := g.Challenge(cfg); ch.Provider != "google_captcha" || ch.Params["site_key"] != "site-1" {
		t.Fatalf("challenge = %+v", ch)
	}
	if _, ok := Get("google_captcha"); !ok {
		t.Fatal("google_captcha must be registered on package load")
	}
}

// 谷歌：error-codes 必须翻成人话，票据为空要提前拦。
func TestGoogleErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"success":false,"error-codes":["invalid-input-response"]}`)
	}))
	defer srv.Close()

	g := NewGoogle()
	g.Endpoint = srv.URL
	cfg := Config{Fields: map[string]string{"site_key": "s"}}
	err := g.Verify(context.Background(), cfg, Secret{"secret_key": "k"}, Token{Value: "x"})
	if err == nil || !strings.Contains(err.Error(), "验证票据无效或已过期") {
		t.Fatalf("want translated error, got %v", err)
	}
	if err := g.Verify(context.Background(), cfg, Secret{"secret_key": "k"}, Token{}); err == nil {
		t.Fatal("empty token must be rejected before calling upstream")
	}
	if err := g.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing site_key/secret must be rejected")
	}
}

// 腾讯：请求头、TC3 签名作用域与请求体字段必须与官方 API 3.0 一致。
func TestTencentVerifyFlow(t *testing.T) {
	var gotAction, gotVersion, gotTimestamp, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.Header.Get("X-TC-Action")
		gotVersion = r.Header.Get("X-TC-Version")
		gotTimestamp = r.Header.Get("X-TC-Timestamp")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		writeStr(w, `{"Response":{"CaptchaCode":1,"CaptchaMsg":"ok","RequestId":"x"}}`)
	}))
	defer srv.Close()

	tc := NewTencent()
	tc.Endpoint = srv.URL
	tc.Now = func() time.Time { return time.Unix(1759650000, 0) }
	cfg := Config{Fields: map[string]string{"captcha_app_id": "12345"}}
	secret := Secret{"secret_id": "AKID", "secret_key": "KEY", "app_secret_key": "appsk"}
	if err := tc.Verify(context.Background(), cfg, secret, Token{Value: "ticket1", Randstr: "rand1", RemoteIP: "9.9.9.9"}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotAction != "DescribeCaptchaResult" || gotVersion != "2019-07-22" || gotTimestamp != "1759650000" {
		t.Fatalf("headers wrong: action=%q version=%q ts=%q", gotAction, gotVersion, gotTimestamp)
	}
	if !strings.Contains(gotAuth, "Credential=AKID/") || !strings.Contains(gotAuth, "/captcha/tc3_request") {
		t.Fatalf("authorization scope wrong: %s", gotAuth)
	}
	for _, want := range []string{`"Ticket":"ticket1"`, `"Randstr":"rand1"`, `"CaptchaAppId":12345`, `"CaptchaType":9`, `"AppSecretKey":"appsk"`, `"UserIp":"9.9.9.9"`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("body %s missing %s", gotBody, want)
		}
	}
	if ch := tc.Challenge(cfg); ch.Provider != "tencent_captcha" || ch.Params["captcha_app_id"] != "12345" {
		t.Fatalf("challenge = %+v", ch)
	}
}

// 腾讯：平台错误与业务失败码都要带出可读信息。
func TestTencentErrorResponse(t *testing.T) {
	apiErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"Response":{"Error":{"Code":"AuthFailure.SignatureFailure","Message":"签名错误"}}}`)
	}))
	defer apiErr.Close()
	tc := NewTencent()
	tc.Endpoint = apiErr.URL
	cfg := Config{Fields: map[string]string{"captcha_app_id": "1"}}
	secret := Secret{"secret_id": "s", "secret_key": "k", "app_secret_key": "a"}
	err := tc.Verify(context.Background(), cfg, secret, Token{Value: "t", Randstr: "r"})
	if err == nil || !strings.Contains(err.Error(), "签名错误") {
		t.Fatalf("want api error surfaced, got %v", err)
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"Response":{"CaptchaCode":7,"CaptchaMsg":"验证码已过期"}}`)
	}))
	defer fail.Close()
	tc.Endpoint = fail.URL
	err = tc.Verify(context.Background(), cfg, secret, Token{Value: "t", Randstr: "r"})
	if err == nil || !strings.Contains(err.Error(), "验证码已过期") {
		t.Fatalf("want business error surfaced, got %v", err)
	}
}

func TestTencentValidate(t *testing.T) {
	tc := NewTencent()
	if err := tc.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
	cfg := Config{Fields: map[string]string{"captcha_app_id": "abc"}}
	if err := tc.Validate(cfg, Secret{"secret_id": "s", "secret_key": "k", "app_secret_key": "a"}); err == nil {
		t.Fatal("non-numeric app id must be rejected")
	}
	if _, ok := Get("tencent_captcha"); !ok {
		t.Fatal("tencent_captcha must be registered on package load")
	}
}
