package payment

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// 用与实现独立的算法复算虎皮椒 hash：参数按名排序拼 k=v&k=v（排除 hash/空值），
// 末尾直接拼接 AppSecret，取 MD5 小写。与魔方 XunhupayClient::generate_hash 一致。
func xunhuTestHash(params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "hash" || params.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	str := ""
	for i, k := range keys {
		if i > 0 {
			str += "&"
		}
		str += k + "=" + params.Get(k)
	}
	sum := md5.Sum([]byte(str + secret))
	return hex.EncodeToString(sum[:])
}

func TestXunhuHashVector(t *testing.T) {
	// 固定向量：独立计算（python: md5("a=1&b=%E4%B8%AD&appid=123&time=1700000000SECRET")）。
	params := url.Values{"b": {"中"}, "a": {"1"}, "hash": {"ignored"}, "empty": {""}, "appid": {"123"}, "time": {"1700000000"}}
	got := xunhuHash(params, "SECRET")
	sum := md5.Sum([]byte("a=1&appid=123&b=中&time=1700000000SECRET"))
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("xunhuHash = %s", got)
	}
}

// 下单全流程：JSON 体、字段齐备、hash 正确、响应验签、返回收银台地址。
func TestXunhuPayURL(t *testing.T) {
	var cashierURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payment/do.html" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("content type = %q, want json（参考实现发 JSON）", got)
		}
		body, _ := io.ReadAll(r.Body)
		var sent map[string]string
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatalf("body not json: %v", err)
		}
		params := url.Values{}
		for k, v := range sent {
			params.Set(k, v)
		}
		if xunhuTestHash(params, "SECRET") != sent["hash"] {
			t.Errorf("request hash mismatch: %s", sent["hash"])
		}
		for _, key := range []string{"version", "trade_order_id", "payment", "total_fee", "title", "notify_url", "return_url", "appid", "time", "nonce_str"} {
			if sent[key] == "" {
				t.Errorf("missing param %s", key)
			}
		}
		if sent["payment"] != "alipay" {
			t.Errorf("payment = %s", sent["payment"])
		}
		if sent["total_fee"] != "12.30" {
			t.Errorf("total_fee = %s, want 12.30（两位小数字符串）", sent["total_fee"])
		}
		// 响应带 hash，测试服务器用它自己的复算签出来（JSON 体）。
		resp := url.Values{"errcode": {"0"}, "errmsg": {"success"}, "url": {cashierURL + "/cashier/123"}}
		resp.Set("hash", xunhuTestHash(resp, "SECRET"))
		out := map[string]string{}
		for k := range resp {
			out[k] = resp.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}))
	cashierURL = srv.URL
	defer srv.Close()

	g := XunhuPayGateway{Now: func() time.Time { return time.Unix(1700000000, 0) }, Nonce: func() string { return "n1" }, HTTPClient: srv.Client()}
	u, err := g.PayURL(context.Background(),
		ProviderConfig{Method: "xunhupay", GatewayURL: srv.URL, MerchantID: "APPID", Secret: "SECRET"},
		Prepared{OutTradeNo: "INV1", Subject: "商品", AmountCents: 1230, PayType: "alipay", NotifyURL: "https://s/notify", ReturnURL: "https://s/return"})
	if err != nil {
		t.Fatalf("PayURL: %v", err)
	}
	if !strings.Contains(u, "/cashier/123") {
		t.Fatalf("url = %s", u)
	}
}

// 响应 hash 不匹配必须报错——参考实现 `$hash !== $hash` 恒 false 的缺陷不能复刻。
func TestXunhuPayResponseHashFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errcode":0,"errmsg":"success","url":"https://x/cashier","hash":"badhash"}`))
	}))
	defer srv.Close()

	g := XunhuPayGateway{Now: func() time.Time { return time.Unix(1700000000, 0) }, HTTPClient: srv.Client()}
	_, err := g.PayURL(context.Background(),
		ProviderConfig{GatewayURL: srv.URL, MerchantID: "APPID", Secret: "SECRET"},
		Prepared{OutTradeNo: "INV1", Subject: "s", AmountCents: 100, PayType: "wechat"})
	if err == nil || !strings.Contains(err.Error(), "签名校验失败") {
		t.Fatalf("want response hash failure, got %v", err)
	}
}

// errcode != 0 必须把 errmsg 带出来。
func TestXunhuPayGatewayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errcode":20001,"errmsg":"AppSecret错误","hash":"x"}`))
	}))
	defer srv.Close()

	g := XunhuPayGateway{Now: func() time.Time { return time.Unix(1700000000, 0) }, HTTPClient: srv.Client()}
	_, err := g.PayURL(context.Background(),
		ProviderConfig{GatewayURL: srv.URL, MerchantID: "A", Secret: "S"},
		Prepared{OutTradeNo: "1", Subject: "s", AmountCents: 100, PayType: "alipay"})
	if err == nil || !strings.Contains(err.Error(), "AppSecret错误") {
		t.Fatalf("want errmsg surfaced, got %v", err)
	}
}

// 支付方式白名单：只接受 alipay / wechat。
func TestXunhuPayTypeWhitelist(t *testing.T) {
	g := XunhuPayGateway{}
	_, err := g.PayURL(context.Background(), ProviderConfig{MerchantID: "A", Secret: "S"},
		Prepared{AmountCents: 100, PayType: "qqpay"})
	if err == nil || !strings.Contains(err.Error(), "alipay / wechat") {
		t.Fatalf("want paytype rejected, got %v", err)
	}
}

// 回调验签：合法签名 + OD 状态通过；篡改金额、错签名、非 OD 都拒绝。
func TestXunhuVerifyNotify(t *testing.T) {
	cfg := ProviderConfig{Method: "xunhupay", MerchantID: "A", Secret: "SECRET"}
	g := XunhuPayGateway{}

	good := url.Values{
		"trade_order_id": {"INV9"},
		"transaction_id": {"2024XXXX"},
		"status":         {"OD"},
		"total_fee":      {"88.00"},
	}
	good.Set("hash", xunhuTestHash(good, "SECRET"))
	res := g.VerifyNotify(cfg, NotifyInput{PostForm: good})
	if !res.OK || res.TradeNo != "2024XXXX" || res.AmountCents != 8800 {
		t.Fatalf("notify result = %+v", res)
	}

	// 篡改金额但保留原签名。
	bad := clonePayValues(good)
	bad.Set("total_fee", "0.01")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: bad}); res.OK {
		t.Fatal("tampered amount must fail hash")
	}

	// 签名错。
	bad = clonePayValues(good)
	bad.Set("hash", "deadbeef")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: bad}); res.OK {
		t.Fatal("bad hash must fail")
	}

	// 非 OD 状态：签名合法但不算支付成功。
	pending := clonePayValues(good)
	pending.Set("status", "WP")
	pending.Set("hash", xunhuTestHash(pending, "SECRET"))
	res = g.VerifyNotify(cfg, NotifyInput{PostForm: pending})
	if res.OK || res.Err == nil {
		t.Fatal("non-OD status must not verify as paid")
	}
}

// 金额换算：分 → 元字符串 → 分 必须闭环，两位小数以外的形态拒绝。
func TestXunhuMoneyRoundtrip(t *testing.T) {
	for _, cents := range []int64{0, 1, 5, 99, 100, 1230, 999999} {
		if got, err := xunhuYuanToCents(xunhuMoney(cents)); err != nil || got != cents {
			t.Fatalf("roundtrip %d: got %d err %v", cents, got, err)
		}
	}
	if _, err := xunhuYuanToCents("12.3"); err == nil {
		t.Fatal("one decimal digit must be rejected")
	}
	if _, err := xunhuYuanToCents("12.345"); err == nil {
		t.Fatal("three decimal digits must be rejected")
	}
	if _, err := xunhuYuanToCents("abc"); err == nil {
		t.Fatal("non-numeric must be rejected")
	}
	if got, err := xunhuYuanToCents("88.00"); err != nil || got != 8800 {
		t.Fatalf("88.00 => %d, %v", got, err)
	}
}

func clonePayValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		for _, s := range vs {
			out.Add(k, s)
		}
	}
	return out
}
