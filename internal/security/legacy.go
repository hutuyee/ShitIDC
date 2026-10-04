package security

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
)

// VerifyLegacyPassword checks a password against a migrated legacy hash
// (第十四阶段 §37 PasswordVerifier / MagicCubeLegacy). MagicCube stores
// user passwords as plain MD5 hex; migrated accounts keep that value in
// user_security.legacy_password_hash and upgrade to Argon2id on first login.
func VerifyLegacyPassword(legacyHash, password string) bool {
	if legacyHash == "" {
		return false
	}
	sum := md5.Sum([]byte(password))
	return hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(legacyHash))
}

// SignHMAC returns hex HMAC-SHA256 over message using key.
func SignHMAC(key []byte, message []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

// MaskPhone masks the middle digits: 138****8888.
func MaskPhone(v string) string {
	runes := []rune(v)
	switch {
	case len(runes) >= 11:
		return string(runes[:3]) + "****" + string(runes[len(runes)-4:])
	case len(runes) >= 7:
		return string(runes[:2]) + "****" + string(runes[len(runes)-2:])
	case len(runes) > 2:
		return string(runes[:1]) + "****"
	default:
		return ""
	}
}

// MaskEmail hides most of the local part: a***@example.com.
func MaskEmail(v string) string {
	for i := 0; i < len(v); i++ {
		if v[i] == '@' {
			local := v[:i]
			if len(local) <= 1 {
				return v
			}
			return local[:1] + "***" + v[i:]
		}
	}
	return v
}
