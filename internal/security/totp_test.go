package security

import (
	"testing"
	"time"
)

// RFC 6238 appendix B test secret ("12345678901234567890" in base32).
const totpTestSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTPRFC6238Vectors(t *testing.T) {
	cases := []struct {
		unix int64
		code string
	}{
		{59, "287082"},         // RFC vector 94287082 truncated to 6 digits
		{1111111109, "081804"}, // 07081804
		{1234567890, "005924"}, // 89005924
		{2000000000, "279037"}, // 69279037
	}
	for _, c := range cases {
		got, err := TOTPCode(totpTestSecret, time.Unix(c.unix, 0))
		if err != nil {
			t.Fatalf("TOTPCode(%d): %v", c.unix, err)
		}
		if got != c.code {
			t.Errorf("TOTPCode at %d = %s, want %s", c.unix, got, c.code)
		}
	}
}

func TestTOTPVerifyWindow(t *testing.T) {
	now := time.Now()
	code, err := TOTPCode(totpTestSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyTOTP(totpTestSecret, code) {
		t.Error("current step code should verify")
	}
	// previous step accepted through the drift window
	prev, _ := TOTPCode(totpTestSecret, now.Add(-totpStep))
	if !VerifyTOTP(totpTestSecret, prev) {
		t.Error("previous step code should verify within window")
	}
	// two steps back must be rejected
	old, _ := TOTPCode(totpTestSecret, now.Add(-3*totpStep))
	if VerifyTOTP(totpTestSecret, old) {
		t.Error("stale code must not verify")
	}
	if VerifyTOTP(totpTestSecret, "00000") {
		t.Error("malformed code must not verify")
	}
}

func TestGenerateTOTPSecret(t *testing.T) {
	s, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 32 { // 160-bit secret -> 32 base32 chars without padding
		t.Fatalf("secret length = %d, want 32", len(s))
	}
	if _, err := TOTPCode(s, time.Now()); err != nil {
		t.Errorf("generated secret must decode: %v", err)
	}
	uri := TOTPProvisioningURI(s, "ShitIDC", "a@b.c")
	if len(uri) < len("otpauth://totp/") || uri[:15] != "otpauth://totp/" {
		t.Errorf("unexpected provisioning uri: %s", uri)
	}
}

func TestCaptchaRoundTrip(t *testing.T) {
	c, err := NewCaptcha()
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.SVG == "" {
		t.Fatal("captcha must produce id and svg")
	}
	if len(c.SVG) < 20 || c.SVG[:4] != "<svg" {
		t.Errorf("svg output malformed: %.20s", c.SVG)
	}
}
