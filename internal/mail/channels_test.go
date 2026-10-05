package mail

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func writeStr(w http.ResponseWriter, s string) { _, _ = io.WriteString(w, s) }

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// 阿里云邮件推送的签名必须可复算，并且换参数/换密钥都会变。
func TestAliMailSignature(t *testing.T) {
	params := map[string]string{
		"AccessKeyId":    "testid",
		"Action":         "SingleSendMail",
		"AccountName":    "noreply@example.com",
		"ToAddress":      "user@example.com",
		"SignatureNonce": "fixed",
		"Timestamp":      "2024-01-01T00:00:00Z",
	}
	sig := aliMailSignature("secret", params)
	if sig == "" || sig != aliMailSignature("secret", params) {
		t.Fatalf("aliMailSignature not deterministic: %q", sig)
	}
	if sig == aliMailSignature("other", params) {
		t.Fatal("different secret produced same signature")
	}
	params["ToAddress"] = "other@example.com"
	if aliMailSignature("secret", params) == sig {
		t.Fatal("signature did not change when a parameter changed")
	}
}

// 发送请求必须带 SingleSendMail 业务参数与签名。
func TestAlimailSend(t *testing.T) {
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = url.ParseQuery(readBody(r))
		w.Header().Set("Content-Type", "application/json")
		writeStr(w, `{"RequestId":"abc"}`)
	}))
	t.Cleanup(srv.Close)
	a := NewAlimail()
	a.Endpoint = srv.URL
	a.http = srv.Client()
	a.Random = func() string { return "fixednonce" }

	err := a.Send(context.Background(),
		Config{Fields: map[string]string{"account_name": "noreply@example.com", "from_alias": "ShitIDC"}},
		Secret{"access_key_id": "id", "access_key_secret": "secret"},
		Message{To: "user@example.com", Subject: "主题", HTML: "<p>hi</p>"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for k, want := range map[string]string{
		"Action":      "SingleSendMail",
		"AccountName": "noreply@example.com",
		"ToAddress":   "user@example.com",
		"Subject":     "主题",
		"HtmlBody":    "<p>hi</p>",
		"Version":     "2015-11-23",
	} {
		if captured.Get(k) != want {
			t.Fatalf("form[%s] = %q, want %q", k, captured.Get(k), want)
		}
	}
	if captured.Get("Signature") == "" {
		t.Fatal("missing Signature")
	}

	// 上游报错时错误信息要带出 Code 与 Message。
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeStr(w, `{"Code":"InvalidAccessKeyId.NotFound","Message":"not found"}`)
	}))
	t.Cleanup(errSrv.Close)
	a.Endpoint = errSrv.URL
	a.http = errSrv.Client()
	err = a.Send(context.Background(),
		Config{Fields: map[string]string{"account_name": "noreply@example.com"}},
		Secret{"access_key_id": "id", "access_key_secret": "secret"},
		Message{To: "user@example.com", Subject: "s", HTML: "b"})
	if err == nil || !strings.Contains(err.Error(), "InvalidAccessKeyId.NotFound") {
		t.Fatalf("expected code in error, got %v", err)
	}
}

// 赛邮邮件：成功看 status=="success"，失败时错误码要翻成人话。
func TestSubemailSend(t *testing.T) {
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = url.ParseQuery(readBody(r))
		writeStr(w, `{"status":"success"}`)
	}))
	t.Cleanup(srv.Close)
	s := NewSubemail()
	s.APIBase = srv.URL + "/"

	err := s.Send(context.Background(),
		Config{Fields: map[string]string{"app_id": "1234", "from_address": "noreply@example.com", "from_name": "ShitIDC"}},
		Secret{"app_key": "key"},
		Message{To: "user@example.com", Subject: "主题", HTML: "<p>hi</p>"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for k, want := range map[string]string{
		"appid": "1234", "from": "noreply@example.com", "from_name": "ShitIDC",
		"signature": "key", "to": "user@example.com", "subject": "主题", "html": "<p>hi</p>",
	} {
		if captured.Get(k) != want {
			t.Fatalf("form[%s] = %q, want %q", k, captured.Get(k), want)
		}
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"status":"error","code":"205","msg":""}`)
	}))
	t.Cleanup(errSrv.Close)
	s.APIBase = errSrv.URL + "/"
	err = s.Send(context.Background(),
		Config{Fields: map[string]string{"app_id": "1234", "from_address": "noreply@example.com"}},
		Secret{"app_key": "key"}, Message{To: "u@example.com", Subject: "s", HTML: "b"})
	if err == nil || !strings.Contains(err.Error(), "错误的发件人地址") {
		t.Fatalf("expected translated code error, got %v", err)
	}
}

// 宝塔邮局：status 必须是布尔 true；默认跳过自签证书校验。
func TestBtmailSend(t *testing.T) {
	var captured url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = url.ParseQuery(readBody(r))
		writeStr(w, `{"status":true,"msg":"ok"}`)
	}))
	t.Cleanup(srv.Close)
	b := NewBtmail()
	err := b.Send(context.Background(),
		Config{Fields: map[string]string{"host": srv.URL, "from_address": "noreply@example.com"}},
		Secret{"password": "pw"},
		Message{To: "user@example.com", Subject: "主题", HTML: "<p>hi</p>"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for k, want := range map[string]string{
		"mail_from": "noreply@example.com", "password": "pw", "mail_to": "user@example.com",
		"subtype": "html", "subject": "主题", "content": "<p>hi</p>",
	} {
		if captured.Get(k) != want {
			t.Fatalf("form[%s] = %q, want %q", k, captured.Get(k), want)
		}
	}

	// status=false 必须报错并把 msg 带出来。
	badSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStr(w, `{"status":false,"msg":"auth failed"}`)
	}))
	t.Cleanup(badSrv.Close)
	err = b.Send(context.Background(),
		Config{Fields: map[string]string{"host": badSrv.URL, "from_address": "noreply@example.com"}},
		Secret{"password": "pw"}, Message{To: "u@example.com", Subject: "s", HTML: "b"})
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected msg in error, got %v", err)
	}
}

// 通用 HTTP 通道：默认表单模板、secret 占位符替换、GET 走 query。
func TestGenericMailSend(t *testing.T) {
	var capturedForm url.Values
	var capturedQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query()
		capturedForm, _ = url.ParseQuery(readBody(r))
		writeStr(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	g := NewGenericHTTP()

	err := g.Send(context.Background(),
		Config{Fields: map[string]string{"endpoint": srv.URL, "body_template": "to={{to}}&subject={{subject}}&body={{body}}&token={{secret:token}}"}},
		Secret{"token": "t0ken"},
		Message{To: "user@example.com", Subject: "主题", HTML: "<p>hi</p>"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if capturedForm.Get("to") != "user@example.com" || capturedForm.Get("token") != "t0ken" {
		t.Fatalf("unexpected form: %v", capturedForm)
	}

	err = g.Send(context.Background(),
		Config{Fields: map[string]string{"endpoint": srv.URL + "/hook", "method": "GET", "body_template": "to={{to}}&subject={{subject}}"}},
		Secret{},
		Message{To: "user@example.com", Subject: "主题", HTML: "b"})
	if err != nil {
		t.Fatalf("GET Send: %v", err)
	}
	if capturedQuery.Get("to") != "user@example.com" || capturedQuery.Get("subject") != "主题" {
		t.Fatalf("unexpected query: %v", capturedQuery)
	}

	// 未引用的 secret 占位符必须被清掉，不能把键名发出去。
	err = g.Send(context.Background(),
		Config{Fields: map[string]string{"endpoint": srv.URL, "body_template": "to={{to}}&x={{secret:missing}}"}},
		Secret{"token": "t0ken"},
		Message{To: "user@example.com", Subject: "s", HTML: "b"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if strings.Contains(capturedForm.Get("x"), "secret:") {
		t.Fatalf("unused secret placeholder leaked: %q", capturedForm.Get("x"))
	}
}

// 通道注册表必须包含本轮实现的四个通道。
func TestMailRegistryNames(t *testing.T) {
	want := map[string]bool{"alimail": false, "subemail": false, "btmail": false, "generic": false}
	for _, n := range Names() {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Fatalf("provider %q not registered", name)
		}
	}
}
