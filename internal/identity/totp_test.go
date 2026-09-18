package identity

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors for the SHA-1 secret "12345678901234567890",
// truncated to the 6 digits authenticator apps use.
func TestTOTPVectors(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	vectors := map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	}
	for ts, want := range vectors {
		got, err := totpCode(secret, time.Unix(ts, 0))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("t=%d: got %s want %s", ts, got, want)
		}
	}
}

func TestTOTPValidWindow(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Unix(1111111111, 0)
	if !totpValid(secret, "050471", now) {
		t.Error("current code rejected")
	}
	if !totpValid(secret, "050 471", now) {
		t.Error("spaces should be tolerated")
	}
	prev, _ := totpCode(secret, now.Add(-totpPeriod))
	next, _ := totpCode(secret, now.Add(totpPeriod))
	if !totpValid(secret, prev, now) || !totpValid(secret, next, now) {
		t.Error("adjacent windows should be accepted")
	}
	far, _ := totpCode(secret, now.Add(3*totpPeriod))
	if totpValid(secret, far, now) {
		t.Error("code three windows away must be rejected")
	}
	if totpValid(secret, "00000", now) || totpValid(secret, "", now) {
		t.Error("wrong length accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	uri := TOTPURI("Payments", "a@b.co", "SECRET")
	if !strings.HasPrefix(uri, "otpauth://totp/Payments:a@b.co?") || !strings.Contains(uri, "secret=SECRET") || !strings.Contains(uri, "issuer=Payments") {
		t.Errorf("uri = %s", uri)
	}
}
