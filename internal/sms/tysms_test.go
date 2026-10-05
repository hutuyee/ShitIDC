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

// GET 参数拼装：u / p(md5) / m / c（带【签名】），"0" 成功。
func TestTysmsSend(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		writeStr(w, "0")
	}))
	defer srv.Close()

	ts := NewTysms()
	err := ts.Send(context.Background(),
		Config{Fields: map[string]string{"url": srv.URL, "sign": "通用短信"}},
		Secret{"user": "u1", "pass": "p1"},
		Message{Phone: "13800138000", Code: "952744", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sum := md5.Sum([]byte("p1"))
	if gotQuery.Get("u") != "u1" || gotQuery.Get("p") != hex.EncodeToString(sum[:]) {
		t.Fatalf("query = %v", gotQuery)
	}
	if gotQuery.Get("m") != "13800138000" {
		t.Fatalf("phone = %q", gotQuery.Get("m"))
	}
	content := gotQuery.Get("c")
	if !strings.HasPrefix(content, "【通用短信】") || !strings.Contains(content, "952744") {
		t.Fatalf("content = %q", content)
	}
}

// 错误码表与插件一致，溢出码和未知响应都要有交待。
func TestTysmsErrorCodes(t *testing.T) {
	cases := map[string]string{
		"41":                   "余额不足",
		"51":                   "手机号码不正确",
		"18446744073709551615": "参数不全",
		"999":                  "未知的响应",
	}
	for code, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeStr(w, code)
		}))
		ts := NewTysms()
		err := ts.Send(context.Background(),
			Config{Fields: map[string]string{"url": srv.URL, "sign": "S"}},
			Secret{"user": "u", "pass": "p"}, Message{Phone: "13800138000", Code: "1"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("code %s: want error containing %q, got %v", code, want, err)
		}
	}
}

func TestTysmsValidate(t *testing.T) {
	ts := NewTysms()
	if err := ts.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("empty config must be rejected")
	}
	cfg := Config{Fields: map[string]string{"url": "http://example.com/sms", "sign": "S"}}
	if err := ts.Validate(cfg, Secret{"user": "u", "pass": "p"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if _, ok := Get("tysms"); !ok {
		t.Fatal("tysms must be registered on package load")
	}
}
