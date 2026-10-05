package wlkangle

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/provider"
)

// 签名向量：s = md5(a + token + r)（与魔方 wlkanglepro_CreateSign 一致）。
func TestSignVector(t *testing.T) {
	sum := md5.Sum([]byte("add_vhSECRETK123456"))
	if got := createSign("add_vh", "SECRETK", "123456"); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("createSign = %s", got)
	}
}

// 开通全流程：query 带签名与业务参数，s 对 a+token+r 复算一致。
func TestCreateFlow(t *testing.T) {
	var gotQuery url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Write([]byte(`{"result":200,"msg":"ok"}`))
	}))
	defer srv.Close()

	c, err := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "SECRETK", AllowPrivate: true}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	inst, err := c.Create(context.Background(), provider.CreateRequest{
		RequestID: "SVC-abc123",
		Options: map[string]any{"configoptions": map[string]any{
			"web_quota": "2048", "db_quota": "512", "domain": "example.com",
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if gotPath != "/api/index.php" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotQuery.Get("c") != "whm" || gotQuery.Get("a") != "add_vh" || gotQuery.Get("json") != "1" {
		t.Fatalf("common params wrong: %v", gotQuery)
	}
	// 签名必须对 a + token + r 复算一致。
	if createSign("add_vh", "SECRETK", gotQuery.Get("r")) != gotQuery.Get("s") {
		t.Fatalf("signature mismatch: %v", gotQuery)
	}
	if gotQuery.Get("web_quota") != "2048" || gotQuery.Get("db_quota") != "512" || gotQuery.Get("domain") != "example.com" {
		t.Fatalf("business params wrong: %v", gotQuery)
	}
	// 未提供的键用默认值（与 ConfigOptions 的 default 一致）。
	if gotQuery.Get("module") != "php" || gotQuery.Get("ftp") != "1" {
		t.Fatalf("defaults wrong: %v", gotQuery)
	}
	if inst.ID == "" || inst.Data["password"] == "" {
		t.Fatalf("instance = %+v", inst)
	}
}

// 产品ID开通方式只带 product_id。
func TestCreateProductIDMode(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Write([]byte(`{"result":200}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())
	_, err := c.Create(context.Background(), provider.CreateRequest{
		RequestID: "svc1",
		Options:   map[string]any{"configoptions": map[string]any{"type": "1", "product_id": "9"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("product_id") != "9" {
		t.Fatalf("product_id = %s", gotQuery.Get("product_id"))
	}
	if gotQuery.Get("web_quota") != "" {
		t.Fatal("产品ID开通不应带配额参数")
	}
}

// 生命周期端点与续费=解除暂停。
func TestLifecycleEndpoints(t *testing.T) {
	var gotAction, gotStatus string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotAction = q.Get("a")
		gotStatus = q.Get("status")
		if createSign(gotAction, "T", q.Get("r")) != q.Get("s") {
			t.Errorf("signature mismatch for %s", gotAction)
		}
		w.Write([]byte(`{"result":200}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())

	if err := c.Suspend(context.Background(), "vh1"); err != nil || gotAction != "update_vh" || gotStatus != "1" {
		t.Fatalf("suspend: %v %s %s", err, gotAction, gotStatus)
	}
	if err := c.Unsuspend(context.Background(), "vh1"); err != nil || gotStatus != "0" {
		t.Fatalf("unsuspend: %v", err)
	}
	if err := c.Terminate(context.Background(), "vh1"); err != nil || gotAction != "del_vh" {
		t.Fatalf("terminate: %v", err)
	}
	if err := c.TestConnection(context.Background()); err != nil || gotAction != "info" {
		t.Fatalf("test: %v", err)
	}
	// 续费就是解除暂停（参考实现 _Renew 调 unsuspend）。
	if err := c.Renew(context.Background(), provider.RenewRequest{InstanceID: "vh1"}); err != nil || gotStatus != "0" {
		t.Fatalf("renew: %v", err)
	}
}

// 业务错误：result != 200 必须报错。
func TestBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":500,"msg":"主机名重复"}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())
	_, err := c.Create(context.Background(), provider.CreateRequest{RequestID: "dup"})
	if err == nil || !strings.Contains(err.Error(), "主机名重复") {
		t.Fatalf("want business error, got %v", err)
	}
}

// 改配走 add_vh + edit=1。
func TestChangePackage(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Write([]byte(`{"result":200}`))
	}))
	defer srv.Close()
	c, _ := NewWithHTTPClient(Config{BaseURL: srv.URL, Token: "T", AllowPrivate: true}, srv.Client())
	err := c.ChangePackage(context.Background(), provider.ChangePackageRequest{
		InstanceID:     "vh1",
		SelectionsJSON: map[string]any{"web_quota": "4096"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("edit") != "1" || gotQuery.Get("web_quota") != "4096" || gotQuery.Get("name") != "vh1" {
		t.Fatalf("change params: %v", gotQuery)
	}
}
