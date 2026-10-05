package certification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPhonethreeVerify(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantMatch bool
	}{
		{"一致", `{"code":200,"msg":"一致","ordersign":"O1"}`, true},
		{"不一致", `{"code":400,"msg":"不一致"}`, false},
	}
	for _, tc := range cases {
		var gotQuery map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = map[string]string{}
			for k, v := range r.URL.Query() {
				gotQuery[k] = v[0]
			}
			if got := r.Header.Get("Authorization"); got != "APPCODE CODE1" {
				t.Errorf("%s: authorization = %q", tc.name, got)
			}
			_, _ = w.Write([]byte(tc.body))
		}))
		impl := NewPhonethree()
		impl.Endpoint = srv.URL
		res, err := impl.Verify(context.Background(), Config{}, Secret{"app_code": "CODE1"},
			Subject{RealName: "张三", IDNumber: "110101199003077574", Extra: map[string]string{"phone": "13800138000"}})
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Match != tc.wantMatch {
			t.Fatalf("%s: result = %+v", tc.name, res)
		}
		if gotQuery["idcard"] != "110101199003077574" || gotQuery["phone"] != "13800138000" || gotQuery["realname"] != "张三" {
			t.Errorf("%s: query = %v", tc.name, gotQuery)
		}
		if tc.wantMatch && !strings.Contains(res.Message, "ordersign") {
			t.Errorf("%s: message = %q", tc.name, res.Message)
		}
	}
}
func TestPhonethreeFieldsAndErrors(t *testing.T) {
	impl := NewPhonethree()
	if got := impl.Fields(Config{}); len(got) != 1 || got[0].Key != "phone" || !got[0].Required {
		t.Fatalf("fields = %+v", got)
	}
	if err := impl.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing app_code must be rejected")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	impl.Endpoint = srv.URL
	if _, err := impl.Verify(context.Background(), Config{}, Secret{"app_code": "C"}, Subject{}); err == nil || !strings.Contains(err.Error(), "服务未授权") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := Get("phonethree"); !ok {
		t.Fatal("phonethree must be registered")
	}
}
