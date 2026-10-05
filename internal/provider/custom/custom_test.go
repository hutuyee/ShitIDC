package custom

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/zjmfimport"
)

// specBthostsLike 按转换器对 bthosts 的输出手写等价规格。
func specBthostsLike() zjmfimport.Spec {
	return zjmfimport.Spec{
		SpecVersion: 1, Slug: "bt", Source: zjmfimport.SourceZJMF,
		Auth: zjmfimport.Auth{
			Scheme: zjmfimport.SchemeMD5SortUpper, TokenSource: zjmfimport.TokenAccessHash,
			TimeParam: "time", RandomParam: "random", SignatureParam: "signature",
			Placement: zjmfimport.PlacementForm,
		},
		Success: zjmfimport.Success{Field: "code", Equals: "1", MessageField: "msg"},
		Actions: map[string]zjmfimport.Action{
			zjmfimport.ActionTest:      {Method: "GET", Path: "/api/vhost/index", InstanceIDParam: "id"},
			zjmfimport.ActionCreate:    {Method: "POST", Path: "/api/vhost/user_create", InstanceIDPath: "data.site.id", Body: map[string]string{"username": "{{domain}}", "pack[flow_max]": "{{opt_flow_max}}", "client_id": "{{uid}}"}},
			zjmfimport.ActionSuspend:   {Method: "POST", Path: "/api/vhost/host_locked", InstanceIDParam: "id"},
			zjmfimport.ActionRenew:     {Method: "POST", Path: "/api/vhost/host_endtime", Body: map[string]string{"endtime": "{{expire_date}}"}, InstanceIDParam: "id"},
			zjmfimport.ActionTerminate: {Method: "POST", Path: "/api/vhost/host_recycle", InstanceIDParam: "id"},
		},
	}
}

func fixedClock(t *testing.T, opt *Options) {
	t.Helper()
	opt.Now = func() time.Time { return time.Unix(1710000000, 0) }
	opt.Random = func() string { return "12345" }
}

func md5UpperRef(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func newProvider(t *testing.T, spec zjmfimport.Spec, baseURL string) *Provider {
	t.Helper()
	opt := Options{BaseURL: baseURL, Token: "SECRET", AllowPrivate: true}
	fixedClock(t, &opt)
	// SafeHTTPClient 无条件拦截环回地址，httptest 服务器必须用普通客户端。
	opt.Client = &http.Client{Timeout: 10 * time.Second}
	p, err := New(spec, opt)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestSignatureMD5SortUpperForm(t *testing.T) {
	var gotForm url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotForm = r.PostForm
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"code":1,"data":{"site":{"id":"42"}}}`)
	}))
	defer srv.Close()

	p := newProvider(t, specBthostsLike(), srv.URL)
	inst, err := p.Create(context.Background(), provider.CreateRequest{
		RequestID: "svc-1", UserID: 7,
		Options: map[string]any{"domain": "web1.example.com", "configoptions": map[string]string{"flow_max": "20"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inst.ID != "42" {
		t.Errorf("instance id = %q", inst.ID)
	}
	if gotPath != "/api/vhost/user_create" {
		t.Errorf("path = %q", gotPath)
	}
	// 签名：sort(["1710000000","12345","SECRET"]) → "12345"+"1710000000"+"SECRET"
	wantSig := md5UpperRef("12345" + "1710000000" + "SECRET")
	if gotForm.Get("signature") != wantSig {
		t.Errorf("signature = %q want %q", gotForm.Get("signature"), wantSig)
	}
	if gotForm.Get("time") != "1710000000" || gotForm.Get("random") != "12345" {
		t.Errorf("time/random = %v", gotForm)
	}
	if gotForm.Get("username") != "web1.example.com" || gotForm.Get("pack[flow_max]") != "20" || gotForm.Get("client_id") != "7" {
		t.Errorf("body = %v", gotForm)
	}
	// token 不能随请求发送
	if gotForm.Get("token") != "" {
		t.Errorf("token leaked in form: %v", gotForm)
	}
}

func TestCreateFailureMessagePassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"主机名重复"}`)
	}))
	defer srv.Close()
	p := newProvider(t, specBthostsLike(), srv.URL)
	_, err := p.Create(context.Background(), provider.CreateRequest{UserID: 1})
	if err == nil || !strings.Contains(err.Error(), "主机名重复") {
		t.Fatalf("err = %v", err)
	}
}

func TestSuccessNumericTolerance(t *testing.T) {
	// 上游把 code 发成字符串 "1" 也要认
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":"1"}`)
	}))
	defer srv.Close()
	p := newProvider(t, specBthostsLike(), srv.URL)
	if err := p.Suspend(context.Background(), "42"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
}

func TestLifecycleCarriesInstanceID(t *testing.T) {
	var gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotID = r.PostForm.Get("id")
		_, _ = io.WriteString(w, `{"code":1}`)
	}))
	defer srv.Close()
	p := newProvider(t, specBthostsLike(), srv.URL)
	if err := p.Terminate(context.Background(), "42"); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if gotID != "42" {
		t.Errorf("id param = %q", gotID)
	}
}

func TestRenewSendsExpireDate(t *testing.T) {
	var gotEndtime string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotEndtime = r.PostForm.Get("endtime")
		_, _ = io.WriteString(w, `{"code":1}`)
	}))
	defer srv.Close()
	p := newProvider(t, specBthostsLike(), srv.URL)
	exp := time.Unix(1710000000, 0)
	if err := p.Renew(context.Background(), provider.RenewRequest{InstanceID: "42", ExpiresAt: exp}); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if want := exp.Format("2006-01-02"); gotEndtime != want {
		t.Errorf("endtime = %q want %q", gotEndtime, want)
	}
}

func TestMissingActionsAreHonestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("should not call upstream") }))
	defer srv.Close()
	spec := specBthostsLike()
	delete(spec.Actions, zjmfimport.ActionSuspend)
	p := newProvider(t, spec, srv.URL)
	if err := p.Suspend(context.Background(), "42"); err == nil || !strings.Contains(err.Error(), "suspend") {
		t.Fatalf("err = %v", err)
	}
}

func TestChangePackageUnsupportedWithoutAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("should not call upstream") }))
	defer srv.Close()
	p := newProvider(t, specBthostsLike(), srv.URL)
	if err := p.ChangePackage(context.Background(), provider.ChangePackageRequest{InstanceID: "42"}); err != provider.ErrChangePackageUnsupported {
		t.Fatalf("err = %v", err)
	}
}

func TestChangePackageSendsSelections(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotForm = r.PostForm
		_, _ = io.WriteString(w, `{"code":1}`)
	}))
	defer srv.Close()
	spec := specBthostsLike()
	spec.Actions[zjmfimport.ActionChangePackage] = zjmfimport.Action{
		Method: "POST", Path: "/api/vhost/host_update",
		Body: map[string]string{"site_max": "{{opt_site_max}}"},
	}
	p := newProvider(t, spec, srv.URL)
	err := p.ChangePackage(context.Background(), provider.ChangePackageRequest{
		InstanceID:     "42",
		SelectionsJSON: map[string]any{"configoptions": map[string]any{"site_max": "50"}},
	})
	if err != nil {
		t.Fatalf("change package: %v", err)
	}
	if gotForm.Get("site_max") != "50" || gotForm.Get("id") != "42" {
		t.Errorf("form = %v", gotForm)
	}
}

func TestMD5ConcatQueryScheme(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = io.WriteString(w, `{"result":200}`)
	}))
	defer srv.Close()

	spec := zjmfimport.Spec{
		SpecVersion: 1, Slug: "ka", Source: zjmfimport.SourceZJMF,
		Auth: zjmfimport.Auth{
			Scheme: zjmfimport.SchemeMD5Concat, ActionParam: "a", RandomParam: "r", SigParam: "s",
			ExtraQuery: map[string]string{"json": "1"},
		},
		Success: zjmfimport.Success{Field: "result", Equals: "200"},
		Actions: map[string]zjmfimport.Action{
			zjmfimport.ActionTest: {Method: "GET", Path: "info"},
		},
	}
	p := newProvider(t, spec, srv.URL)
	if err := p.TestConnection(context.Background()); err != nil {
		t.Fatalf("test: %v", err)
	}
	if gotQuery.Get("a") != "info" || gotQuery.Get("json") != "1" {
		t.Errorf("query = %v", gotQuery)
	}
	// s = md5("info" + "SECRET" + "12345") 大写
	if want := md5UpperRef("info" + "SECRET" + "12345"); gotQuery.Get("s") != want {
		t.Errorf("s = %q want %q", gotQuery.Get("s"), want)
	}
}

func TestQueryPlacementSignature(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer srv.Close()
	spec := zjmfimport.Spec{
		Slug: "nk", Source: zjmfimport.SourceZJMF,
		Auth: zjmfimport.Auth{
			Scheme: zjmfimport.SchemeMD5SortUpper, TokenSource: zjmfimport.TokenServerPasswd,
			TimeParam: "timeStamp", RandomParam: "randomStr", SignatureParam: "signature",
			Placement: zjmfimport.PlacementQuery,
		},
		Success: zjmfimport.Success{Field: "code", Equals: "0", MessageField: "message"},
		Actions: map[string]zjmfimport.Action{
			zjmfimport.ActionTest: {Method: "GET", Path: "/api/virtual/ping"},
		},
	}
	p := newProvider(t, spec, srv.URL)
	if err := p.TestConnection(context.Background()); err != nil {
		t.Fatalf("test: %v", err)
	}
	if gotQuery.Get("timeStamp") != "1710000000" || gotQuery.Get("randomStr") != "12345" {
		t.Errorf("query = %v", gotQuery)
	}
	if want := md5UpperRef("12345" + "1710000000" + "SECRET"); gotQuery.Get("signature") != want {
		t.Errorf("signature = %q", gotQuery.Get("signature"))
	}
}

func TestRejectsUnsupportedSchemeAndEmptyConfig(t *testing.T) {
	spec := specBthostsLike()
	spec.Auth.Scheme = zjmfimport.SchemeUnsupported
	if _, err := New(spec, Options{BaseURL: "https://x", Token: "t"}); err == nil {
		t.Fatal("expected error for unsupported scheme with actions")
	}
	if _, err := New(specBthostsLike(), Options{BaseURL: "", Token: "t"}); err == nil {
		t.Fatal("expected error for empty base url")
	}
	if _, err := New(specBthostsLike(), Options{BaseURL: "https://x", Token: ""}); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestExpandHandlesSpacesInKeys(t *testing.T) {
	got := expand("{{opt_Disk Space}}-x", map[string]string{"opt_Disk Space": "10"})
	if got != "10-x" {
		t.Errorf("expand = %q", got)
	}
	if v := expand("{{missing}}", map[string]string{}); v != "" {
		t.Errorf("missing var should expand to empty, got %q", v)
	}
}

func TestVarsFromCreateMapAnyShapes(t *testing.T) {
	vars := varsFromCreate(provider.CreateRequest{
		UserID: 9,
		Options: map[string]any{
			"domain":         "a.example.com",
			"configoptions":  map[string]any{"CPU": "2", "Memory": float64(2048)},
			"opt_disk_space": "20",
		},
	})
	if vars["opt_CPU"] != "2" || vars["opt_Memory"] != "2048" || vars["opt_disk_space"] != "20" {
		t.Errorf("vars = %v", vars)
	}
	if vars["uid"] != "9" || vars["domain"] != "a.example.com" {
		t.Errorf("identity = %v", vars)
	}
}

func TestJSONStringAtNumberForms(t *testing.T) {
	payload := map[string]any{"a": float64(42), "b": float64(1.5)}
	if v := jsonStringAt(payload, "a"); v != "42" {
		t.Errorf("a = %q", v)
	}
	if v := jsonStringAt(payload, "b"); v != "1.5" {
		t.Errorf("b = %q", v)
	}
	if v := jsonStringAt(payload, "nope"); v != "" {
		t.Errorf("missing = %q", v)
	}
	_ = json.Marshal // keep import if refactors drop usage
}
