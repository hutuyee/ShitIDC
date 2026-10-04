package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

func RandomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func HMACSecret(master []byte, value string) (string, error) {
	if len(master) == 0 {
		return "", fmt.Errorf("master key is not configured")
	}
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// NumericCode returns a zero-padded random numeric code (crypto/rand).
func NumericCode(digits int) (string, error) {
	if digits <= 0 || digits > 18 {
		digits = 6
	}
	max := uint64(1)
	for i := 0; i < digits; i++ {
		max *= 10
	}
	var buf [8]byte
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		n := binary.BigEndian.Uint64(buf[:]) % max
		out := fmt.Sprintf("%0*d", digits, n)
		// avoid codes that collapse when leading zeros are stripped client-side
		if len(out) == digits {
			return out, nil
		}
	}
}
