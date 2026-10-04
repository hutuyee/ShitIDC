package payment

import (
	"net/url"
	"slices"
	"testing"
)

func TestRegistryDefaultsToEpay(t *testing.T) {
	gw, ok := Get("")
	if !ok || gw.Method() != "epay" {
		t.Fatalf("default gateway = %v ok=%v, want epay", gw, ok)
	}
	if _, ok := Get("epay"); !ok {
		t.Fatal("epay must be registered")
	}
	if _, ok := Get("nonexistent"); ok {
		t.Fatal("unknown method must not resolve")
	}
	methods := Methods()
	if !slices.Contains(methods, "epay") {
		t.Fatalf("methods = %v", methods)
	}
}

func TestEpayVerifyNotifyRejectsBadSignature(t *testing.T) {
	gw := EpayGateway{}
	cfg := ProviderConfig{Method: "epay", GatewayURL: "https://pay.example.com", MerchantID: "1001", Secret: "testkey"}
	values := url.Values{"out_trade_no": {"O123"}, "trade_status": {"TRADE_SUCCESS"}, "money": {"10.00"}}
	if res := gw.VerifyNotify(cfg, NotifyInput{PostForm: values}); res.OK {
		t.Fatal("missing sign must be rejected")
	}
	if res := gw.VerifyNotify(cfg, NotifyInput{PostForm: url.Values{}}); res.OK {
		t.Fatal("empty payload must be rejected")
	}
}
