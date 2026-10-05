package certification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 判据对齐魔方 alitwo 插件：HTTP 200 且 status=="01" 一致；
// 其余状态码查到了但不匹配；HTTP 层错误报错。
func TestAlitwoVerifyMatch(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantMatch bool
		wantErr   bool
	}{
		{"一致", `{"status":"01","msg":"认证通过","traceId":"t1"}`, true, false},
		{"不一致", `{"status":"02","msg":"身份证与姓名不一致","traceId":"t2"}`, false, false},
		{"查无此人", `{"status":"03","msg":"无此身份证号"}`, false, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 请求形态必须与协议一致：GET + APPCODE 头 + query 参数。
			if r.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "APPCODE TESTCODE" {
				t.Errorf("authorization = %q", got)
			}
			if r.URL.Query().Get("idCard") == "" || r.URL.Query().Get("name") == "" {
				t.Errorf("missing query params: %v", r.URL.Query())
			}
			w.Write([]byte(tc.body))
		}))
		a := NewAlitwo()
		a.Endpoint = srv.URL
		res, err := a.Verify(context.Background(), Config{}, Secret{"app_code": "TESTCODE"},
			Subject{RealName: "张三", IDNumber: "110101199003077574"})
		srv.Close()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error", tc.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Match != tc.wantMatch {
			t.Fatalf("%s: match = %v, want %v", tc.name, res.Match, tc.wantMatch)
		}
	}
}

// HTTP 错误码必须翻译成人话（云市场网关分类）。
func TestAlitwoHTTPError(t *testing.T) {
	cases := map[int]string{
		400: "参数错误",
		403: "服务未授权",
		500: "API 网关错误",
	}
	for code, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{}`))
		}))
		a := NewAlitwo()
		a.Endpoint = srv.URL
		_, err := a.Verify(context.Background(), Config{}, Secret{"app_code": "C"},
			Subject{RealName: "张三", IDNumber: "110101199003077574"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("code %d: want error containing %q, got %v", code, want, err)
		}
	}
}

func TestAlitwoValidateAndRegistered(t *testing.T) {
	a := NewAlitwo()
	if err := a.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing app_code must be rejected")
	}
	if err := a.Validate(Config{}, Secret{"app_code": "C"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if _, ok := Get("alitwo"); !ok {
		t.Fatal("alitwo must be registered on package load")
	}
}
