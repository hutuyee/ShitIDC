package tc3

import "testing"

// 固定向量与短信通道共用（独立用 Python 按官方「签名方法 v3」复算得出）：
// service / host 是参数，签名链上任何一步写错都会在这里爆出来。
func TestAuthorizationVector(t *testing.T) {
	payload := `{"PhoneNumberSet":["+8613800138000"],"SmsSdkAppId":"1400000000","SignName":"测试签名","TemplateId":"100001","TemplateParamSet":["123456"]}`
	got := Authorization("AKIDtest", "TESTKEY", "sms", "sms.tencentcloudapi.com", "2026-10-05", 1759650000, payload)
	want := "TC3-HMAC-SHA256 Credential=AKIDtest/2026-10-05/sms/tc3_request, SignedHeaders=content-type;host, Signature=f2556471dd627857394e9d218bf333f4e374414cc90ed024ccfc7c6ac3530b7c"
	if got != want {
		t.Fatalf("Authorization mismatch:\n got  %s\n want %s", got, want)
	}
}

// 换 service / 密钥 / 载荷都必须改变签名，且可复算。
func TestAuthorizationDependsOnInputs(t *testing.T) {
	const payload = `{"a":1}`
	base := Authorization("id", "key", "sms", "sms.tencentcloudapi.com", "2026-10-05", 1759650000, payload)
	if base != Authorization("id", "key", "sms", "sms.tencentcloudapi.com", "2026-10-05", 1759650000, payload) {
		t.Fatal("signature is not deterministic")
	}
	for _, other := range []string{
		Authorization("id2", "key", "sms", "sms.tencentcloudapi.com", "2026-10-05", 1759650000, payload),
		Authorization("id", "key2", "sms", "sms.tencentcloudapi.com", "2026-10-05", 1759650000, payload),
		Authorization("id", "key", "captcha", "captcha.tencentcloudapi.com", "2026-10-05", 1759650000, payload),
		Authorization("id", "key", "sms", "sms.tencentcloudapi.com", "2026-10-06", 1759650000, payload),
		Authorization("id", "key", "sms", "sms.tencentcloudapi.com", "2026-10-05", 1759650001, payload),
		Authorization("id", "key", "sms", "sms.tencentcloudapi.com", "2026-10-05", 1759650000, payload+" "),
	} {
		if base == other {
			t.Fatalf("different inputs produced same signature: %s", other)
		}
	}
}
