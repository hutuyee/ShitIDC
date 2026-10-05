package sms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// POST form（默认）：body 由模板渲染，{{secret:*}} 注入，占位符全部展开。
func TestGenericSendPostForm(t *testing.T) {
	var gotBody string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readBody(r)
		gotContentType = r.Header.Get("Content-Type")
		writeStr(w, "success")
	}))
	defer srv.Close()

	g := NewGenericHTTP()
	err := g.Send(context.Background(),
		Config{Fields: map[string]string{
			"endpoint": srv.URL, "content_template": "您的验证码是{code}",
			"body_template": `uid={{secret:uid}}&mobile={{phone}}&text={{content}}&t={{timestamp}}`,
		}},
		Secret{"uid": "u123", "token": "topsecret"},
		Message{Phone: "13800138000", Code: "778899", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("content type = %q", gotContentType)
	}
	for _, want := range []string{"uid=u123", "mobile=13800138000", "text=您的验证码是778899"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("body missing %q: %s", want, gotBody)
		}
	}
	// 没被模板引用的凭据绝不能出现。
	if strings.Contains(gotBody, "topsecret") {
		t.Fatalf("unreferenced secret leaked: %s", gotBody)
	}
}

// JSON content_type 与 {{code}} 直接注入。
func TestGenericSendJSON(t *testing.T) {
	var gotBody string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readBody(r)
		gotContentType = r.Header.Get("Content-Type")
		writeStr(w, `{"code":0}`)
	}))
	defer srv.Close()

	g := NewGenericHTTP()
	err := g.Send(context.Background(),
		Config{Fields: map[string]string{
			"endpoint": srv.URL, "content_type": "json",
			"body_template": `{"mobile":"{{phone}}","code":"{{code}}"}`,
		}},
		Secret{"uid": "u"},
		Message{Phone: "13800138000", Code: "334455"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q", gotContentType)
	}
	if !strings.Contains(gotBody, `"mobile":"13800138000"`) || !strings.Contains(gotBody, `"code":"334455"`) {
		t.Fatalf("json body wrong: %s", gotBody)
	}
}

// GET：模板作为 query 追加，值逐个 URL 编码。
func TestGenericSendGetQuery(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		writeStr(w, "ok")
	}))
	defer srv.Close()

	g := NewGenericHTTP()
	err := g.Send(context.Background(),
		Config{Fields: map[string]string{
			"endpoint": srv.URL + "/send", "method": "GET",
			"body_template": `m={{phone}}&c={{content}}`,
		}},
		Secret{}, Message{Phone: "13800138000", Code: "123456"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.Contains(gotRawQuery, "m=13800138000") {
		t.Fatalf("query = %s", gotRawQuery)
	}
	// 中文 content 必须被编码（%E6 类字节而不是裸 UTF-8）。
	if !strings.Contains(gotRawQuery, "%") {
		t.Fatalf("query values not encoded: %s", gotRawQuery)
	}
}

// success_keyword 配置后，响应不含关键字必须失败——这是通用通道唯一的成功判据。
func TestGenericSuccessKeyword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		writeStr(w, `{"code":-1,"msg":"余额不足"}`)
	}))
	defer srv.Close()

	g := NewGenericHTTP()
	err := g.Send(context.Background(),
		Config{Fields: map[string]string{
			"endpoint": srv.URL, "content_template": "code {code}", "success_keyword": "code\":0",
		}},
		Secret{}, Message{Phone: "13800138000", Code: "1"})
	if err == nil || !strings.Contains(err.Error(), "余额不足") {
		t.Fatalf("want keyword failure surfaced, got %v", err)
	}
}

func TestGenericValidate(t *testing.T) {
	g := NewGenericHTTP()
	if err := g.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing endpoint must be rejected")
	}
	cfg := Config{Fields: map[string]string{"endpoint": "http://x"}}
	if err := g.Validate(cfg, Secret{}); err == nil {
		t.Fatal("missing templates must be rejected")
	}
	cfg.Fields["content_template"] = "c"
	if err := g.Validate(cfg, Secret{}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	cfg.Fields["method"] = "PUT"
	if err := g.Validate(cfg, Secret{}); err == nil {
		t.Fatal("method PUT must be rejected")
	}
	if _, ok := Get("generic"); !ok {
		t.Fatal("generic must be registered on package load")
	}
}

// 模板里遗留的 secret 占位符（键不存在）必须被清空，不能原样出现在请求里。
func TestGenericUnknownSecretPlaceholderCleared(t *testing.T) {
	got := renderSMSBody(
		Config{Fields: map[string]string{"body_template": "k={{secret:missing}}&m={{phone}}"}},
		Secret{}, Message{Phone: "13800138000", Code: "1"}, "content")
	if strings.Contains(got, "{{secret:") {
		t.Fatalf("leftover secret placeholder leaked: %s", got)
	}
	if !strings.Contains(got, "m=13800138000") {
		t.Fatalf("phone missing: %s", got)
	}
}
