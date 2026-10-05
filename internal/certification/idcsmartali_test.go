package certification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdcsmartaliChallengeAndQuery(t *testing.T) {
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api") != "A1" || r.Header.Get("key") != "K1" {
			t.Errorf("headers = %q/%q", r.Header.Get("api"), r.Header.Get("key"))
		}
		_ = r.ParseForm()
		action := r.URL.Query().Get("action")
		actions = append(actions, action)
		switch action {
		case "initialize":
			if r.PostForm.Get("cert_name") != "张三" || r.PostForm.Get("cert_no") == "" {
				t.Errorf("initialize form = %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"status":200,"certify_id":"cid-9"}`))
		case "certify":
			if r.PostForm.Get("certify_id") != "cid-9" {
				t.Errorf("certify form = %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"status":200,"url":"https://qr.example/abc"}`))
		case "query":
			_, _ = w.Write([]byte(`{"status":4,"msg":"已提交资料"}`))
		default:
			t.Errorf("unexpected action %q", action)
		}
	}))
	defer srv.Close()

	i := NewIdcsmartali()
	i.Endpoint = srv.URL
	cfg := Config{}
	secret := Secret{"api": "A1", "key": "K1"}
	ch, err := i.Challenge(context.Background(), cfg, secret, Subject{RealName: "张三", IDNumber: "110101199003077574"})
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if ch.Token != "cid-9" || ch.URL != "https://qr.example/abc" {
		t.Fatalf("challenge = %+v", ch)
	}
	res, err := i.Query(context.Background(), cfg, secret, ch.Token)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// 关键纠偏：未完成（status=4）必须按「处理中」返回，不能判失败。
	if !res.Pending || res.Match {
		t.Fatalf("query result = %+v", res)
	}
	if strings.Join(actions, ",") != "initialize,certify,query" {
		t.Fatalf("actions = %v", actions)
	}
}
func TestIdcsmartaliQueryApprovedAndValidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"200","msg":"通过"}`))
	}))
	defer srv.Close()
	i := NewIdcsmartali()
	i.Endpoint = srv.URL
	res, err := i.Query(context.Background(), Config{}, Secret{"api": "a", "key": "k"}, "cid")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !res.Match {
		t.Fatalf("result = %+v", res)
	}
	if err := i.Validate(Config{}, Secret{"api": "a"}); err == nil {
		t.Fatal("missing key must be rejected")
	}
	if _, ok := Get("idcsmartali"); !ok {
		t.Fatal("idcsmartali must be registered")
	}
}

func TestIdcsmartaliChallengeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":500,"msg":"签名错误"}`))
	}))
	defer srv.Close()
	i := NewIdcsmartali()
	i.Endpoint = srv.URL
	if _, err := i.Challenge(context.Background(), Config{}, Secret{"api": "a", "key": "k"}, Subject{}); err == nil || !strings.Contains(err.Error(), "签名错误") {
		t.Fatalf("err = %v", err)
	}
}
