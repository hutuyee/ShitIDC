package certification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWechatChallengeAndQuery(t *testing.T) {
	var payloads []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-TC-Version"); got != "2018-03-01" {
			t.Errorf("X-TC-Version = %q", got)
		}
		if got := r.Header.Get("Authorization"); !strings.Contains(got, "/faceid/tc3_request") {
			t.Errorf("authorization = %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		payloads = append(payloads, payload)
		switch r.Header.Get("X-TC-Action") {
		case "DetectAuth":
			if payload["IdCard"] == "" || payload["Name"] == "" || payload["RuleId"] == nil {
				t.Errorf("DetectAuth payload = %v", payload)
			}
			_, _ = w.Write([]byte(`{"Response":{"BizToken":"bt-1","Url":"https://faceid.example/?x=1&amp;y=2"}}`))
		case "GetDetectInfoEnhanced":
			if payload["BizToken"] != "bt-1" {
				t.Errorf("query payload = %v", payload)
			}
			if len(payloads) == 2 {
				_, _ = w.Write([]byte(`{"Response":{"BizToken":"bt-1"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"Response":{"Text":{"ErrCode":0,"ErrMsg":""}}}`))
		default:
			t.Errorf("unexpected action %q", r.Header.Get("X-TC-Action"))
		}
	}))
	defer srv.Close()

	impl := NewWechat()
	impl.Endpoint = strings.TrimPrefix(srv.URL, "https://")
	impl.http = srv.Client()
	cfg := Config{Fields: map[string]string{"rule_id": "12345"}}
	secret := Secret{"secret_id": "id", "secret_key": "key"}

	ch, err := impl.Challenge(context.Background(), cfg, secret, Subject{RealName: "张三", IDNumber: "110101199003077574"})
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if ch.Token != "bt-1" || ch.URL != "https://faceid.example/?x=1&y=2" {
		t.Fatalf("challenge = %+v", ch)
	}
	res, err := impl.Query(context.Background(), cfg, secret, ch.Token)
	if err != nil {
		t.Fatalf("query1: %v", err)
	}
	if !res.Pending || res.Match {
		t.Fatalf("missing Text must be pending, got %+v", res)
	}
	res, err = impl.Query(context.Background(), cfg, secret, ch.Token)
	if err != nil {
		t.Fatalf("query2: %v", err)
	}
	if !res.Match {
		t.Fatalf("query2 = %+v", res)
	}
}
func TestWechatValidateAndRegistered(t *testing.T) {
	impl := NewWechat()
	if err := impl.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing rule_id must be rejected")
	}
	if err := impl.Validate(Config{Fields: map[string]string{"rule_id": "1"}}, Secret{}); err == nil {
		t.Fatal("missing secrets must be rejected")
	}
	if _, err := impl.Verify(context.Background(), Config{}, Secret{}, Subject{}); err != ErrChallengeRequired {
		t.Fatalf("Verify err = %v", err)
	}
	if _, ok := Get("wechat"); !ok {
		t.Fatal("wechat must be registered")
	}
}
