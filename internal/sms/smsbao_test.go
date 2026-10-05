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

// 全流程：GET /sms，密码 MD5，内容带签名，"0" 成功。
func TestSmsbaoSendDomestic(t *testing.T) {
	var gotQuery url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		writeStr(w, "0")
	}))
	defer srv.Close()

	s := NewSmsbao()
	s.APIBase = srv.URL
	err := s.Send(context.Background(),
		Config{Fields: map[string]string{"sign": "短信宝测试"}},
		Secret{"user": "demo", "pass": "secret"},
		Message{Phone: "13800138000", Code: "952744", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/sms" {
		t.Fatalf("path = %s, want /sms", gotPath)
	}
	if gotQuery.Get("u") != "demo" {
		t.Fatalf("user = %q", gotQuery.Get("u"))
	}
	sum := md5.Sum([]byte("secret"))
	if gotQuery.Get("p") != hex.EncodeToString(sum[:]) {
		t.Fatalf("pass md5 = %q", gotQuery.Get("p"))
	}
	if !strings.HasPrefix(gotQuery.Get("c"), "【短信宝测试】") || !strings.Contains(gotQuery.Get("c"), "952744") {
		t.Fatalf("content wrong: %q", gotQuery.Get("c"))
	}
}

// 国际号码走 /wsms。
func TestSmsbaoSendInternational(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeStr(w, "0")
	}))
	defer srv.Close()

	s := NewSmsbao()
	s.APIBase = srv.URL
	err := s.Send(context.Background(),
		Config{Fields: map[string]string{"sign": "S"}},
		Secret{"user": "u", "pass": "p"},
		Message{Phone: "61400123456", Code: "1"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/wsms" {
		t.Fatalf("international path = %s, want /wsms", gotPath)
	}
}

// 错误码必须翻成人话（与插件 statusStr 一致），未知响应也要有交待。
func TestSmsbaoErrorCodes(t *testing.T) {
	cases := map[string]string{
		"51":  "手机号码不正确",
		"41":  "余额不足",
		"500": "未知的响应",
	}
	for code, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeStr(w, code)
		}))
		s := NewSmsbao()
		s.APIBase = srv.URL
		err := s.Send(context.Background(),
			Config{Fields: map[string]string{"sign": "S"}},
			Secret{"user": "u", "pass": "p"}, Message{Phone: "13800138000", Code: "1"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("code %s: want error containing %q, got %v", code, want, err)
		}
	}
}

func TestSmsbaoValidate(t *testing.T) {
	s := NewSmsbao()
	if err := s.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
	cfg := Config{Fields: map[string]string{"sign": "S"}}
	if err := s.Validate(cfg, Secret{"user": "u", "pass": "p"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if _, ok := Get("smsbao"); !ok {
		t.Fatal("smsbao must be registered on package load")
	}
}
