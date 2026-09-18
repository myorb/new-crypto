package secrets

import (
	"strings"
	"testing"
)

const testKey = "v1:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestKeyringRoundTripAndRotation(t *testing.T) {
	kr, err := LoadKeyring(testKey)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := kr.EncryptString("whsec_secret")
	if err != nil {
		t.Fatal(err)
	}
	if ct[0] != 1 {
		t.Errorf("version byte = %d", ct[0])
	}
	pt, err := kr.DecryptString(ct)
	if err != nil || pt != "whsec_secret" {
		t.Fatalf("decrypt = %q, %v", pt, err)
	}

	// rotate: v2 becomes current, v1 still decrypts
	kr2, err := LoadKeyring(testKey + ",v2:1f1e1d1c1b1a19181716151413121110" + "0f0e0d0c0b0a09080706050403020100")
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := kr2.DecryptString(ct); err != nil || pt != "whsec_secret" {
		t.Fatalf("old ciphertext with rotated keyring = %q, %v", pt, err)
	}
	ct2, _ := kr2.EncryptString("x")
	if ct2[0] != 2 {
		t.Errorf("new ciphertext should use v2, got v%d", ct2[0])
	}
	if _, err := kr.Decrypt(ct2); err != ErrUnknownKeyID {
		t.Errorf("old keyring must reject v2 ciphertext, got %v", err)
	}

	ct[len(ct)-1] ^= 0xff
	if _, err := kr.Decrypt(ct); err != ErrCorrupt {
		t.Errorf("tampered ciphertext should be ErrCorrupt, got %v", err)
	}
}

func TestLoadKeyringRejectsBadInput(t *testing.T) {
	for _, spec := range []string{"", "zz", "v0:" + strings.Repeat("00", 32), strings.Repeat("00", 16)} {
		if _, err := LoadKeyring(spec); err == nil {
			t.Errorf("LoadKeyring(%q) should fail", spec)
		}
	}
}

func TestTokens(t *testing.T) {
	tok, err := Token("sk_live_")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "sk_live_") || len(tok) != len("sk_live_")+43 {
		t.Errorf("token shape %q", tok)
	}
	if got := DisplayPrefix(tok); got != tok[:12] {
		t.Errorf("display prefix = %q", got)
	}
	if len(Hash(tok)) != 32 || !HashEqual(Hash(tok), Hash(tok)) || HashEqual(Hash(tok), Hash(tok+"x")) {
		t.Error("hash behaviour")
	}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		s, _ := RandomString(16)
		if seen[s] {
			t.Fatal("random string repeated")
		}
		seen[s] = true
		for _, c := range s {
			if !strings.ContainsRune(alphabet, c) {
				t.Fatalf("char %q outside alphabet", c)
			}
		}
	}
}

func TestRecoveryCodes(t *testing.T) {
	c, err := RecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 14 || c[4] != '-' || c[9] != '-' {
		t.Errorf("code shape %q", c)
	}
	if NormalizeRecoveryCode(" "+strings.ToLower(c)+" ") != strings.ReplaceAll(c, "-", "") {
		t.Error("normalisation")
	}
}

func TestHMAC(t *testing.T) {
	key := []byte("k")
	sig := SignHMAC(key, []byte("msg"))
	if !VerifyHMAC(key, []byte("msg"), sig) || VerifyHMAC(key, []byte("msg2"), sig) || VerifyHMAC(key, []byte("msg"), "zz") {
		t.Error("hmac verify")
	}
}
