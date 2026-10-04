package sms

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 阿里云签名的三处编码差异必须与官方一致，否则签名校验会失败。
func TestAliPercentEncode(t *testing.T) {
	cases := map[string]string{
		"abc":                  "abc",
		"a b":                  "a%20b", // 空格 -> %20（不是 +）
		"a+b":                  "a%2Bb", // 加号本身要被编码
		"a*b":                  "a%2Ab", // 星号 -> %2A
		"a~b":                  "a~b",   // 波浪号必须保持原样（不是 %7E）
		"2024-01-01T00:00:00Z": "2024-01-01T00%3A00%3A00Z",
	}
	for in, want := range cases {
		if got := aliPercentEncode(in); got != want {
			t.Fatalf("aliPercentEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

// 规范化查询串必须按参数名排序。
func TestCanonicalQueryIsSorted(t *testing.T) {
	got := canonicalQuery(map[string]string{"b": "2", "a": "1", "c": "3"})
	if got != "a=1&b=2&c=3" {
		t.Fatalf("canonicalQuery = %q, want a=1&b=2&c=3", got)
	}
}

// 签名必须可复算：同样的参数与密钥永远得到同样的签名。
func TestAliyunSignatureIsDeterministic(t *testing.T) {
	params := map[string]string{
		"AccessKeyId":    "testid",
		"Action":         "SendSms",
		"PhoneNumbers":   "13800138000",
		"SignatureNonce": "fixednonce",
		"Timestamp":      "2024-01-01T00:00:00Z",
		"TemplateParam":  `{"code":"123456"}`,
		"SignName":       "测试签名",
		"TemplateCode":   "SMS_123",
		"Version":        "2017-05-25",
	}
	sig1 := aliyunSignature("testsecret", params)
	sig2 := aliyunSignature("testsecret", params)
	if sig1 != sig2 {
		t.Fatalf("signature is not deterministic: %s vs %s", sig1, sig2)
	}
	if sig1 == aliyunSignature("othersecret", params) {
		t.Fatal("a different secret produced the same signature")
	}
	// 换掉任意一个参数，签名必须变化。
	params["PhoneNumbers"] = "13900139000"
	if aliyunSignature("testsecret", params) == sig1 {
		t.Fatal("signature did not change when a parameter changed")
	}
}

// captureAliyun 起一个假端点，记录收到的表单并返回可控响应。
func captureAliyun(t *testing.T, respond string) (*Aliyun, *url.Values) {
	t.Helper()
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respond))
	}))
	t.Cleanup(srv.Close)
	a := NewAliyun()
	a.Endpoint = srv.URL
	// 固定时间与随机数，让签名可复算。
	a.Now = func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) }
	a.Random = func() string { return "fixednonce" }
	return a, &captured
}

func TestAliyunSendHappyPath(t *testing.T) {
	var captured *url.Values
	a, capturedPtr := captureAliyun(t, `{"Code":"OK","Message":"OK"}`)
	captured = capturedPtr
	cfg := Config{Provider: "aliyun", Fields: map[string]string{
		"sign_name": "测试签名", "template_code": "SMS_123", "region_id": "cn-hangzhou",
	}}
	secret := Secret{"access_key_id": "testid", "access_key_secret": "testsecret"}
	err := a.Send(context.Background(), cfg, secret, Message{Phone: "13800138000", Code: "123456", Purpose: "register"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	form := *captured
	if form.Get("PhoneNumbers") != "13800138000" {
		t.Fatalf("phone not forwarded: %q", form.Get("PhoneNumbers"))
	}
	if form.Get("Action") != "SendSms" {
		t.Fatalf("action = %q, want SendSms", form.Get("Action"))
	}
	// 关键：签名必须能用同一套参数与密钥复算出来。
	params := map[string]string{}
	for k := range form {
		if k == "Signature" {
			continue
		}
		params[k] = form.Get(k)
	}
	if got, want := form.Get("Signature"), aliyunSignature("testsecret", params); got != want {
		t.Fatalf("signature %s does not verify (want %s)", got, want)
	}
	// 模板参数里必须带上验证码。
	var tp map[string]string
	if err := json.Unmarshal([]byte(form.Get("TemplateParam")), &tp); err != nil {
		t.Fatalf("template param is not JSON: %q", form.Get("TemplateParam"))
	}
	if tp["code"] != "123456" {
		t.Fatalf("code = %q, want 123456", tp["code"])
	}
}

func TestAliyunSendSurfacesError(t *testing.T) {
	a, _ := captureAliyun(t, `{"Code":"isv.BUSINESS_LIMIT_CONTROL","Message":"触发流控"}`)
	cfg := Config{Provider: "aliyun", Fields: map[string]string{"sign_name": "s", "template_code": "t"}}
	secret := Secret{"access_key_id": "id", "access_key_secret": "sec"}
	err := a.Send(context.Background(), cfg, secret, Message{Phone: "13800138000", Code: "1"})
	if err == nil {
		t.Fatal("a non-OK code must be an error")
	}
	if !strings.Contains(err.Error(), "触发流控") {
		t.Fatalf("error %q lost the gateway message", err.Error())
	}
}

func TestAliyunValidateRequiresFields(t *testing.T) {
	a := NewAliyun()
	if err := a.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("empty config must be rejected")
	}
	cfg := Config{Fields: map[string]string{"sign_name": "s", "template_code": "t"}}
	if err := a.Validate(cfg, Secret{"access_key_id": "id"}); err == nil {
		t.Fatal("missing access_key_secret must be rejected")
	}
	if err := a.Validate(cfg, Secret{"access_key_id": "id", "access_key_secret": "sec"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
}

func TestRegistryKnowsAliyun(t *testing.T) {
	if _, ok := Get("aliyun"); !ok {
		t.Fatal("the aliyun provider must be registered")
	}
	names := Names()
	found := false
	for _, n := range names {
		if n == "aliyun" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Names() = %v, want it to contain aliyun", names)
	}
}
