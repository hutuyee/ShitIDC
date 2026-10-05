package sms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// WSSE PasswordDigest 向量：独立用 Python 复算。
// 口径是 Base64(SHA256(Nonce+Created+Secret) 的十六进制串)，
// 与魔方 PHP 插件 base64_encode(hash('sha256', ...)) 一致。
// 这个向量同时挡住两种写错：Base64(原始摘要) 与先 Base64 再 hex。
func TestHuaweicloudWSSEHeaderVector(t *testing.T) {
	got := buildWSSEHeader("AKTEST", "SECRET1", "2026-10-05T00:00:00Z", "noncenonce")
	want := `UsernameToken Username="AKTEST",PasswordDigest="NjAxYzg5N2I5NjQ2ZWQ0MTQ5YmVhOWIxNmQzYjNiYmQ1ZmE0NmE2ZDk2YzM1ZTI1ZTRmYmM2MjhmMjdjMDFiNQ==",Nonce="noncenonce",Created="2026-10-05T00:00:00Z"`
	if got != want {
		t.Fatalf("wsse header mismatch:\n got  %s\n want %s", got, want)
	}
}

// 全流程：国内（含签名参数）+ 国际（免签名、独立凭据）+ 错误判据。
func TestHuaweicloudSendFlowDomestic(t *testing.T) {
	var gotBody string
	var gotWSSE string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readBody(r)
		gotWSSE = r.Header.Get("X-WSSE")
		writeStr(w, `{"result":"0","smsMsgId":"0000","code":"000000","description":"Success"}`)
	}))
	defer srv.Close()

	h := NewHuaweicloud()
	h.Endpoint = srv.URL
	h.Now = func() time.Time { return time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) }
	h.Nonce = func() string { return "noncenonce" }

	cfg := Config{Fields: map[string]string{
		"sender": "1069050099999996", "sign_name": "华为云测试", "template_id": "T100",
	}}
	err := h.Send(context.Background(), cfg,
		Secret{"app_key": "AKTEST", "app_secret": "SECRET1"},
		Message{Phone: "13800138000", Code: "654321", TTLMinutes: 10})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	body := string(gotBody)
	if !strings.Contains(body, "to=%2B8613800138000") {
		t.Fatalf("phone not +86 prefixed: %s", body)
	}
	if !strings.Contains(body, "templateParas=%5B%22654321%22%5D") {
		t.Fatalf("template paras wrong: %s", body)
	}
	if !strings.Contains(body, "from=1069050099999996") || !strings.Contains(body, "signature=") {
		t.Fatalf("from/signature missing: %s", body)
	}
	if !strings.Contains(gotWSSE, `Username="AKTEST"`) {
		t.Fatalf("wsse header wrong: %s", gotWSSE)
	}
}

func TestHuaweicloudSendFlowInternational(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"code":"000000","description":"Success"}`)
	}))
	defer srv.Close()

	h := NewHuaweicloud()
	h.Endpoint = srv.URL
	cfg := Config{Fields: map[string]string{
		"sender": "cnSender", "sign_name": "签名", "template_id": "CN_T",
		"global_sender": "8819", "global_template_id": "GL_T",
	}}
	err := h.Send(context.Background(), cfg,
		Secret{"app_key": "cnAK", "app_secret": "cnSK", "global_app_key": "glAK", "global_app_secret": "glSK"},
		Message{Phone: "61400123456", Code: "111111"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// 国际通道号/模板缺失必须明确报错，而不是拿国内配置硬发。
	// （app_key/secret 是账号级凭据，回退用国内的一套是合理设计。）
	cnOnly := Config{Fields: map[string]string{
		"sender": "cnSender", "sign_name": "签名", "template_id": "CN_T",
	}}
	err = h.Send(context.Background(), cnOnly, Secret{"app_key": "cnAK", "app_secret": "cnSK"},
		Message{Phone: "61400123456", Code: "111111"})
	if err == nil || !strings.Contains(err.Error(), "global_sender") {
		t.Fatalf("want international config error, got %v", err)
	}
}

func TestHuaweicloudApiError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"code":"E200015","description":"templateParams invalid"}`)
	}))
	defer srv.Close()

	h := NewHuaweicloud()
	h.Endpoint = srv.URL
	err := h.Send(context.Background(),
		Config{Fields: map[string]string{"sender": "s", "sign_name": "n", "template_id": "t"}},
		Secret{"app_key": "k", "app_secret": "sec"}, Message{Phone: "13800138000", Code: "1"})
	if err == nil || !strings.Contains(err.Error(), "E200015") {
		t.Fatalf("want api error surfaced, got %v", err)
	}
}

func TestHuaweicloudValidate(t *testing.T) {
	h := NewHuaweicloud()
	if err := h.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
	cfg := Config{Fields: map[string]string{"sender": "s", "template_id": "t"}}
	if err := h.Validate(cfg, Secret{"app_key": "k", "app_secret": "sec"}); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if _, ok := Get("huaweicloud"); !ok {
		t.Fatal("huaweicloud must be registered on package load")
	}
}
