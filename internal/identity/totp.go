package identity

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 defaults, what every authenticator app expects).
const (
	totpPeriod = 30 * time.Second
	totpDigits = 6
	totpSkew   = 1 // accept the previous and next window
)

// totpCode computes the code for a base32 secret at time t.
func totpCode(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", fmt.Errorf("identity: bad totp secret: %w", err)
	}
	counter := uint64(t.Unix()) / uint64(totpPeriod/time.Second)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, bin%1_000_000), nil
}

// totpValid checks a user-supplied code against the current window ± skew.
func totpValid(secret, code string, now time.Time) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return false
	}
	ok := false
	for i := -totpSkew; i <= totpSkew; i++ {
		want, err := totpCode(secret, now.Add(time.Duration(i)*totpPeriod))
		if err != nil {
			return false
		}
		// constant-time per candidate; do not short-circuit so timing is uniform
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			ok = true
		}
	}
	return ok
}

// TOTPURI builds the otpauth:// URI an authenticator app scans.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(int(totpPeriod/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
