// Package devchain is a chain adapter for development and sandbox use. It
// derives deterministic addresses that satisfy each network's format rules
// but correspond to no real key, and it implements scanning and broadcasting
// against an in-memory fake chain (fakechain.go) whose head advances with the
// wall clock. Never register it in production: the addresses cannot receive
// or send funds and the transactions exist only in this process.
package devchain

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"

	"templ-app/internal/chain"
	"templ-app/internal/reference"
	"templ-app/internal/store"
)

const (
	base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	bech32Charset  = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
)

// Adapter implements chain.Adapter, chain.Scanner and chain.Broadcaster for
// one provider code, against a shared in-memory Chain.
type Adapter struct {
	code    string
	catalog *reference.Catalog
	chain   *Chain
}

// Register installs a dev adapter for every given provider code and returns
// the fake chain they share. Use the returned Chain to inject deposits
// (Deposit) and to make transactions fail (Drop, Reject).
func Register(reg *chain.Registry, catalog *reference.Catalog, opts Options, codes ...string) *Chain {
	fake := NewChain(opts)
	for _, c := range codes {
		reg.Register(&Adapter{code: c, catalog: catalog, chain: fake})
	}
	return fake
}

// Chain returns the fake chain behind this adapter.
func (a *Adapter) Chain() *Chain { return a.chain }

// Code implements chain.Adapter.
func (a *Adapter) Code() string { return a.code }

// DeriveAddress implements chain.Adapter with a hash of (wallet, index).
func (a *Adapter) DeriveAddress(ctx context.Context, wallet store.Wallet, index int64) (chain.DerivedAddress, error) {
	net, err := a.catalog.Network(ctx, wallet.NetworkID)
	if err != nil {
		return chain.DerivedAddress{}, err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(index))
	seed := sha256.Sum256(append(wallet.ID[:], buf[:]...))
	return chain.DerivedAddress{Address: Format(net.Family, seed[:])}, nil
}

// Format renders 32 seed bytes as an address in the network family's style.
func Format(family store.NetworkFamily, seed []byte) string {
	switch family {
	case store.NetworkFamilyEvm:
		return "0x" + hex.EncodeToString(seed[:20])
	case store.NetworkFamilyTron:
		return "T" + pad(base58(seed), 33, '1')[:33]
	case store.NetworkFamilyBitcoin:
		out := make([]byte, 39)
		for i := range out {
			out[i] = bech32Charset[int(seed[i%len(seed)])%len(bech32Charset)]
		}
		return "bc1q" + string(out)
	case store.NetworkFamilySolana:
		return pad(base58(seed), 44, '1')[:44]
	default:
		return pad(base58(seed), 40, '1')[:40]
	}
}

func base58(b []byte) string {
	n := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, radix, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func pad(s string, n int, c byte) string {
	for len(s) < n {
		s = string(c) + s
	}
	return s
}

// String helps logs identify the adapter.
func (a *Adapter) String() string { return fmt.Sprintf("devchain(%s)", a.code) }
