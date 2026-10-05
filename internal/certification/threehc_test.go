package certification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestThreehcVerify(t *testing.T) {
	cases := []struct {
		name      string
		cfgType   string
		extra     map[string]string
		body      string
		wantMatch bool
	}{
		{"四要素一致", "4", map[string]string{"bank": "6222020202020202", "phone": "13800138000"}, `{"ret":200,"msg":"ok","log_id":"L1","data":{"desc":"一致"}}`, true},
		{"三要素不一致", "3", map[string]string{"bank": "6222020202020202"}, `{"ret":200,"data":{"desc":"不一致"}}`, false},
	}
	for _, tc := range cases {
		captured := map[string]string{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured["path"] = r.URL.Path
			for k, v := range r.URL.Query() {
				captured[k] = v[0]
			}
			if got := r.Header.Get("Authorization"); got != "APPCODE CODE1" {
				t.Errorf("%s: authorization = %q", tc.name, got)
			}
			_, _ = w.Write([]byte(tc.body))
		}))
		impl := NewThreehc()
		impl.Endpoint = srv.URL
		res, err := impl.Verify(context.Background(), Config{Fields: map[string]string{"type": tc.cfgType}}, Secret{"app_code": "CODE1"},
			Subject{RealName: "张三", IDNumber: "110101199003077574", Extra: tc.extra})
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Match != tc.wantMatch {
			t.Fatalf("%s: result = %+v", tc.name, res)
		}
		if captured["path"] != "/cert/bank-card/"+tc.cfgType {
			t.Errorf("%s: path = %q", tc.name, captured["path"])
		}
		if captured["bank"] != tc.extra["bank"] || captured["name"] != "张三" {
			t.Errorf("%s: query = %v", tc.name, captured)
		}
		if tc.cfgType == "4" && (captured["number"] != "110101199003077574" || captured["mobile"] != tc.extra["phone"]) {
			t.Errorf("%s: query = %v", tc.name, captured)
		}
		if tc.cfgType == "3" && captured["number"] == "" {
			t.Errorf("%s: number missing: %v", tc.name, captured)
		}
	}
}
func TestThreehcFieldsAndValidate(t *testing.T) {
	impl := NewThreehc()
	if got := impl.Fields(Config{Fields: map[string]string{"type": "2"}}); len(got) != 1 || got[0].Key != "bank" || !got[0].Required {
		t.Fatalf("fields(type=2) = %+v", got)
	}
	if got := impl.Fields(Config{Fields: map[string]string{"type": "4"}}); len(got) != 2 || got[1].Key != "phone" {
		t.Fatalf("fields(type=4) = %+v", got)
	}
	if err := impl.Validate(Config{Fields: map[string]string{"type": "5"}}, Secret{"app_code": "c"}); err == nil {
		t.Fatal("type=5 must be rejected")
	}
	if err := impl.Validate(Config{}, Secret{}); err == nil {
		t.Fatal("missing app_code must be rejected")
	}
	if _, ok := Get("threehc"); !ok {
		t.Fatal("threehc must be registered")
	}
}
