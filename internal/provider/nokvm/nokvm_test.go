package nokvm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/provider"
)

// 签名向量：独立按 PHP sort($data, SORT_STRING) 的语义复算——排序的是三个
// **值**（time/random/token 的字符串形态）而非键名，拼接后 MD5 大写。
// 这与宝塔模块的签名同源，但 random 是 6 位字母数字串而不是纯数字。
func TestSignVector(t *testing.T) {
	// values = ["1700000000", "Ab3xY9", "tok"] → 排序后 "1700000000Ab3xY9tok"
	got := sign("1700000000", "Ab3xY9", "tok")
	sum := md5.Sum([]byte("1700000000Ab3xY9tok"))
	if got != strings.ToUpper(hex.EncodeToString(sum[:])) {
		t.Fatalf("sign = %s", got)
	}
	// 随机串以小写字母开头时排序位置变化：["Ab3xY9" < "tok" < 大写?]
	// 再来一个 token 排在最前的用例（token 以数字开头时）。
	got = sign("1700000000", "Ab3xY9", "9token")
	sum = md5.Sum([]byte("17000000009tokenAb3xY9"))
	if got != strings.ToUpper(hex.EncodeToString(sum[:])) {
		t.Fatalf("sign (value-order case) = %s", got)
	}
}

// 独立复算签名（测试侧自己的实现，与被测代码不同构）。
func testSign(ts, rnd, token string) string {
	vals := []string{ts, rnd, token}
	sort.Strings(vals)
	sum := md5.Sum([]byte(strings.Join(vals, "")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// 开通全流程：POST /api/virtual，query 只有签名三元组，业务参数在 form 体；
// 主 IP / 附加 IP 按 ip_address_id 分流；win 镜像用户名是 administrator。
func TestCreateFlow(t *testing.T) {
	var gotQuery url.Values
	var gotForm url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_ = r.ParseForm()
		gotForm = r.PostForm
		if testSign(gotQuery.Get("time"), gotQuery.Get("random"), "TOKEN") != gotQuery.Get("signature") {
			t.Errorf("signature mismatch: %v", gotQuery)
		}
		w.Write([]byte(`{"code":0,"message":"ok","data":{"id":"66","name":"vm-66","ip_address_id":"2","public_ip":[{"id":"2","ip":"1.2.3.4"},{"id":"3","ip":"5.6.7.8"}]}}`))
	}))
	defer srv.Close()

	c, err := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "TOKEN", AllowPrivate: true}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	inst, err := c.Create(context.Background(), provider.CreateRequest{
		RequestID: "svc1", UserID: 42,
		Options: map[string]any{
			"email": "user@example.com",
			"configoptions": map[string]any{
				"CPU": float64(2), "Memory": "1024", "Disk Space": "20",
				"os": "win2019", "net_out": "256", "net_in": "256",
			},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if gotPath != "/api/virtual" {
		t.Fatalf("path = %s", gotPath)
	}
	// query 里不允许出现业务参数（只允许签名三元组）。
	for k := range gotQuery {
		switch k {
		case "time", "random", "signature":
		default:
			t.Errorf("unexpected query param %s=%s", k, gotQuery.Get(k))
		}
	}
	if gotForm.Get("core") != "2" || gotForm.Get("memory") != "1024" || gotForm.Get("data_disk_size") != "20" {
		t.Fatalf("form config wrong: %v", gotForm)
	}
	if gotForm.Get("users_id") != "42" || gotForm.Get("username") != "user@example.com" {
		t.Fatalf("user fields wrong: %v", gotForm)
	}
	if gotForm.Get("expire_time") != "2999-01-01 00:00:00" {
		t.Fatalf("expire_time = %s", gotForm.Get("expire_time"))
	}
	if inst.ID != "66" {
		t.Fatalf("instance id = %s", inst.ID)
	}
	if inst.Data["main_ip"] != "1.2.3.4" {
		t.Fatalf("main ip = %v", inst.Data["main_ip"])
	}
	if ips, ok := inst.Data["assigned_ips"].([]string); !ok || len(ips) != 1 || ips[0] != "5.6.7.8" {
		t.Fatalf("assigned ips = %v", inst.Data["assigned_ips"])
	}
	if inst.Data["username"] != "administrator" {
		t.Fatalf("win image username = %v", inst.Data["username"])
	}
	if pw := inst.Data["password"].(string); len(pw) != 8 {
		t.Fatalf("sys_pwd = %v", inst.Data["password"])
	}
}

// 非 win 镜像用户名 root。
func TestCreateLinuxUsername(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"ok","data":{"id":"7","ip_address_id":"1","public_ip":[{"id":"1","ip":"9.9.9.9"}]}}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())
	inst, err := c.Create(context.Background(), provider.CreateRequest{
		UserID:  1,
		Options: map[string]any{"configoptions": map[string]any{"os": "debian-12"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Data["username"] != "root" {
		t.Fatalf("username = %v", inst.Data["username"])
	}
}

// 生命周期端点：暂停 / 恢复 / 删除 / 连通测试。
func TestLifecycleEndpoints(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"code":0,"message":"ok","data":{}}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())

	if err := c.Suspend(context.Background(), "66"); err != nil || gotPath != "/api/virtual_pause/66" || gotMethod != http.MethodGet {
		t.Fatalf("suspend: %v %s %s", err, gotMethod, gotPath)
	}
	if err := c.Unsuspend(context.Background(), "66"); err != nil || gotPath != "/api/virtual_restore_pause/66" {
		t.Fatalf("unsuspend: %v %s", err, gotPath)
	}
	if err := c.Terminate(context.Background(), "66"); err != nil || gotPath != "/api/virtual/66" || gotMethod != http.MethodDelete {
		t.Fatalf("terminate: %v %s %s", err, gotMethod, gotPath)
	}
	if err := c.TestConnection(context.Background()); err != nil || gotPath != "/api/area" {
		t.Fatalf("test connection: %v %s", err, gotPath)
	}
}

// 业务错误：code != 0 必须带出 message。
func TestBusinessErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":1,"message":"库存不足","data":null}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())
	_, err := c.Create(context.Background(), provider.CreateRequest{UserID: 1})
	if err == nil || !strings.Contains(err.Error(), "库存不足") {
		t.Fatalf("want business error surfaced, got %v", err)
	}
}

// 改配：只提交 SelectionsJSON 里出现的键，走 PUT。
func TestChangePackage(t *testing.T) {
	var gotMethod, gotPath string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Write([]byte(`{"code":0,"message":"ok","data":{}}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())

	err := c.ChangePackage(context.Background(), provider.ChangePackageRequest{
		InstanceID:     "66",
		SelectionsJSON: map[string]any{"CPU": "4", "Memory": "2048"},
	})
	if err != nil || gotPath != "/api/virtual/66" || gotMethod != http.MethodPut {
		t.Fatalf("change package: %v %s %s", err, gotMethod, gotPath)
	}
	if gotForm.Get("core") != "4" || gotForm.Get("memory") != "2048" {
		t.Fatalf("form = %v", gotForm)
	}
	if gotForm.Get("data_disk_size") != "" {
		t.Fatal("未选择的键不应提交")
	}
	// 什么都不改：明确返回不支持而不是发空请求。
	err = c.ChangePackage(context.Background(), provider.ChangePackageRequest{InstanceID: "66", SelectionsJSON: map[string]any{}})
	if err != provider.ErrChangePackageUnsupported {
		t.Fatalf("empty change = %v", err)
	}
}
