package sms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedSMSNow 与 TC3 向量共用同一时间戳，保证签名可复算。
var fixedSMSNow = time.Unix(1759650000, 0).UTC()

// TC3 签名向量：独立用 Python 按官方「签名方法 v3」复算得出，钉死实现。
// 输入固定，签名必须逐字符一致——签名链上任何一步（规范化头、密钥派生、
// hex 编码）写错都会在这里爆出来。
func TestQcloudsmsTC3SignatureVector(t *testing.T) {
	payload := `{"PhoneNumberSet":["+8613800138000"],"SmsSdkAppId":"1400000000","SignName":"测试签名","TemplateId":"100001","TemplateParamSet":["123456"]}`
	got := tc3Signature("AKIDtest", "TESTKEY", "2026-10-05", 1759650000, payload)
	want := "TC3-HMAC-SHA256 Credential=AKIDtest/2026-10-05/sms/tc3_request, SignedHeaders=content-type;host, Signature=f2556471dd627857394e9d218bf333f4e374414cc90ed024ccfc7c6ac3530b7c"
	if got != want {
		t.Fatalf("tc3Signature mismatch:\n got  %s\n want %s", got, want)
	}
}

// 全流程：请求形态（头、体、E.164 号码）+ 成功/两种失败判据。
func TestQcloudsmsSendFlow(t *testing.T) {
	var gotAuth string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody = readBody(r)
		switch r.Header.Get("X-TC-Action") {
		case "SendSms":
			if r.Header.Get("X-TC-Version") != "2021-01-11" {
				t.Errorf("X-TC-Version = %q", r.Header.Get("X-TC-Version"))
			}
			if r.Header.Get("X-TC-Region") != "ap-guangzhou" {
				t.Errorf("X-TC-Region = %q", r.Header.Get("X-TC-Region"))
			}
			writeStr(w, `{"Response":{"RequestId":"x","SendStatusSet":[{"SerialNo":"50:1","PhoneNumber":"+8613800138000","Fee":1,"Code":"Ok","Message":"send success"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q := NewQcloudsms()
	q.Endpoint = srv.URL
	q.Now = func() time.Time { return fixedSMSNow }
	q.Nonce = func() string { return "testnonce" }

	cfg := Config{Provider: "qcloudsms", Fields: map[string]string{
		"sms_sdk_app_id": "1400000000", "sign_name": "测试签名", "template_id": "100001",
	}}
	err := q.Send(context.Background(), cfg, Secret{"secret_id": "AKIDtest", "secret_key": "TESTKEY"},
		Message{Phone: "13800138000", Code: "123456", Purpose: "register", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// 号码必须被补成 E.164。
	if !strings.Contains(string(gotBody), `"+8613800138000"`) {
		t.Fatalf("phone not E.164 encoded: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), `"TemplateParamSet":["123456"]`) {
		t.Fatalf("template params wrong: %s", gotBody)
	}
	// 签名头结构必须齐备。
	if !strings.Contains(gotAuth, "Credential=AKIDtest/") || !strings.Contains(gotAuth, "SignedHeaders=content-type;host") {
		t.Fatalf("authorization header malformed: %s", gotAuth)
	}
}

// 平台级错误（Response.Error）必须带出 Message 与 Code。
func TestQcloudsmsApiError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"Response":{"Error":{"Code":"AuthFailure.SignatureFailure","Message":"The provided credentials could not be validated."},"RequestId":"x"}}`)
	}))
	defer srv.Close()

	q := NewQcloudsms()
	q.Endpoint = srv.URL
	err := q.Send(context.Background(),
		Config{Fields: map[string]string{"sms_sdk_app_id": "1", "sign_name": "s", "template_id": "t"}},
		Secret{"secret_id": "s", "secret_key": "k"}, Message{Phone: "13800138000", Code: "1"})
	if err == nil || !strings.Contains(err.Error(), "AuthFailure.SignatureFailure") {
		t.Fatalf("want api error surfaced, got %v", err)
	}
}

// 发送级错误：SendStatusSet 里第一条非 Ok。
func TestQcloudsmsSendStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"Response":{"SendStatusSet":[{"Code":"LimitExceeded.PhoneNumberDailyLimit","Message":"unlock"}],"RequestId":"x"}}`)
	}))
	defer srv.Close()

	q := NewQcloudsms()
	q.Endpoint = srv.URL
	err := q.Send(context.Background(),
		Config{Fields: map[string]string{"sms_sdk_app_id": "1", "sign_name": "s", "template_id": "t"}},
		Secret{"secret_id": "s", "secret_key": "k"}, Message{Phone: "13800138000", Code: "1"})
	if err == nil || !strings.Contains(err.Error(), "LimitExceeded.PhoneNumberDailyLimit") {
		t.Fatalf("want status error surfaced, got %v", err)
	}
}

func TestQcloudsmsValidate(t *testing.T) {
	q := NewQcloudsms()
	if err := q.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
	cfg := Config{Fields: map[string]string{"sms_sdk_app_id": "1", "sign_name": "s", "template_id": "t"}}
	if err := q.Validate(cfg, Secret{"secret_id": "s", "secret_key": "k"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if _, ok := Get("qcloudsms"); !ok {
		t.Fatal("qcloudsms must be registered on package load")
	}
}

// E.164 规则表驱动：大陆号码补 86，带国家码补 +，已是 + 原样。
func TestE164Phone(t *testing.T) {
	cases := map[string]string{
		"13800138000":    "+8613800138000",
		"8613800138000":  "+8613800138000",
		"61400123456":    "+61400123456",
		"+8613800138000": "+8613800138000",
	}
	for in, want := range cases {
		if got := e164Phone(in); got != want {
			t.Fatalf("e164Phone(%q) = %q, want %q", in, got, want)
		}
	}
	if !isMainlandPhone("13800138000") || isMainlandPhone("61400123456") {
		t.Fatal("isMainlandPhone misclassifies")
	}
}
