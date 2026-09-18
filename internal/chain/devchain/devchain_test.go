package devchain

import (
	"crypto/sha256"
	"regexp"
	"testing"

	"templ-app/internal/store"
)

// The regexes are the ones seeded into networks.address_regex in 00009.
func TestFormatMatchesNetworkRules(t *testing.T) {
	rules := map[store.NetworkFamily]string{
		store.NetworkFamilyTron:    `^T[1-9A-HJ-NP-Za-km-z]{33}$`,
		store.NetworkFamilyEvm:     `^0x[0-9a-fA-F]{40}$`,
		store.NetworkFamilyBitcoin: `^(bc1[02-9ac-hj-np-z]{11,87}|[13][a-km-zA-HJ-NP-Z1-9]{25,34})$`,
		store.NetworkFamilySolana:  `^[1-9A-HJ-NP-Za-km-z]{32,44}$`,
	}
	for i := 0; i < 50; i++ {
		seed := sha256.Sum256([]byte{byte(i)})
		for fam, re := range rules {
			addr := Format(fam, seed[:])
			if !regexp.MustCompile(re).MatchString(addr) {
				t.Errorf("%s: %q does not match %s", fam, addr, re)
			}
		}
	}
	a, b := sha256.Sum256([]byte{1}), sha256.Sum256([]byte{2})
	if Format(store.NetworkFamilyTron, a[:]) == Format(store.NetworkFamilyTron, b[:]) {
		t.Error("different seeds should give different addresses")
	}
}
