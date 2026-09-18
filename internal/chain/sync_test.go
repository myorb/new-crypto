package chain

import (
	"testing"

	"templ-app/internal/store"
)

func TestFinality(t *testing.T) {
	net := store.Network{RequiredConfirmations: 19}
	cases := []struct {
		name       string
		head, blk  int64
		in         store.TxStatus
		wantConf   int32
		wantStatus store.TxStatus
	}{
		{"just included", 100, 100, store.TxStatusPending, 1, store.TxStatusPending},
		{"one short", 117, 100, store.TxStatusPending, 18, store.TxStatusPending},
		{"at threshold", 118, 100, store.TxStatusPending, 19, store.TxStatusConfirmed},
		{"deep", 1000, 100, store.TxStatusPending, 901, store.TxStatusConfirmed},
		{"reverted stays failed", 1000, 100, store.TxStatusFailed, 901, store.TxStatusFailed},
		{"dropped stays dropped", 1000, 100, store.TxStatusDropped, 901, store.TxStatusDropped},
		{"ahead of head", 100, 105, store.TxStatusPending, 0, store.TxStatusPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conf, status := finality(net, c.head, c.blk, c.in)
			if conf != c.wantConf || status != c.wantStatus {
				t.Errorf("finality(head=%d, block=%d, %s) = %d/%s, want %d/%s", c.head, c.blk, c.in, conf, status, c.wantConf, c.wantStatus)
			}
		})
	}
}

// A network needing a single confirmation must confirm in the block itself.
func TestFinalitySingleConfirmation(t *testing.T) {
	conf, status := finality(store.Network{RequiredConfirmations: 1}, 500, 500, store.TxStatusPending)
	if conf != 1 || status != store.TxStatusConfirmed {
		t.Fatalf("got %d/%s, want 1/confirmed", conf, status)
	}
}

func TestStampFillsBlockFields(t *testing.T) {
	s := &Service{}
	block := Block{Height: 42, Hash: "blockhash", ParentHash: "parent"}
	obs := ObservedTransaction{Hash: "tx"}
	s.stamp(&obs, 7, 3, block, 42)
	if obs.ProviderID != 7 || obs.NetworkID != 3 {
		t.Fatalf("provider/network not stamped: %+v", obs)
	}
	if obs.BlockNumber == nil || *obs.BlockNumber != 42 {
		t.Fatalf("block number not stamped: %+v", obs.BlockNumber)
	}
	if obs.BlockHash == nil || *obs.BlockHash != "blockhash" {
		t.Fatalf("block hash not stamped: %+v", obs.BlockHash)
	}
	if obs.Status != store.TxStatusPending {
		t.Fatalf("status should default to pending, got %s", obs.Status)
	}

	// an adapter that already knows better is not overwritten
	height := int64(9)
	hash := "adapter-hash"
	obs2 := ObservedTransaction{Hash: "tx", BlockNumber: &height, BlockHash: &hash, Status: store.TxStatusFailed}
	s.stamp(&obs2, 7, 3, block, 42)
	if *obs2.BlockNumber != 9 || *obs2.BlockHash != "adapter-hash" || obs2.Status != store.TxStatusFailed {
		t.Fatalf("adapter values overwritten: %+v", obs2)
	}
}
