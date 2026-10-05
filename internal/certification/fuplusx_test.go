package certification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 请求形态对齐插件：GET {base}{path}?idCard&name(&accountNo/mobile) + APPCODE 头，
// 要素类型决定资源路径与附带的参数。
func TestFuplusxPathsAndParams(t *testing.T) {
	cases := []struct {
		kind   string
		path   string
		extra  map[string]string
		wantQ  map[string]string
		absent []string
	}{
		{"2", "/IDCard", nil, nil, []string{"accountNo", "mobile"}},
		{"3", "/bankCheck", map[string]string{"bank": "6222020200"}, map[string]string{"accountNo": "6222020200"}, []string{"mobile"}},
		{"4", "/phoneCheck", map[string]string{"phone": "13800000000"}, map[string]string{"mobile": "13800000000"}, []string{"accountNo"}},
		{"5", "/bankCheck4", map[string]string{"bank": "6222020200", "phone": "13800000000"}, map[string]string{"accountNo": "6222020200", "mobile": "13800000000"}, nil},
	}
	for _, tc := range cases {
		var gotPath string
		var gotQ url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotQ = r.URL.Query()
			if got := r.Header.Get("Authorization"); got != "APPCODE TESTCODE" {
				t.Errorf("authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"01","msg":"认证通过","traceId":"tr-1"}`))
		}))
		f := NewFuplusx()
		f.Endpoint = srv.URL
		res, err := f.Verify(context.Background(),
			Config{Fields: map[string]string{"type": tc.kind}},
			Secret{"app_code": "TESTCODE"},
			Subject{RealName: "张三", IDNumber: "110101199003077574", Extra: tc.extra})
		srv.Close()
		if err != nil {
			t.Fatalf("type %s: %v", tc.kind, err)
		}
		if gotPath != tc.path {
			t.Fatalf("type %s: path = %s, want %s", tc.kind, gotPath, tc.path)
		}
		if gotQ.Get("idCard") == "" || gotQ.Get("name") != "张三" {
			t.Fatalf("type %s: query = %v", tc.kind, gotQ)
		}
		for k, v := range tc.wantQ {
			if gotQ.Get(k) != v {
				t.Fatalf("type %s: %s = %q, want %q", tc.kind, k, gotQ.Get(k), v)
			}
		}
		for _, k := range tc.absent {
			if gotQ.Get(k) != "" {
				t.Fatalf("type %s: %s must not be sent", tc.kind, k)
			}
		}
		if !res.Match {
			t.Fatalf("type %s: match = false", tc.kind)
		}
	}
}

// 判据：status!="01" 是查到了但不一致；HTTP 错误按云市场分类翻译成人话。
func TestFuplusxMismatchAndHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"02","msg":"身份证与姓名不一致"}`))
	}))
	f := NewFuplusx()
	f.Endpoint = srv.URL
	res, err := f.Verify(context.Background(), Config{}, Secret{"app_code": "C"},
		Subject{RealName: "张三", IDNumber: "110101199003077574"})
	srv.Close()
	if err != nil {
		t.Fatalf("mismatch must not error: %v", err)
	}
	if res.Match || !strings.Contains(res.Message, "不一致") {
		t.Fatalf("res = %+v", res)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	f2 := NewFuplusx()
	f2.Endpoint = bad.URL
	_, err = f2.Verify(context.Background(), Config{}, Secret{"app_code": "C"},
		Subject{RealName: "张三", IDNumber: "x"})
	bad.Close()
	if err == nil || !strings.Contains(err.Error(), "服务未授权") {
		t.Fatalf("403 error = %v", err)
	}
}

func TestFuplusxValidateFieldsAndRegistered(t *testing.T) {
	f := NewFuplusx()
	if err := f.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing app_code must be rejected")
	}
	if err := f.Validate(Config{Fields: map[string]string{"type": "9"}}, Secret{"app_code": "C"}); err == nil {
		t.Fatal("type=9 must be rejected")
	}
	if err := f.Validate(Config{Fields: map[string]string{"type": "5"}}, Secret{"app_code": "C"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if got := f.Fields(Config{Fields: map[string]string{"type": "2"}}); len(got) != 0 {
		t.Fatalf("type=2 fields = %v", got)
	}
	if got := f.Fields(Config{Fields: map[string]string{"type": "3"}}); len(got) != 1 || got[0].Key != "bank" {
		t.Fatalf("type=3 fields = %v", got)
	}
	if got := f.Fields(Config{Fields: map[string]string{"type": "4"}}); len(got) != 1 || got[0].Key != "phone" {
		t.Fatalf("type=4 fields = %v", got)
	}
	if got := f.Fields(Config{Fields: map[string]string{"type": "5"}}); len(got) != 2 || got[0].Key != "bank" || got[1].Key != "phone" {
		t.Fatalf("type=5 fields = %v", got)
	}
	if _, ok := Get("fuplusx"); !ok {
		t.Fatal("fuplusx must be registered")
	}
}
