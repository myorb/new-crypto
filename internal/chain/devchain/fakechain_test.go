package devchain

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/store"
)

func tron() store.Network {
	return store.Network{ID: 1, Code: "tron", Family: store.NetworkFamilyTron, RequiredConfirmations: 19}
}

func newAdapter(t *testing.T, blockTime time.Duration) (*Adapter, *Chain) {
	t.Helper()
	reg := chain.NewRegistry()
	c := Register(reg, nil, Options{BlockTime: blockTime}, "tron_rpc")
	a, ok := reg.Get("tron_rpc")
	if !ok {
		t.Fatal("adapter was not registered")
	}
	return a.(*Adapter), c
}

func TestAdapterImplementsScannerAndBroadcaster(t *testing.T) {
	a, _ := newAdapter(t, time.Second)
	if _, ok := interface{}(a).(chain.Scanner); !ok {
		t.Error("devchain should implement chain.Scanner")
	}
	if _, ok := interface{}(a).(chain.Broadcaster); !ok {
		t.Error("devchain should implement chain.Broadcaster")
	}
}

func TestFormatHashMatchesNetworkRules(t *testing.T) {
	// The regexes are the ones seeded into networks.tx_hash_regex in 00009.
	rules := map[store.NetworkFamily]string{
		store.NetworkFamilyTron:    `^[0-9a-f]{64}$`,
		store.NetworkFamilyEvm:     `^0x[0-9a-f]{64}$`,
		store.NetworkFamilyBitcoin: `^[0-9a-f]{64}$`,
		store.NetworkFamilySolana:  `^[1-9A-HJ-NP-Za-km-z]{64,88}$`,
	}
	for i := 0; i < 100; i++ {
		seed := sha256.Sum256([]byte{byte(i), byte(i >> 8)})
		for fam, re := range rules {
			hash := FormatHash(fam, seed[:])
			if !regexp.MustCompile(re).MatchString(hash) {
				t.Errorf("%s: %q does not match %s", fam, hash, re)
			}
		}
	}
}

func TestHeadAdvancesWithTheClock(t *testing.T) {
	_, c := newAdapter(t, time.Millisecond)
	first := c.Head()
	if first < Genesis {
		t.Fatalf("head %d should start at or above genesis %d", first, Genesis)
	}
	time.Sleep(25 * time.Millisecond)
	if second := c.Head(); second <= first {
		t.Fatalf("head did not advance: %d then %d", first, second)
	}
}

func TestDepositIsMinedAndScannable(t *testing.T) {
	ctx := context.Background()
	a, c := newAdapter(t, time.Millisecond)
	net := tron()
	to := "TQ" + "1234567890123456789012345678901"

	obs := c.Deposit(net, 5, to, nil, decimal.RequireFromString("100"))
	if obs.BlockNumber == nil {
		t.Fatal("deposit should be mined into a block")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(obs.Hash) {
		t.Fatalf("deposit hash %q is not a valid tron hash", obs.Hash)
	}
	height := *obs.BlockNumber

	block, err := a.ScanBlock(ctx, net, height, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0].Hash != obs.Hash {
		t.Fatalf("block %d should contain the deposit, got %+v", height, block.Transactions)
	}
	if block.Transactions[0].Transfers[0].To != to {
		t.Fatalf("transfer should land on %s, got %+v", to, block.Transactions[0].Transfers[0])
	}
	if block.ParentHash != c.BlockHash(net.ID, height-1) {
		t.Error("parent hash should chain to the previous block")
	}
	if empty, err := a.ScanBlock(ctx, net, height+50_000, nil); err != nil || len(empty.Transactions) != 0 {
		t.Fatalf("an empty block should scan clean: %+v %v", empty.Transactions, err)
	}
}

func TestTransactionStatusLifecycle(t *testing.T) {
	ctx := context.Background()
	a, c := newAdapter(t, 50*time.Millisecond)
	net := tron()
	obs := c.Deposit(net, 5, "TQ1234567890123456789012345678901", nil, decimal.RequireFromString("1"))
	stored := store.Transaction{Hash: obs.Hash}

	// mined into a block the head has not reached: still in the mempool
	state, err := a.TransactionStatus(ctx, net, stored)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != store.TxStatusPending || state.BlockNumber != nil {
		t.Fatalf("before the head reaches it the tx should be pending with no block, got %+v", state)
	}

	// once the head passes the block it reports its height and fee
	deadline := time.Now().Add(2 * time.Second)
	for c.Head() < *obs.BlockNumber && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	state, err = a.TransactionStatus(ctx, net, stored)
	if err != nil {
		t.Fatal(err)
	}
	if state.BlockNumber == nil || *state.BlockNumber != *obs.BlockNumber {
		t.Fatalf("included tx should report its block, got %+v", state.BlockNumber)
	}
	if state.FeeNative == nil || !state.FeeNative.IsPositive() {
		t.Fatalf("included tx should report a fee, got %+v", state.FeeNative)
	}
	if state.Status != store.TxStatusPending {
		t.Fatalf("adapters never report confirmed; the service decides. got %s", state.Status)
	}

	// a hash the chain never mined reads as dropped
	unknown, err := a.TransactionStatus(ctx, net, store.Transaction{Hash: "ff"})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Status != store.TxStatusDropped {
		t.Fatalf("unknown hash should be dropped, got %s", unknown.Status)
	}

	// Drop marks a mined transaction as gone
	if !c.Drop(net.ID, obs.Hash) {
		t.Fatal("Drop should report the hash was known")
	}
	if state, _ = a.TransactionStatus(ctx, net, stored); state.Status != store.TxStatusDropped {
		t.Fatalf("dropped tx should read dropped, got %s", state.Status)
	}
}

func broadcastReq(net store.Network, ref string) chain.BroadcastRequest {
	return chain.BroadcastRequest{
		Network: net,
		From:    store.Address{Address: "TFrom1234567890123456789012345678"},
		To:      "TTo123456789012345678901234567890",
		Amount:  decimal.RequireFromString("50"),

		Reference: ref,
	}
}

func TestBroadcastIsIdempotentPerReference(t *testing.T) {
	ctx := context.Background()
	a, c := newAdapter(t, time.Millisecond)
	net := tron()

	first, err := a.Broadcast(ctx, broadcastReq(net, "withdrawal:abc"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Broadcast(ctx, broadcastReq(net, "withdrawal:abc"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("retrying a reference must not send twice: %s then %s", first.Hash, second.Hash)
	}
	if first.BlockNumber != nil {
		t.Error("a freshly broadcast transaction should look like a mempool one, with no block")
	}
	height, ok := c.MinedAt(net.ID, first.Hash)
	if !ok {
		t.Fatal("the broadcast transaction should be known to the chain")
	}
	block, err := a.ScanBlock(ctx, net, height, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("the retry created a second transaction: %+v", block.Transactions)
	}

	other, err := a.Broadcast(ctx, broadcastReq(net, "withdrawal:xyz"))
	if err != nil {
		t.Fatal(err)
	}
	if other.Hash == first.Hash {
		t.Fatal("different references must produce different transactions")
	}
	if c.Head() < Genesis {
		t.Fatal("head went backwards")
	}
}

func TestBroadcastRejection(t *testing.T) {
	ctx := context.Background()
	a, c := newAdapter(t, time.Millisecond)
	net := tron()

	c.Reject("withdrawal:doomed")
	_, err := a.Broadcast(ctx, broadcastReq(net, "withdrawal:doomed"))
	if !errors.Is(err, chain.ErrBroadcastRejected) {
		t.Fatalf("want ErrBroadcastRejected, got %v", err)
	}
	// the rejection is one-shot: a later retry goes through
	if _, err := a.Broadcast(ctx, broadcastReq(net, "withdrawal:doomed")); err != nil {
		t.Fatalf("second attempt should succeed, got %v", err)
	}
}

func TestNetworksAreIsolated(t *testing.T) {
	ctx := context.Background()
	a, c := newAdapter(t, time.Millisecond)
	tronNet := tron()
	evm := store.Network{ID: 2, Code: "ethereum", Family: store.NetworkFamilyEvm, RequiredConfirmations: 12}

	obs := c.Deposit(tronNet, 5, "TQ1234567890123456789012345678901", nil, decimal.RequireFromString("1"))
	block, err := a.ScanBlock(ctx, evm, *obs.BlockNumber, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("a tron deposit must not appear on ethereum: %+v", block.Transactions)
	}
	state, _ := a.TransactionStatus(ctx, evm, store.Transaction{Hash: obs.Hash})
	if state.Status != store.TxStatusDropped {
		t.Fatalf("the hash is unknown on ethereum, want dropped, got %s", state.Status)
	}
}
