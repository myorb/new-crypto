package events

import (
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	body := []byte(`{"id":"x"}`)
	ts := time.Now().Unix()
	sig := Sign("whsec_abc", ts, body)
	if !Verify("whsec_abc", sig, ts, body, time.Minute) {
		t.Error("valid signature rejected")
	}
	if Verify("whsec_other", sig, ts, body, time.Minute) {
		t.Error("wrong secret accepted")
	}
	if Verify("whsec_abc", sig, ts, []byte(`{"id":"y"}`), time.Minute) {
		t.Error("tampered body accepted")
	}
	if Verify("whsec_abc", sig, ts-600, body, time.Minute) {
		t.Error("replayed timestamp accepted")
	}
	if !Verify("whsec_abc", "v0=zz, "+sig, ts, body, 0) {
		t.Error("multiple versions in header should still verify")
	}
}

func TestValidateEndpointURL(t *testing.T) {
	good := []string{"https://example.com/hook", "https://api.merchant.io:8443/x?y=1"}
	bad := []string{
		"http://example.com/hook", "https://localhost/hook", "https://127.0.0.1/hook", "https://10.0.0.5/x",
		"https://[::1]/x", "https://169.254.169.254/latest", "https://user:pw@example.com/x", "https://svc.internal/x",
		"ftp://example.com", "", "https://100.64.1.1/x",
	}
	for _, u := range good {
		if err := ValidateEndpointURL(u, false); err != nil {
			t.Errorf("%s should be accepted: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := ValidateEndpointURL(u, false); err == nil {
			t.Errorf("%s should be rejected", u)
		}
	}
	if err := ValidateEndpointURL("https://localhost:9999/hook", true); err != nil {
		t.Errorf("allowPrivate should accept localhost: %v", err)
	}
}
