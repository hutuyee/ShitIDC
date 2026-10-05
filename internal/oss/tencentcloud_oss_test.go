package oss

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 全流程：探活（HEAD 桶）→ 上传（PUT，图片带 public-read）→ HEAD 存在性 →
// 签名下载地址。签名用固定时钟复算：只校验协议骨架与请求确实被发出。
func TestTencentCOSFlow(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodHead:
			if strings.HasSuffix(r.URL.Path, "missing.txt") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			if r.Header.Get("x-cos-acl") != "public-read" {
				t.Errorf("acl = %q", r.Header.Get("x-cos-acl"))
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "q-sign-algorithm=sha1&q-ak=id1&") {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != "hello" {
				t.Errorf("body = %q", string(b))
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	p := NewTencentCloud()
	p.Endpoint = srv.URL
	p.Now = func() time.Time { return time.Unix(1700000000, 0) }
	cfg := Config{Provider: "tencentcloud_oss", Fields: map[string]string{"bucket": "b1", "region": "ap-guangzhou"}}
	secret := Secret{"secret_id": "id1", "secret_key": "sk1"}

	if err := p.TestLink(context.Background(), cfg, secret); err != nil {
		t.Fatalf("TestLink: %v", err)
	}
	if err := p.Upload(context.Background(), cfg, secret, "img/a.png", []byte("hello"), "image/png", "public-read"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	ok, err := p.Exists(context.Background(), cfg, secret, "img/a.png")
	if err != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	ok, err = p.Exists(context.Background(), cfg, secret, "missing.txt")
	if err != nil || ok {
		t.Fatalf("Exists(missing) = %v, %v", ok, err)
	}
	signed, err := p.SignedURL(context.Background(), cfg, secret, "img/a.png", 3*time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if !strings.Contains(signed, "q-sign-algorithm=sha1") || !strings.Contains(signed, "q-sign-time=1700000000%3B1700000180") {
		t.Fatalf("SignedURL = %q", signed)
	}
	if len(methods) != 4 {
		t.Fatalf("methods = %v", methods)
	}
	if methods[0] != "HEAD /" || methods[1] != "PUT /img/a.png" || methods[2] != "HEAD /img/a.png" || methods[3] != "HEAD /missing.txt" {
		t.Fatalf("methods = %v", methods)
	}
}
