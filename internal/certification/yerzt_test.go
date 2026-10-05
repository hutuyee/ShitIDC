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

// 扫码流程：GetEidToken 拿 EidToken/Url；轮询先查状态，Text 没出现算处理中，
// ErrCode==0 才算通过（请求形态、TC3 签名管道与 wechat 同源）。
func TestYerztChallengeAndQuery(t *testing.T) {
	var actions []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-TC-Version"); got != "2018-03-01" {
			t.Errorf("X-TC-Version = %q", got)
		}
		if got := r.Header.Get("Authorization"); !strings.Contains(got, "/faceid/tc3_request") {
			t.Errorf("authorization = %q", got)
		}
		action := r.Header.Get("X-TC-Action")
		actions = append(actions, action)
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		switch action {
		case "GetEidToken":
			if payload["IdCard"] == "" || payload["Name"] == "" || payload["MerchantId"] != "m-1" {
				t.Errorf("GetEidToken payload = %v", payload)
			}
			cfgNode, _ := payload["Config"].(map[string]any)
			if cfgNode["InputType"] != "3" {
				t.Errorf("InputType = %v", cfgNode["InputType"])
			}
			_, _ = w.Write([]byte(`{"Response":{"EidToken":"eid-1","Url":"https://faceid.example/?a=1&amp;b=2"}}`))
		case "CheckEidTokenStatus":
			if payload["EidToken"] != "eid-1" {
				t.Errorf("status payload = %v", payload)
			}
			_, _ = w.Write([]byte(`{"Response":{"Status":"doing"}}`))
		case "GetEidResult":
			if payload["EidToken"] != "eid-1" || payload["InfoType"] != "0" {
				t.Errorf("result payload = %v", payload)
			}
			if len(actions) == 3 {
				_, _ = w.Write([]byte(`{"Response":{"EidToken":"eid-1"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"Response":{"Text":{"ErrCode":0,"LiveMsg":"成功"}}}`))
		default:
			t.Errorf("unexpected action %q", action)
		}
	}))
	defer srv.Close()

	impl := NewYerzt()
	impl.Endpoint = strings.TrimPrefix(srv.URL, "https://")
	impl.http = srv.Client()
	cfg := Config{Fields: map[string]string{"merchant_id": "m-1"}}
	secret := Secret{"secret_id": "id", "secret_key": "key"}

	ch, err := impl.Challenge(context.Background(), cfg, secret, Subject{RealName: "张三", IDNumber: "110101199003077574"})
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if ch.Provider != "yerzt" || ch.Token != "eid-1" || ch.URL != "https://faceid.example/?a=1&b=2" {
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

// 超时会话保持处理中（提示重新发起）；Text.ErrCode!=0 才判未通过。
func TestYerztTimeoutAndReject(t *testing.T) {
	timeoutSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Response":{"Status":"timeout"}}`))
	}))
	impl := NewYerzt()
	impl.Endpoint = strings.TrimPrefix(timeoutSrv.URL, "https://")
	impl.http = timeoutSrv.Client()
	cfg := Config{Fields: map[string]string{"merchant_id": "m-1"}}
	secret := Secret{"secret_id": "id", "secret_key": "key"}
	res, err := impl.Query(context.Background(), cfg, secret, "eid-1")
	timeoutSrv.Close()
	if err != nil {
		t.Fatalf("timeout query: %v", err)
	}
	if !res.Pending || !strings.Contains(res.Message, "超时") {
		t.Fatalf("timeout res = %+v", res)
	}

	rejectSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-TC-Action") {
		case "CheckEidTokenStatus":
			_, _ = w.Write([]byte(`{"Response":{"Status":"done"}}`))
		default:
			_, _ = w.Write([]byte(`{"Response":{"Text":{"ErrCode":1200,"ErrMsg":"人脸比对不通过"}}}`))
		}
	}))
	impl2 := NewYerzt()
	impl2.Endpoint = strings.TrimPrefix(rejectSrv.URL, "https://")
	impl2.http = rejectSrv.Client()
	res, err = impl2.Query(context.Background(), cfg, secret, "eid-1")
	rejectSrv.Close()
	if err != nil {
		t.Fatalf("reject query: %v", err)
	}
	if res.Match || res.Pending || !strings.Contains(res.Message, "不通过") {
		t.Fatalf("reject res = %+v", res)
	}
}

func TestYerztValidateAndRegistered(t *testing.T) {
	impl := NewYerzt()
	if err := impl.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing merchant_id must be rejected")
	}
	if err := impl.Validate(Config{Fields: map[string]string{"merchant_id": "m"}}, Secret{}); err == nil {
		t.Fatal("missing secrets must be rejected")
	}
	if err := impl.Validate(Config{Fields: map[string]string{"merchant_id": "m", "input_type": "9"}}, Secret{"secret_id": "i", "secret_key": "k"}); err == nil {
		t.Fatal("input_type=9 must be rejected")
	}
	if err := impl.Validate(Config{Fields: map[string]string{"merchant_id": "m"}}, Secret{"secret_id": "i", "secret_key": "k"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if _, err := impl.Verify(context.Background(), Config{}, Secret{}, Subject{}); err != ErrChallengeRequired {
		t.Fatalf("Verify err = %v", err)
	}
	if _, ok := Get("yerzt"); !ok {
		t.Fatal("yerzt must be registered")
	}
}
