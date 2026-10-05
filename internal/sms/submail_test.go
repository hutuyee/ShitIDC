package sms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 全流程：国内 message/send.json；内容以【签名】开头；signature=appkey。
func TestSubmailSendDomestic(t *testing.T) {
	var gotForm url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = r.ParseForm()
		gotForm = r.PostForm
		writeStr(w, `{"status":"success","fee":1,"cnt":1}`)
	}))
	defer srv.Close()

	s := NewSubmail()
	s.APIBase = srv.URL + "/"
	err := s.Send(context.Background(),
		Config{Fields: map[string]string{"app_id": "APPID", "app_sign": "智简魔方"}},
		Secret{"app_key": "APPKEY"},
		Message{Phone: "13800138000", Code: "246810", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/message/send.json" {
		t.Fatalf("endpoint = %s, want /message/send.json", gotPath)
	}
	if gotForm.Get("appid") != "APPID" || gotForm.Get("appkey") != "APPKEY" || gotForm.Get("signature") != "APPKEY" {
		t.Fatalf("auth fields wrong: %v", gotForm)
	}
	content := gotForm.Get("content")
	// 签名统一成中文括号（插件 templateSign 的行为），且验证码已渲染。
	if !strings.HasPrefix(content, "【智简魔方】") {
		t.Fatalf("content signature prefix wrong: %q", content)
	}
	if !strings.Contains(content, "246810") {
		t.Fatalf("code not rendered into content: %q", content)
	}
}

// 国际号码自动分流到 internationalsms/send.json，用国际凭据。
func TestSubmailSendInternational(t *testing.T) {
	var gotPath string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = r.ParseForm()
		gotForm = r.PostForm
		writeStr(w, `{"status":"success"}`)
	}))
	defer srv.Close()

	s := NewSubmail()
	s.APIBase = srv.URL + "/"
	err := s.Send(context.Background(),
		Config{Fields: map[string]string{
			"app_id": "CN", "app_sign": "签名",
			"international_app_id": "GL", "international_app_sign": "GLOBAL",
		}},
		Secret{"app_key": "CNKEY", "international_app_key": "GLKEY"},
		Message{Phone: "61400123456", Code: "111222"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/internationalsms/send.json" {
		t.Fatalf("endpoint = %s", gotPath)
	}
	if gotForm.Get("appid") != "GL" || gotForm.Get("signature") != "GLKEY" {
		t.Fatalf("international creds wrong: %v", gotForm)
	}

	// 国际凭据缺失必须明确报错。
	err = s.Send(context.Background(),
		Config{Fields: map[string]string{"app_id": "CN", "app_sign": "签名"}},
		Secret{"app_key": "CNKEY"},
		Message{Phone: "61400123456", Code: "111222"})
	if err == nil || !strings.Contains(err.Error(), "international") {
		t.Fatalf("want international config error, got %v", err)
	}
}

func TestSubmailErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"status":"error","code":"20","msg":"appkey错误"}`)
	}))
	defer srv.Close()

	s := NewSubmail()
	s.APIBase = srv.URL + "/"
	err := s.Send(context.Background(),
		Config{Fields: map[string]string{"app_id": "A", "app_sign": "S"}},
		Secret{"app_key": "K"}, Message{Phone: "13800138000", Code: "1"})
	if err == nil || !strings.Contains(err.Error(), "appkey错误") {
		t.Fatalf("want submail msg surfaced, got %v", err)
	}
}

func TestSubmailValidate(t *testing.T) {
	s := NewSubmail()
	if err := s.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
	cfg := Config{Fields: map[string]string{"app_id": "a", "app_sign": "s"}}
	if err := s.Validate(cfg, Secret{"app_key": "k"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if _, ok := Get("submail"); !ok {
		t.Fatal("submail must be registered on package load")
	}
}
