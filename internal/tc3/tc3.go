// Package tc3 实现腾讯云 API 3.0 的 TC3-HMAC-SHA256 签名。
//
// 短信（sms）与验证码（captcha）等通道的签名链完全同构，只是 service 与
// host 不同；这里抽出一份，避免安全相关的签名代码各写一遍。
//
//	CanonicalRequest = "POST\n/\n\n" + 规范化头(按名排序,每条以\n结尾) + "\n" +
//	                   "content-type;host\n" + Hex(SHA256(请求体))
//	StringToSign     = "TC3-HMAC-SHA256\n" + 时间戳 + "\n" +
//	                   日期/service/tc3_request + "\n" + Hex(SHA256(CanonicalRequest))
//	Signature        = Hex(HMAC(kSigning, StringToSign))，kDate→kService→kSigning
//	                   逐层 HMAC，种子是 "TC3"+SecretKey
package tc3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Authorization 计算 API 3.0 的 Authorization 头值。
// date 是 UTC 日期（YYYY-MM-DD），必须与 timestamp 同一天；
// 请求头固定为 content-type: application/json; charset=utf-8 与 host。
func Authorization(secretID, secretKey, service, host, date string, timestamp int64, payload string) string {
	canonicalRequest := "POST\n/\n\n" +
		"content-type:application/json; charset=utf-8\n" +
		"host:" + host + "\n" +
		"\n" +
		"content-type;host\n" +
		hashHex(payload)
	scope := date + "/" + service + "/tc3_request"
	stringToSign := "TC3-HMAC-SHA256\n" +
		fmt.Sprintf("%d", timestamp) + "\n" +
		scope + "\n" +
		hashHex(canonicalRequest)

	kDate := hmacSHA256([]byte("TC3"+secretKey), date)
	kService := hmacSHA256(kDate, service)
	kSigning := hmacSHA256(kService, "tc3_request")
	return "TC3-HMAC-SHA256" +
		" Credential=" + secretID + "/" + scope +
		", SignedHeaders=content-type;host" +
		", Signature=" + hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, s string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return mac.Sum(nil)
}
