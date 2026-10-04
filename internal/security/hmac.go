package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HMACSHA256Hex 用 master key 对值做 HMAC-SHA256，返回小写十六进制。
//
// 用途是「去重指纹」：证件号、手机号这类值空间有限，裸 SHA256 可以被穷举反查，
// 带上密钥的 HMAC 就不会。密钥泄露前，指纹不可逆。
func HMACSHA256Hex(master []byte, value string) string {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(mac.Sum(nil))
}
