package chain

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"templ-app/internal/reference"
	"templ-app/internal/store"
)

var (
	ErrNotScanner     = errors.New("chain: adapter cannot scan blocks")
	ErrNotBroadcaster = errors.New("chain: adapter cannot broadcast transactions")
	// ErrBroadcastRejected is wrapped by adapters when the network or provider
	// definitively refused a transaction (bad address, insufficient funds at the
	// provider, policy). Callers fail the underlying payout; any other error is
	// transient and the payout stays queued for the next attempt.
	ErrBroadcastRejected = errors.New("chain: transaction rejected")
)

// Scanner is implemented by adapters that can read a network. The Service
// drives it from the stored cursor (Scan, TrackPending); adapters report raw
// facts and never compute confirmations or finality themselves.
type Scanner interface {
	// Head returns the latest block height the provider knows about.
	Head(ctx context.Context, network store.Network) (int64, error)
	// Earliest returns the oldest block the provider can still serve. Archive
	// nodes return 0; pruned nodes and most hosted RPCs only keep a recent
	// window. A cursor older than this cannot be caught up, so the Service
	// skips forward rather than spinning on blocks nobody can fetch.
	Earliest(ctx context.Context, network store.Network) (int64, error)
	// ScanBlock returns the block at height with the transactions that move
	// value to or from any watched address. Adapters may return more (the
	// Service drops transfers that touch none of our addresses) but must not
	// return less. Transaction.Status is pending for included transactions
	// and failed for reverted ones; block fields may be left empty.
	ScanBlock(ctx context.Context, network store.Network, height int64, watched []WatchedAddress) (Block, error)
	// TransactionStatus re-checks a transaction the Service already stored,
	// for the confirmation tracker and for transactions we broadcast.
	TransactionStatus(ctx context.Context, network store.Network, tx store.Transaction) (TxState, error)
}

// Block is what ScanBlock returns.
type Block struct {
	Height       int64
	Hash         string
	ParentHash   string // empty when the provider does not expose it; disables reorg detection
	Timestamp    time.Time
	Transactions []ObservedTransaction
}

// TxState is the current on-chain state of one transaction.
type TxState struct {
	// Status is pending while in the mempool or included but not reverted,
	// failed when reverted, dropped when the network no longer knows the hash.
	// Adapters never return confirmed; the Service decides finality.
	Status      store.TxStatus
	BlockNumber *int64 // nil while still in the mempool
	BlockHash   *string
	FeeNative   *decimal.Decimal
}

// Broadcaster is implemented by adapters that can move funds out of a
// platform address: self-custody adapters sign with the wallet's key
// reference, custodial ones ask the provider to send.
type Broadcaster interface {
	// Broadcast builds, signs and submits a transfer. Reference is unique per
	// business object ("withdrawal:<id>") and must make the call idempotent:
	// a retry with the same Reference returns the already sent transaction
	// instead of paying twice. The returned observation needs Hash, From, To
	// and either Transfers or nothing (the Service fills one from the request).
	Broadcast(ctx context.Context, req BroadcastRequest) (ObservedTransaction, error)
}

// BroadcastRequest is everything an adapter needs to send.
type BroadcastRequest struct {
	Network   store.Network
	Asset     reference.Asset
	Wallet    store.Wallet  // KeyRef / ExternalWalletID for signing
	From      store.Address // hot, fee or deposit (sweeps) address
	To        string
	ToMemo    *string
	Amount    decimal.Decimal
	Reference string
}

// All returns every registered adapter; the worker uses it to decide which
// loops to start.
func (r *Registry) All() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a)
	}
	return out
}
