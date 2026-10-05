package payment

import (
	"context"
	"crypto/sha256"
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

// 独立复算 AllPay v5 签名（测试侧自己的实现）。
func goallpayTestSign(params url.Values, key string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "signature" || params.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, k+"="+params.Get(k))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "&") + key))
	return hex.EncodeToString(sum[:])
}

// 下单全流程：字段齐备、渠道映射、签名一致、RespCode=00 返回 payUrl。
func TestGoallpayPayURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/createorder" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var sent map[string]string
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatalf("body not json: %v (%s)", err, body)
		}
		params := url.Values{}
		for k, v := range sent {
			params.Set(k, v)
		}
		if goallpayTestSign(params, "SIGNKEY") != sent["signature"] {
			t.Errorf("signature mismatch")
		}
		for _, key := range []string{"orderNum", "orderCurrency", "merID", "transTime", "signType", "orderAmount", "paymentMethod", "version", "transType", "acqID", "paymentSchema", "tradeFrom"} {
			if sent[key] == "" {
				t.Errorf("missing param %s", key)
			}
		}
		if sent["paymentMethod"] != "wechat_pay" {
			t.Errorf("paymentMethod = %s", sent["paymentMethod"])
		}
		if sent["orderCurrency"] != "USD" {
			t.Errorf("orderCurrency = %s", sent["orderCurrency"])
		}
		if sent["orderAmount"] != "45.60" {
			t.Errorf("orderAmount = %s", sent["orderAmount"])
		}
		// detailInfo 是 base64 的 [{"goods_name":...,"quantity":"1"}]
		if sent["detailInfo"] == "" {
			t.Errorf("detailInfo empty")
		}
		w.Write([]byte(`{"RespCode":"00","RespMsg":"success","parameter":{"payUrl":"https://pay.example.com/abc"}}`))
	}))
	defer srv.Close()

	g := GoallpayGateway{Now: func() time.Time { return time.Unix(1700000000, 0) }}
	u, err := g.PayURL(context.Background(),
		ProviderConfig{GatewayURL: srv.URL, MerchantID: "MERID", Secret: "SIGNKEY"},
		Prepared{OutTradeNo: "INV7", Subject: "Cloud Server", AmountCents: 4560, Currency: "USD", PayType: "wxpay", NotifyURL: "https://s/notify", ReturnURL: "https://s/return"})
	if err != nil {
		t.Fatalf("PayURL: %v", err)
	}
	if !strings.Contains(u, "pay.example.com/abc") {
		t.Fatalf("url = %s", u)
	}
}

// 渠道映射：alipay→alipay_cn、unionpay→unionpay、其余拒绝。
func TestGoallpayPaymentMethodMap(t *testing.T) {
	if got := goallpayPaymentMethod("alipay"); got != "alipay_cn" {
		t.Fatalf("alipay => %s", got)
	}
	if got := goallpayPaymentMethod("unionpay"); got != "unionpay" {
		t.Fatalf("unionpay => %s", got)
	}
	if got := goallpayPaymentMethod("qqpay"); got != "" {
		t.Fatalf("qqpay => %s", got)
	}
}

// 下单失败：RespCode != 00 把 RespMsg 带出来。
func TestGoallpayPayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"RespCode":"E1","RespMsg":"invalid merchant"}`))
	}))
	defer srv.Close()
	g := GoallpayGateway{}
	_, err := g.PayURL(context.Background(),
		ProviderConfig{GatewayURL: srv.URL, MerchantID: "M", Secret: "K"},
		Prepared{OutTradeNo: "1", Subject: "s", AmountCents: 100, PayType: "alipay"})
	if err == nil || !strings.Contains(err.Error(), "invalid merchant") {
		t.Fatalf("want RespMsg surfaced, got %v", err)
	}
}

// 回调验签：合法签名 + RespCode=00 通过；错签名、非 00、改金额拒绝。
func TestGoallpayVerifyNotify(t *testing.T) {
	cfg := ProviderConfig{Secret: "SIGNKEY"}
	g := GoallpayGateway{}

	good := url.Values{
		"GWTime":        {"20260101010101"},
		"RespCode":      {"00"},
		"RespMsg":       {"success"},
		"acqID":         {"99020344"},
		"charSet":       {"UTF-8"},
		"merID":         {"M"},
		"orderAmount":   {"100.00"},
		"orderCurrency": {"CNY"},
		"orderNum":      {"INV9"},
		"paymentSchema": {"AP"},
		"signType":      {"SHA256"},
		"transID":       {"TX123"},
		"transTime":     {"20260101010101"},
		"transType":     {"PURC"},
		"version":       {"VER000000005"},
	}
	good.Set("signature", goallpayTestSign(good, "SIGNKEY"))
	res := g.VerifyNotify(cfg, NotifyInput{PostForm: good})
	if !res.OK || res.TradeNo != "TX123" || res.AmountCents != 10000 {
		t.Fatalf("notify = %+v", res)
	}

	bad := clonePayValues(good)
	bad.Set("orderAmount", "0.01")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: bad}); res.OK {
		t.Fatal("tampered amount must fail")
	}
	bad = clonePayValues(good)
	bad.Set("signature", "deadbeef")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: bad}); res.OK {
		t.Fatal("bad signature must fail")
	}
	pending := clonePayValues(good)
	pending.Set("RespCode", "P00")
	pending.Set("signature", goallpayTestSign(pending, "SIGNKEY"))
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: pending}); res.OK {
		t.Fatal("non-00 must not verify as paid")
	}
}
