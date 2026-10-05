package sms

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 全流程：sign 是 md5(account+authCode+timestamp)，Authorization 是
// 大写(base64(account:timestamp))，JSON 体里 appId 取 account（参考实现
// 误取不存在的 appid，这里钉死正确口径）。
func TestOfficesmsSend(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotBody = readBody(r)
		writeStr(w, `{"code":"0000000","msg":"ok"}`)
	}))
	defer srv.Close()

	o := NewOfficesms()
	o.Endpoint = srv.URL
	fixed := time.Unix(1700000000, 0)
	o.Now = func() time.Time { return fixed }
	err := o.Send(context.Background(),
		Config{Fields: map[string]string{"account": "acct1", "channel": "ch1", "signature": "第二办公室"}},
		Secret{"auth_code": "auth123"},
		Message{Phone: "13800138000", Code: "952744", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/Accounts/acct1/Sms/SendSms" {
		t.Fatalf("path = %s", gotPath)
	}
	sum := md5.Sum([]byte("acct1" + "auth123" + "1700000000"))
	if gotQuery != "sign="+hex.EncodeToString(sum[:]) {
		t.Fatalf("query = %s", gotQuery)
	}
	wantAuth := strings.ToUpper(base64.StdEncoding.EncodeToString([]byte("acct1:1700000000")))
	if gotAuth != wantAuth {
		t.Fatalf("authorization = %q, want %q", gotAuth, wantAuth)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body not json: %v (%s)", err, gotBody)
	}
	if body["appId"] != "acct1" || body["channel"] != "ch1" || body["sendType"] != "1" {
		t.Fatalf("body fields wrong: %v", body)
	}
	if body["mobile"] != "13800138000" || body["timestamp"] != float64(1700000000) {
		t.Fatalf("body fields wrong: %v", body)
	}
	if body["smsid"] != "1700000000000000000" {
		t.Fatalf("smsid = %v", body["smsid"])
	}
	content, _ := body["content"].(string)
	if !strings.HasPrefix(content, "【第二办公室】") || !strings.Contains(content, "952744") {
		t.Fatalf("content = %q", content)
	}
}

// 平台返回业务错误时要把 msg 带出来，而不是泛泛的失败。
func TestOfficesmsErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"code":"1001","msg":"余额不足"}`)
	}))
	defer srv.Close()

	o := NewOfficesms()
	o.Endpoint = srv.URL
	err := o.Send(context.Background(),
		Config{Fields: map[string]string{"account": "a", "channel": "c", "signature": "s"}},
		Secret{"auth_code": "x"},
		Message{Phone: "13800138000", Code: "1"})
	if err == nil || !strings.Contains(err.Error(), "余额不足") {
		t.Fatalf("want 余额不足 error, got %v", err)
	}
}

func TestOfficesmsValidate(t *testing.T) {
	o := NewOfficesms()
	if err := o.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("empty config must be rejected")
	}
	cfg := Config{Fields: map[string]string{"account": "a", "channel": "c", "signature": "s"}}
	if err := o.Validate(cfg, Secret{"auth_code": "x"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if _, ok := Get("officesms"); !ok {
		t.Fatal("officesms must be registered on package load")
	}
}
