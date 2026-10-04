package epay

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"testing"
)

func TestSignMatchesReferenceImplementation(t *testing.T) {
	cfg := Config{GatewayURL: "https://pay.example.com", PID: "1001", Key: "testkey123"}
	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "20160921100339",
		"notify_url":   "https://site.example/notify",
		"return_url":   "https://site.example/return",
		"name":         "VPS套餐",
		"money":        "100.00",
		"sign":         "should-be-ignored",
		"sign_type":    "MD5",
		"empty":        "",
	}
	// Independent reference computation: sort, join, append key, md5 lower hex.
	// Sorted keys: money,name,notify_url,out_trade_no,pid,return_url,type
	// (empty value excluded, sign/sign_type excluded)
	want := md5hex("money=100.00&name=VPS套餐&notify_url=https://site.example/notify&out_trade_no=20160921100339&pid=1001&return_url=https://site.example/return&type=alipay" + "testkey123")
	got := cfg.Sign(params)
	if got != want {
		t.Fatalf("sign mismatch: got %s want %s", got, want)
	}
	if got != lower(got) {
		t.Fatalf("sign must be lower-case hex, got %s", got)
	}
}

func TestVerifyNotify(t *testing.T) {
	cfg := Config{GatewayURL: "https://pay.example.com", PID: "1001", Key: "testkey123"}
	params := map[string]string{
		"pid": "1001", "trade_no": "GW2024", "out_trade_no": "Oabc", "type": "wxpay",
		"name": "充值", "money": "50.00", "trade_status": "TRADE_SUCCESS", "param": "",
	}
	params["sign"] = cfg.Sign(params)
	params["sign_type"] = "MD5"
	values := toValues(params)
	if err := cfg.VerifyNotify(values); err != nil {
		t.Fatalf("valid notify rejected: %v", err)
	}
	// tampered amount must fail
	values.Set("money", "500.00")
	if err := cfg.VerifyNotify(values); err == nil {
		t.Fatal("tampered notify accepted")
	}
	// wrong merchant must fail
	values.Set("money", "50.00")
	values.Set("pid", "9999")
	if err := cfg.VerifyNotify(values); err == nil {
		t.Fatal("notify with wrong pid accepted")
	}
}

func TestPayURL(t *testing.T) {
	cfg := Config{GatewayURL: "https://pay.example.com/", PID: "1001", Key: "testkey123"}
	urlStr, err := cfg.PayURL(SubmitParams{PayType: "alipay", OutTradeNo: "O123", Name: "订单", MoneyCents: 10000, NotifyURL: "https://site.example/notify", ReturnURL: "https://site.example/return", SiteName: "ShitIDC"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(urlStr, "https://pay.example.com/submit.php?") {
		t.Fatalf("unexpected gateway url: %s", urlStr)
	}
	for _, want := range []string{"money=100.00", "out_trade_no=O123", "sign_type=MD5", "sign="} {
		if !contains(urlStr, want) {
			t.Fatalf("pay url missing %s: %s", want, urlStr)
		}
	}
}

func TestMoneyToCents(t *testing.T) {
	cases := map[string]int64{"10.00": 1000, "0.10": 10, "100": 10000, "12.34": 1234, "0": 0, " 5.50 ": 550}
	for in, want := range cases {
		if got, err := MoneyToCents(in); err != nil || got != want {
			t.Fatalf("MoneyToCents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := MoneyToCents("abc"); err == nil {
		t.Fatal("non-numeric money accepted")
	}
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func lower(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func toValues(m map[string]string) url.Values {
	v := url.Values{}
	for k, val := range m {
		v.Set(k, val)
	}
	return v
}
