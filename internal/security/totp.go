package security

import (
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238) two-factor support (§9 可选 2FA). Self-contained on
// crypto/hmac + sha1 so the finance core takes no new third-party dependency
// for its security path.

const (
	totpStep    = 30 * time.Second
	totpDigits  = 6
	totpWindow  = 1  // accept the previous and next step for clock drift
	totpSecretN = 20 // 160-bit secret
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a base32 (RFC 4648, no padding) shared secret.
func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretN)
	if _, err := cryptorand.Read(raw); err != nil {
		return "", err
	}
	return base32NoPad.EncodeToString(raw), nil
}

// TOTPCode computes the 6-digit code for a base32 secret at time t.
func TOTPCode(secretBase32 string, t time.Time) (string, error) {
	key, err := base32NoPad.DecodeString(strings.ToUpper(strings.ReplaceAll(secretBase32, " ", "")))
	if err != nil {
		return "", fmt.Errorf("invalid base32 secret")
	}
	counter := uint64(t.Unix()) / uint64(totpStep.Seconds())
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	code := bin % 1000000
	return fmt.Sprintf("%06d", code), nil
}

// VerifyTOTP checks a user-supplied code against the secret allowing one step
// of clock drift in either direction. Comparison is constant-time per step.
func VerifyTOTP(secretBase32, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	now := time.Now()
	for _, d := range []int{0, -totpWindow, totpWindow} {
		want, err := TOTPCode(secretBase32, now.Add(time.Duration(d)*totpStep))
		if err != nil {
			return false
		}
		if hmac.Equal([]byte(want), []byte(code)) {
			return true
		}
	}
	return false
}

// TOTPProvisioningURI builds the otpauth:// URI authenticator apps scan or
// accept as a manually typed key.
func TOTPProvisioningURI(secretBase32, siteName, account string) string {
	label := url.PathEscape(siteName + ":" + account)
	params := url.Values{}
	params.Set("secret", secretBase32)
	params.Set("issuer", siteName)
	params.Set("algorithm", "SHA1")
	params.Set("digits", "6")
	params.Set("period", "30")
	return "otpauth://totp/" + label + "?" + params.Encode()
}
