package baota

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/provider"
)

// 签名必须与魔方模块逐字节一致，否则面板会拒绝所有请求。
// 这里把 PHP 的写法原样转写一遍做对照：
//
//	$data = [$time, $random, $token];
//	sort($data, SORT_STRING);
//	return strtoupper(md5(implode($data)));
func phpReferenceSign(ts, rnd int64, token string) string {
	values := []string{strconvFormat(ts), strconvFormat(rnd), token}
	sortStrings(values)
	return md5Upper(strings.Join(values, ""))
}

func TestSignMatchesPHPModule(t *testing.T) {
	cases := []struct {
		ts    int64
		rnd   int64
		token string
	}{
		{1791082385, 123456789, "abcdef0123456789"},
		{1791082385, 7, "0"},
		{9, 10, "tok"}, // 字符串排序下 "10" < "9"，数值排序会算错
		{1, 2147483647, "x9y8z7"},
		{1700000000, 1, "TOKEN-With-Dashes"},
	}
	for _, tc := range cases {
		if got, want := sign(tc.ts, tc.rnd, tc.token), phpReferenceSign(tc.ts, tc.rnd, tc.token); got != want {
			t.Fatalf("sign(%d,%d,%q) = %s, want PHP reference %s", tc.ts, tc.rnd, tc.token, got, want)
		}
	}
}

func TestSignIsUppercaseMD5(t *testing.T) {
	got := sign(1, 2, "token")
	if got != strings.ToUpper(got) {
		t.Fatalf("signature %s is not upper-case", got)
	}
	if len(got) != 32 {
		t.Fatalf("signature %s is not a 32-char md5 digest", got)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{APIKey: "k"}); err == nil {
		t.Fatal("missing base_url must be rejected")
	}
	if _, err := New(Config{BaseURL: "https://panel.example.com"}); err == nil {
		t.Fatal("missing API key must be rejected")
	}
	// 注入一个假的 HTTP 客户端：SSRF 防护会拦截回环地址，所以不能用真网络。
	c, err := NewWithHTTPClient(
		Config{BaseURL: "http://127.0.0.1:8888/", APIKey: "k", AllowPrivate: true},
		&http.Client{},
	)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if c.cfg.BaseURL != "http://127.0.0.1:8888" {
		t.Fatalf("base url not normalised: %q", c.cfg.BaseURL)
	}
	if c.cfg.SiteType != "PHP" || c.cfg.SitePath != "/www/wwwroot" {
		t.Fatalf("defaults not applied: %+v", c.cfg)
	}
}

func TestSiteNameIsSafe(t *testing.T) {
	name := siteName(provider.CreateRequest{RequestID: "a1b2-c3d4-e5f6-7890"})
	if strings.ContainsAny(name, "-_ ") {
		t.Fatalf("site name %q contains characters panels reject", name)
	}
	if !strings.HasSuffix(name, ".shitidc.local") {
		t.Fatalf("site name %q lost its suffix", name)
	}
	// 纯符号的 ID 不能生成空站点名。
	if got := siteName(provider.CreateRequest{RequestID: "----"}); got == "" || !strings.Contains(got, ".") {
		t.Fatalf("empty-ish request id produced %q", got)
	}
}

func TestRandomPasswordComplexity(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		pw := randomPassword()
		if len(pw) != 16 {
			t.Fatalf("password length = %d, want 16", len(pw))
		}
		if seen[pw] {
			t.Fatal("randomPassword returned a duplicate")
		}
		seen[pw] = true
	}
}

func TestChangePackageUnsupported(t *testing.T) {
	c := &Client{cfg: Config{BaseURL: "http://127.0.0.1:8888", APIKey: "k", AllowPrivate: true}}
	if err := c.ChangePackage(context.Background(), provider.ChangePackageRequest{}); err != provider.ErrChangePackageUnsupported {
		t.Fatalf("baota cannot change packages; want ErrChangePackageUnsupported, got %v", err)
	}
}

// 测试内的极小工具函数，避免为了对照 PHP 再引一遍标准库。
func strconvFormat(v int64) string { return strconv.FormatInt(v, 10) }
func sortStrings(s []string)       { sort.Strings(s) }
func md5Upper(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// captureRoundTripper 记录出站请求并返回准备好的响应。
type captureRoundTripper struct {
	lastReq  *http.Request
	lastBody string
	respond  func(*http.Request) string
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	c.lastBody = string(body)
	c.lastReq = req
	payload := c.respond(req)
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

// 出站请求必须带上符合宝塔算法要求的 time / random / signature 三个参数。
func TestCallSignsOutboundRequest(t *testing.T) {
	rt := &captureRoundTripper{respond: func(*http.Request) string {
		return `{"code":1,"msg":"ok","data":{"siteId":"42"}}`
	}}
	c, err := NewWithHTTPClient(
		Config{BaseURL: "http://127.0.0.1:8888", APIKey: "panel-secret-key", AllowPrivate: true},
		&http.Client{Transport: rt},
	)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	data, err := c.call(context.Background(), "sites?action=GetSiteList", map[string]string{"page": "1"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := firstStringValue(data, "siteId"); got != "42" {
		t.Fatalf("parsed siteId = %q, want 42", got)
	}
	if rt.lastReq == nil {
		t.Fatal("no request was sent")
	}
	if ct := rt.lastReq.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Fatalf("content type = %q, want form encoding", ct)
	}
	form, err := url.ParseQuery(rt.lastBody)
	if err != nil {
		t.Fatalf("request body is not a form: %v", err)
	}
	ts := form.Get("time")
	rnd := form.Get("random")
	sig := form.Get("signature")
	if ts == "" || rnd == "" || sig == "" {
		t.Fatalf("missing auth fields in body %q", rt.lastBody)
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		t.Fatalf("time is not an integer: %v", err)
	}
	rndInt, err := strconv.ParseInt(rnd, 10, 64)
	if err != nil {
		t.Fatalf("random is not an integer: %v", err)
	}
	// 关键断言：签名必须能用同一把密钥重算出来。
	if want := sign(tsInt, rndInt, "panel-secret-key"); sig != want {
		t.Fatalf("signature %s does not verify against the panel key (want %s)", sig, want)
	}
	// 业务参数也要带上。
	if form.Get("page") != "1" {
		t.Fatalf("business parameter page lost: %q", rt.lastBody)
	}
}

// 面板返回失败码时必须报错，而不是把失败当成功。
func TestCallSurfacesPanelError(t *testing.T) {
	rt := &captureRoundTripper{respond: func(*http.Request) string {
		return `{"code":-1,"msg":"签名校验失败"}`
	}}
	c, _ := NewWithHTTPClient(
		Config{BaseURL: "http://127.0.0.1:8888", APIKey: "k", AllowPrivate: true},
		&http.Client{Transport: rt},
	)
	_, err := c.call(context.Background(), "sites?action=GetSiteList", nil)
	if err == nil {
		t.Fatal("a failed panel response must surface as an error")
	}
	if !strings.Contains(err.Error(), "签名校验失败") {
		t.Fatalf("error %q lost the panel message", err.Error())
	}
}

// code=0 与 code=1 都是宝塔的成功码，两种都要接受。
func TestCallAcceptsBothSuccessCodes(t *testing.T) {
	for _, payload := range []string{`{"code":0,"data":{"id":"1"}}`, `{"code":1,"data":{"id":"1"}}`} {
		rt := &captureRoundTripper{respond: func(*http.Request) string { return payload }}
		c, _ := NewWithHTTPClient(
			Config{BaseURL: "http://127.0.0.1:8888", APIKey: "k", AllowPrivate: true},
			&http.Client{Transport: rt},
		)
		if _, err := c.call(context.Background(), "sites?action=GetSiteList", nil); err != nil {
			t.Fatalf("payload %s rejected: %v", payload, err)
		}
	}
}

// Create 必须把面板返回的站点 ID 与生成的凭据交回去。
func TestCreateReturnsInstanceCredentials(t *testing.T) {
	rt := &captureRoundTripper{respond: func(*http.Request) string {
		return `{"code":1,"data":{"siteId":77}}`
	}}
	c, _ := NewWithHTTPClient(
		Config{BaseURL: "http://127.0.0.1:8888", APIKey: "k", AllowPrivate: true, CreateDB: true},
		&http.Client{Transport: rt},
	)
	inst, err := c.Create(context.Background(), provider.CreateRequest{RequestID: "abcd-1234-efgh"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inst.ID != "77" {
		t.Fatalf("instance id = %q, want 77", inst.ID)
	}
	if inst.Data["username"] == nil || inst.Data["password"] == nil {
		t.Fatalf("credentials missing from instance data: %+v", inst.Data)
	}
	form, _ := url.ParseQuery(rt.lastBody)
	if form.Get("webname") == "" {
		t.Fatalf("AddSite payload lacks webname: %q", rt.lastBody)
	}
	if form.Get("sql") != "true" {
		t.Fatalf("CreateDB was not forwarded: %q", form.Get("sql"))
	}
}

// 面板没返回站点 ID 时必须报错，否则后续暂停/删除都没有目标。
func TestCreateRequiresSiteID(t *testing.T) {
	rt := &captureRoundTripper{respond: func(*http.Request) string {
		return `{"code":1,"data":{}}`
	}}
	c, _ := NewWithHTTPClient(
		Config{BaseURL: "http://127.0.0.1:8888", APIKey: "k", AllowPrivate: true},
		&http.Client{Transport: rt},
	)
	if _, err := c.Create(context.Background(), provider.CreateRequest{RequestID: "abcd"}); err == nil {
		t.Fatal("a missing site id must be an error")
	}
}
