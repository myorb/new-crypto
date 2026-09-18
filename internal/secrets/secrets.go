// Package secrets is the one place that generates tokens, hashes them for
// storage and encrypts the few values that must be recoverable (webhook
// signing secrets, TOTP seeds). Nothing secret is ever stored in clear.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	ErrNoKey        = errors.New("secrets: no encryption key configured")
	ErrBadKey       = errors.New("secrets: APP_ENCRYPTION_KEY must be 32 bytes hex (64 chars), optionally prefixed with a version like v1:")
	ErrCorrupt      = errors.New("secrets: ciphertext is corrupt or was produced with an unknown key version")
	ErrUnknownKeyID = errors.New("secrets: ciphertext references a key version that is not loaded")
)

// Keyring holds one or more AES-256 keys by version. New ciphertext always
// uses Current; old versions stay loaded so rotation is a config change.
// Wire format: [1 byte version][12 byte nonce][AES-GCM ciphertext+tag].
type Keyring struct {
	current uint8
	keys    map[uint8]cipher.AEAD
}

// LoadKeyring parses "v1:<hex>,v2:<hex>" (or a bare hex key, treated as v1).
// The highest version becomes the current key.
func LoadKeyring(spec string) (*Keyring, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, ErrNoKey
	}
	kr := &Keyring{keys: map[uint8]cipher.AEAD{}}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		version := uint8(1)
		hexKey := part
		if i := strings.IndexByte(part, ':'); i > 0 {
			v, err := strconv.ParseUint(strings.TrimPrefix(part[:i], "v"), 10, 8)
			if err != nil || v == 0 {
				return nil, ErrBadKey
			}
			version = uint8(v)
			hexKey = part[i+1:]
		}
		raw, err := hex.DecodeString(hexKey)
		if err != nil || len(raw) != 32 {
			return nil, ErrBadKey
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		kr.keys[version] = aead
		if version > kr.current {
			kr.current = version
		}
	}
	return kr, nil
}

// Encrypt seals plaintext with the current key.
func (k *Keyring) Encrypt(plaintext []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKey
	}
	aead := k.keys[k.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, k.current)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, []byte{k.current}), nil
}

// Decrypt opens a value produced by Encrypt with any loaded key version.
func (k *Keyring) Decrypt(ciphertext []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKey
	}
	if len(ciphertext) < 1 {
		return nil, ErrCorrupt
	}
	version := ciphertext[0]
	aead, ok := k.keys[version]
	if !ok {
		return nil, ErrUnknownKeyID
	}
	ns := aead.NonceSize()
	if len(ciphertext) < 1+ns+aead.Overhead() {
		return nil, ErrCorrupt
	}
	nonce, body := ciphertext[1:1+ns], ciphertext[1+ns:]
	plain, err := aead.Open(nil, nonce, body, []byte{version})
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

// EncryptString / DecryptString are Encrypt / Decrypt for text values.
func (k *Keyring) EncryptString(s string) ([]byte, error) { return k.Encrypt([]byte(s)) }
func (k *Keyring) DecryptString(b []byte) (string, error) {
	p, err := k.Decrypt(b)
	return string(p), err
}

// Hash is the storage form of every bearer token: SHA-256 of the raw value.
// Tokens carry 256 bits of entropy, so an unsalted hash is sufficient and
// keeps lookups a single indexed equality.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// HashEqual compares two hashes in constant time.
func HashEqual(a, b []byte) bool { return hmac.Equal(a, b) }

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// RandomString returns n characters from an unambiguous base-62 alphabet,
// drawn without modulo bias.
func RandomString(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < 62*4 { // 248 = largest multiple of 62 below 256
				out = append(out, alphabet[int(b)%62])
				if len(out) == n {
					break
				}
			}
		}
	}
	return string(out), nil
}

// Token returns a prefixed bearer token such as "sess_…", "sk_live_…",
// "inv_…" or "whsec_…" with 43 random base-62 characters (~256 bits).
func Token(prefix string) (string, error) {
	s, err := RandomString(43)
	if err != nil {
		return "", err
	}
	return prefix + s, nil
}

// DisplayPrefix returns the part of a token that may be shown in a UI list,
// e.g. "sk_live_4fT9" for a key. Never store or log more than this.
func DisplayPrefix(token string) string {
	const visible = 4
	i := strings.LastIndexByte(token, '_')
	if i < 0 || i+1+visible > len(token) {
		if len(token) > visible {
			return token[:visible]
		}
		return token
	}
	return token[:i+1+visible]
}

// TOTPSecret returns a fresh 160-bit base32 seed for an authenticator app.
func TOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// RecoveryCode returns a human-typable code like "7K3M-Q9XA-2FHT".
func RecoveryCode() (string, error) {
	s, err := RandomString(12)
	if err != nil {
		return "", err
	}
	s = strings.ToUpper(s)
	return fmt.Sprintf("%s-%s-%s", s[0:4], s[4:8], s[8:12]), nil
}

// NormalizeRecoveryCode strips separators and case so users can paste freely.
func NormalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// SignHMAC returns hex(HMAC-SHA256(key, msg)); the webhook signature primitive.
func SignHMAC(key []byte, msg []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHMAC checks a hex signature produced by SignHMAC in constant time.
func VerifyHMAC(key []byte, msg []byte, hexSig string) bool {
	want, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return hmac.Equal(mac.Sum(nil), want)
}
