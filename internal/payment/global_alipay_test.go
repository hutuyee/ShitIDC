package payment

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// 独立复算 MD5 签名：去掉 sign/sign_type/空值，按名排序拼 k=v&k=v 后直接拼密钥。
func globalAliPayTestSign(params url.Values, key string) string {
	keys := make([]string, 0, len(params))
	for k, vs := range params {
		if k == "sign" || k == "sign_type" || len(vs) == 0 || strings.TrimSpace(vs[0]) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params.Get(k))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	return hex.EncodeToString(sum[:])
}

// 下单：境外收单参数齐备、金额按汇率换算并截断到分、签名自洽。
func TestGlobalAliPayPayURL(t *testing.T) {
	g := GlobalAliPayGateway{}
	cfg := ProviderConfig{
		Method:     "global_alipay",
		MerchantID: "2088621935295134",
		GatewayURL: "https://intlmapi.example/gateway.do",
		Secret:     `{"key":"KEY","currency":"HKD","rate":"0.91"}`,
	}
	p := Prepared{OutTradeNo: "INV1", Subject: "云主机 服务费", AmountCents: 1230, PayType: "alipay",
		NotifyURL: "https://shop.example.com/api/v1/pay/notify/global_alipay", ReturnURL: "https://shop.example.com/return"}
	u, err := g.PayURL(context.Background(), cfg, p)
	if err != nil {
		t.Fatalf("PayURL: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("url parse: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "intlmapi.example" || parsed.Path != "/gateway.do" {
		t.Fatalf("url = %s", u)
	}
	q := parsed.Query()
	checks := map[string]string{
		"service":        "create_forex_trade",
		"partner":        "2088621935295134",
		"out_trade_no":   "INV1",
		"currency":       "HKD",
		"total_fee":      "11.19",
		"product_code":   "NEW_OVERSEAS_SELLER",
		"sign_type":      "MD5",
		"refer_url":      "https://shop.example.com",
		"_input_charset": "utf-8",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(q.Get("subject"), "Service Fee") {
		t.Errorf("subject = %q, want 服务费 replaced", q.Get("subject"))
	}
	var info struct {
		BusinessType      int    `json:"business_type"`
		OtherBusinessType string `json:"other_business_type"`
	}
	if err := json.Unmarshal([]byte(q.Get("trade_information")), &info); err != nil || info.BusinessType != 5 {
		t.Fatalf("trade_information = %q, %v", q.Get("trade_information"), err)
	}
	if got := q.Get("sign"); got != globalAliPayTestSign(q, "KEY") {
		t.Fatalf("sign = %s", got)
	}
}

// 回调验签：合法签名 + TRADE_SUCCESS 通过；篡改金额、错签名、非成功状态、缺签名都拒绝。
func TestGlobalAliPayVerifyNotify(t *testing.T) {
	cfg := ProviderConfig{Method: "global_alipay", MerchantID: "2088123456789012", Secret: `{"key":"SECRET","currency":"HKD","rate":"0.91"}`}
	g := GlobalAliPayGateway{}

	good := url.Values{
		"trade_status": {"TRADE_SUCCESS"},
		"trade_no":     {"2024ALI0001"},
		"out_trade_no": {"INV7"},
		"total_fee":    {"11.19"},
		"currency":     {"HKD"},
		"partner":      {"2088123456789012"},
	}
	good.Set("sign", globalAliPayTestSign(good, "SECRET"))
	good.Set("sign_type", "MD5")
	res := g.VerifyNotify(cfg, NotifyInput{PostForm: good})
	if !res.OK || res.TradeNo != "2024ALI0001" || res.AmountCents != 1230 {
		t.Fatalf("notify result = %+v", res)
	}

	// 篡改金额但保留原签名。
	bad := clonePayValues(good)
	bad.Set("total_fee", "0.01")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: bad}); res.OK {
		t.Fatal("tampered amount must fail sign")
	}

	// 错签名。
	bad = clonePayValues(good)
	bad.Set("sign", "deadbeef")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: bad}); res.OK {
		t.Fatal("bad sign must fail")
	}

	// WAIT_BUYER_PAY：签名合法但不算支付成功。
	pending := clonePayValues(good)
	pending.Set("trade_status", "WAIT_BUYER_PAY")
	pending.Set("sign", globalAliPayTestSign(pending, "SECRET"))
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: pending}); res.OK || res.Err == nil {
		t.Fatal("non-success status must not verify as paid")
	}

	// 缺 sign。
	missing := clonePayValues(good)
	missing.Del("sign")
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: missing}); res.OK || res.Err == nil {
		t.Fatal("missing sign must be rejected")
	}
}

// 汇率换算：下单金额 ×rate 截断到分，回调金额 ÷rate 四舍五入回分，闭环不漂移。
func TestGlobalAliPayRateMath(t *testing.T) {
	rate := big.NewRat(91, 100) // 0.91 HKD/CNY
	if got := payRateMulCents(1230, rate); got != 1119 {
		t.Fatalf("payRateMulCents(1230, 0.91) = %d, want 1119", got)
	}
	if got := payRateDivCents(1119, rate); got != 1230 {
		t.Fatalf("payRateDivCents(1119, 0.91) = %d, want 1230", got)
	}
	// 乘法截断（bcmul(..., 2) 的语义）：100 × 2/3 = 66.67 → 66。
	if got := payRateMulCents(100, big.NewRat(2, 3)); got != 66 {
		t.Fatalf("payRateMulCents(100, 2/3) = %d, want 66", got)
	}
	// 不足 1 分的部分在乘法里被截掉。
	if got := payRateMulCents(1, rate); got != 0 {
		t.Fatalf("payRateMulCents(1, 0.91) = %d, want 0", got)
	}
}

// wap 渠道：service / product_code 用 wap 变体；纯密钥串默认 HKD、rate 1。
func TestGlobalAliPayPayURLWap(t *testing.T) {
	g := GlobalAliPayGateway{}
	u, err := g.PayURL(context.Background(),
		ProviderConfig{MerchantID: "2088123456789012", Secret: "KEY"},
		Prepared{OutTradeNo: "INV2", Subject: "订单", AmountCents: 100, PayType: "wap", NotifyURL: "https://shop.example.com/notify", ReturnURL: "https://shop.example.com/return"})
	if err != nil {
		t.Fatalf("PayURL: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := parsed.Query()
	if q.Get("service") != "create_forex_trade_wap" || q.Get("product_code") != "NEW_WAP_OVERSEAS_SELLER" {
		t.Fatalf("wap params = %s / %s", q.Get("service"), q.Get("product_code"))
	}
	if q.Get("currency") != "HKD" || q.Get("total_fee") != "1.00" {
		t.Fatalf("defaults = %s / %s, want HKD / 1.00", q.Get("currency"), q.Get("total_fee"))
	}
	if q.Get("refer_url") != "https://shop.example.com" {
		t.Fatalf("refer_url = %s", q.Get("refer_url"))
	}
	if globalAliPayTestSign(q, "KEY") != q.Get("sign") {
		t.Fatal("sign mismatch")
	}
}

// 凭据校验：缺 partner、缺 key、rate 非正数都要报错；零金额拒绝。
func TestGlobalAliPayValidate(t *testing.T) {
	g := GlobalAliPayGateway{}
	prepared := Prepared{OutTradeNo: "INV3", Subject: "订单", AmountCents: 1000, PayType: "pc"}

	if _, err := g.PayURL(context.Background(), ProviderConfig{Secret: "KEY"}, prepared); err == nil || !strings.Contains(err.Error(), "partner") {
		t.Fatalf("missing partner must fail, got %v", err)
	}
	if _, err := g.PayURL(context.Background(), ProviderConfig{MerchantID: "2088", Secret: `{"rate":"1"}`}, prepared); err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("missing key must fail, got %v", err)
	}
	for _, rate := range []string{"0", "-0.5"} {
		secret := `{"key":"K","rate":"` + rate + `"}`
		if _, err := g.PayURL(context.Background(), ProviderConfig{MerchantID: "2088", Secret: secret}, prepared); err == nil || !strings.Contains(err.Error(), "rate") {
			t.Fatalf("rate %s must fail, got %v", rate, err)
		}
	}
	if _, err := g.PayURL(context.Background(), ProviderConfig{MerchantID: "2088", Secret: "K"}, Prepared{OutTradeNo: "INV4", PayType: "pc"}); err == nil || !strings.Contains(err.Error(), "金额") {
		t.Fatalf("zero amount must fail, got %v", err)
	}
}
