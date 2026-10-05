package sms

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 表单字段与 md5(key) 必须与插件一致；code 是数字 1 时算成功。
func TestPuddingv10smsSend(t *testing.T) {
	var gotForm url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = r.PostForm
		writeStr(w, `{"code":1,"msg":"ok"}`)
	}))
	defer srv.Close()

	p := NewPuddingv10sms()
	p.Endpoint = srv.URL + "/sendApi"
	err := p.Send(context.Background(),
		Config{Fields: map[string]string{"username": "user1", "channel": "ch9", "sign": "布丁云"}},
		Secret{"key": "sk-123"},
		Message{Phone: "13800138000", Code: "952744", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/sendApi" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotForm.Get("username") != "user1" || gotForm.Get("channel") != "ch9" {
		t.Fatalf("form = %v", gotForm)
	}
	sum := md5.Sum([]byte("sk-123"))
	if gotForm.Get("key") != hex.EncodeToString(sum[:]) {
		t.Fatalf("key = %q", gotForm.Get("key"))
	}
	if gotForm.Get("phone") != "13800138000" {
		t.Fatalf("phone = %q", gotForm.Get("phone"))
	}
	content := gotForm.Get("content")
	if !strings.HasPrefix(content, "【布丁云】") || !strings.Contains(content, "952744") {
		t.Fatalf("content = %q", content)
	}
}

// PHP 是松散比较，字符串 "1" 也必须算成功。
func TestPuddingv10smsAcceptsStringCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"code":"1","msg":"ok"}`)
	}))
	defer srv.Close()

	p := NewPuddingv10sms()
	p.Endpoint = srv.URL
	if err := p.Send(context.Background(),
		Config{Fields: map[string]string{"username": "u", "channel": "c", "sign": "s"}},
		Secret{"key": "k"}, Message{Phone: "13800138000", Code: "1"}); err != nil {
		t.Fatalf("string code 1 must be accepted: %v", err)
	}
}

func TestPuddingv10smsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"code":0,"msg":"余额不足"}`)
	}))
	defer srv.Close()

	p := NewPuddingv10sms()
	p.Endpoint = srv.URL
	err := p.Send(context.Background(),
		Config{Fields: map[string]string{"username": "u", "channel": "c", "sign": "s"}},
		Secret{"key": "k"}, Message{Phone: "13800138000", Code: "1"})
	if err == nil || !strings.Contains(err.Error(), "余额不足") {
		t.Fatalf("want 余额不足 error, got %v", err)
	}
}

func TestPuddingv10smsValidate(t *testing.T) {
	p := NewPuddingv10sms()
	if err := p.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("empty config must be rejected")
	}
	cfg := Config{Fields: map[string]string{"username": "u", "channel": "c", "sign": "s"}}
	if err := p.Validate(cfg, Secret{"key": "k"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if _, ok := Get("puddingv10sms"); !ok {
		t.Fatal("puddingv10sms must be registered on package load")
	}
}
