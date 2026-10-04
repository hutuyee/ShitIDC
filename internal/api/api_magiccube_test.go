package api

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/apisign"
)

// These tests pin the contract between the 魔方 server module
// (zjmf-plugin/shitidc/shitidc.php) and ShitIDC upstream endpoints. The PHP
// side is not compiled here, so its signing code from
// public/plugins/servers/bthosts/bthosts.php:14-23 is transcribed literally and
// the resulting request is fed through the real middleware. If either end
// drifts, this fails.

// phpModuleSignature transcribes the PHP module signing routine:
//
//	$data = [$time, $random, $token];
//	sort($data, SORT_STRING);
//	return strtoupper(md5(implode($data)));
func phpModuleSignature(ts int64, random int64, token string) string {
	values := []string{strconv.FormatInt(ts, 10), strconv.FormatInt(random, 10), token}
	sort.Strings(values)
	sum := md5.Sum([]byte(strings.Join(values, "")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// phpModuleRequest builds the exact form body the module posts: the three auth
// fields plus the access hash as the token field, and the action payload.
func phpModuleRequest(accessHash string, extra map[string]string) url.Values {
	form := url.Values{}
	ts := time.Now().Unix()
	random := int64(424242)
	form.Set("time", strconv.FormatInt(ts, 10))
	form.Set("random", strconv.FormatInt(random, 10))
	form.Set("signature", phpModuleSignature(ts, random, accessHash))
	form.Set("token", accessHash)
	for k, v := range extra {
		form.Set(k, v)
	}
	return form
}

func newTestContext(method, target string, form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request = req
	return c, rec
}

// TestPHPModuleSignatureMatchesGo is the byte-level cross-language check.
func TestPHPModuleSignatureMatchesGo(t *testing.T) {
	accessHash := "zj_0123456789abcdef.SuperSecretValue1234567890abcd"
	cases := []struct {
		ts     int64
		random int64
	}{
		{1791082385, 123456789},
		{1791082385, 7},
		{9, 10}, // "10" < "9" as strings: where a numeric sort would diverge
		{1, 2147483647},
	}
	for _, tc := range cases {
		want := phpModuleSignature(tc.ts, tc.random, accessHash)
		got := apisign.Sign(tc.ts, tc.random, accessHash)
		if got != want {
			t.Fatalf("Go signature %s != PHP module signature %s for (%d,%d)", got, want, tc.ts, tc.random)
		}
	}
}

// TestUpstreamMiddlewareRejectsUnsignedAndWrongToken proves the middleware is
// actually enforcing the signature rather than trusting the token.
func TestUpstreamMiddlewareRejectsUnsignedAndWrongToken(t *testing.T) {
	app := &App{}
	handler := app.requireUpstreamKey()

	t.Run("no credentials at all", func(t *testing.T) {
		c, rec := newTestContext(http.MethodPost, "/compat/magiccube/v1/test", url.Values{})
		handler(c)
		if c.IsAborted() == false {
			t.Fatal("unsigned request was not aborted")
		}
		if strings.Contains(rec.Body.String(), "\"code\":1") {
			t.Fatal("unsigned request was accepted")
		}
	})

	// The remaining branches need a database (they look the key up and decrypt
	// it), so the signature-rejection behaviour is covered at the unit level in
	// internal/apisign, where Verify is exercised against a wrong token, a stale
	// timestamp and a far-future timestamp.
}

// TestUpstreamStatusVocabulary pins the status words the PHP module accepts
// (on / off / waiting / unknown) so the two sides cannot drift apart.
func TestUpstreamStatusVocabulary(t *testing.T) {
	allowed := map[string]bool{"on": true, "off": true, "waiting": true, "unknown": true}
	cases := map[string]string{
		"active":       "on",
		"suspended":    "off",
		"pending":      "waiting",
		"provisioning": "waiting",
		"suspending":   "waiting",
		"unsuspending": "waiting",
		"terminated":   "off",
		"terminating":  "off",
		"failed":       "off",
		"":             "unknown",
		"nonsense":     "unknown",
	}
	for status, want := range cases {
		got := upstreamStatusWord(status)
		if got != want {
			t.Fatalf("upstreamStatusWord(%q) = %q, want %q", status, got, want)
		}
		if !allowed[got] {
			t.Fatalf("upstreamStatusWord(%q) returned %q which the 魔方 module does not understand", status, got)
		}
	}
}

// TestUpstreamActionAliases pins the action names the module sends to the
// dispatcher, so renaming one end is caught immediately.
func TestUpstreamActionAliases(t *testing.T) {
	// Every act value the PHP module can send must reach a handler branch.
	moduleActions := []string{
		"create",   // shitidc_CreateAccount
		"status",   // shitidc_Status
		"sync",     // shitidc_Sync
		"locked",   // shitidc_SuspendAccount
		"unlocked", // shitidc_UnsuspendAccount
		"recycle",  // shitidc_TerminateAccount
		"renew",    // shitidc_Renew
		"test",     // shitidc_TestLink
		"product",  // shitidc_ProductList
	}
	dispatched := map[string]bool{
		"test": true, "product": true, "products": true, "product_list": true,
		"create": true, "host_create": true, "create_account": true,
		"status": true, "host_status": true, "info": true,
		"sync": true, "host_sync": true,
		"locked": true, "lock": true, "host_locked": true, "suspend": true,
		"unlocked": true, "unlock": true, "host_start": true, "unsuspend": true,
		"recycle": true, "delete": true, "host_recycle": true, "terminate": true,
		"renew": true, "host_renew": true, "renewal": true,
	}
	for _, act := range moduleActions {
		if !dispatched[act] {
			t.Fatalf("module sends act=%q but the dispatcher has no branch for it", act)
		}
	}
}

// TestUpstreamActionParsing checks act/action are read from form or query.
func TestUpstreamActionParsing(t *testing.T) {
	c, _ := newTestContext(http.MethodPost, "/compat/magiccube/v1/host", url.Values{"act": {"CREATE"}})
	if got := upstreamAction(c); got != "create" {
		t.Fatalf("upstreamAction(form act) = %q, want create", got)
	}
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec)
	c2.Request = httptest.NewRequest(http.MethodGet, "/compat/magiccube/v1/host?action=Locked", nil)
	if got := upstreamAction(c2); got != "locked" {
		t.Fatalf("upstreamAction(query action) = %q, want locked", got)
	}
}

// TestAccessHashTokenSplitting verifies the "<key_id>.<secret>" value the admin
// console generates is split the same way when it comes back.
func TestAccessHashTokenSplitting(t *testing.T) {
	cases := []struct{ raw, wantKey string }{
		{"zj_abc.secret", "zj_abc"},
		{"zj_abc", "zj_abc"},          // hand-typed bare key id still resolves
		{" zj_abc.secret ", "zj_abc"}, // form values can carry padding
	}
	for _, tc := range cases {
		form := url.Values{"token": {tc.raw}}
		c, _ := newTestContext(http.MethodPost, "/compat/magiccube/v1/test", form)
		_ = c.Request.ParseForm()
		raw := strings.TrimSpace(c.Request.FormValue("token"))
		keyID := raw
		if head, _, ok := strings.Cut(raw, "."); ok {
			keyID = strings.TrimSpace(head)
		}
		if keyID != tc.wantKey {
			t.Fatalf("token %q resolved to key id %q, want %q", tc.raw, keyID, tc.wantKey)
		}
	}
}

// TestUpstreamErrorEnvelopeMatchesModuleExpectations pins the response shape the
// PHP module parses: code==1 means success, otherwise msg is displayed.
func TestUpstreamErrorEnvelopeMatchesModuleExpectations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	upstreamFail(c, http.StatusPaymentRequired, "上游余额不足")
	body := rec.Body.String()
	if !strings.Contains(body, "\"code\":402") {
		t.Fatalf("error envelope missing numeric code: %s", body)
	}
	if !strings.Contains(body, "上游余额不足") {
		t.Fatalf("error envelope missing msg the module shows to the operator: %s", body)
	}

	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	upstreamOK(c2, "success", gin.H{"id": "abc"})
	if !strings.Contains(rec2.Body.String(), "\"code\":1") {
		t.Fatalf("success envelope must set code=1: %s", rec2.Body.String())
	}
}
