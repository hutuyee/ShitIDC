package security

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
)

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestHashPasswordRoundtrip(t *testing.T) {
	h, err := HashPassword("super-secret-password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "super-secret-password") {
		t.Fatal("correct password must verify")
	}
	if VerifyPassword(h, "wrong-password-xx") {
		t.Fatal("wrong password must not verify")
	}
}

func TestHashPasswordTooShort(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("passwords under 10 chars must be rejected")
	}
}

func TestVerifyLegacyPassword(t *testing.T) {
	sum := md5Hex("magiccube-old-pass")
	if !VerifyLegacyPassword(sum, "magiccube-old-pass") {
		t.Fatalf("legacy md5 verify failed for %s", sum)
	}
	if VerifyLegacyPassword(sum, "not-the-password") {
		t.Fatal("legacy md5 must reject wrong password")
	}
	if VerifyLegacyPassword("", "anything") {
		t.Fatal("empty legacy hash must never verify")
	}
}

func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"13812348888": "138****8888",
		"1234567":     "12****67",
		"12345":       "1****",
		"12":          "",
	}
	for in, want := range cases {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMaskEmail(t *testing.T) {
	if got := MaskEmail("alice@example.com"); got != "a***@example.com" {
		t.Errorf("MaskEmail got %q", got)
	}
	if got := MaskEmail("no-at-sign"); got != "no-at-sign" {
		t.Errorf("MaskEmail passthrough got %q", got)
	}
}
