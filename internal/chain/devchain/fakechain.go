package devchain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/store"
)

// Genesis is the height the fake chain starts at, high enough that block
// numbers look like a real chain's.
const Genesis int64 = 1_000_000

// Options tune the fake chain.
type Options struct {
	// BlockTime is how fast the fake head advances; one block per BlockTime.
	// A network confirms after required_confirmations blocks, so the default
	// of a second means Tron deposits confirm in about 19 seconds.
	BlockTime time.Duration
	// NetworkFee is the native-coin fee reported for every transaction.
	NetworkFee decimal.Decimal
}

func (o *Options) defaults() {
	if o.BlockTime <= 0 {
		o.BlockTime = time.Second
	}
	if o.NetworkFee.IsZero() {
		o.NetworkFee = decimal.RequireFromString("0.01")
	}
}

// Chain is an in-memory blockchain: its head advances with the wall clock and
// its blocks contain only the transactions this process broadcast or injected.
// One Chain is shared by every dev adapter so a payout broadcast through one
// provider is visible to the scanner of another on the same network.
type Chain struct {
	opts    Options
	started time.Time
	base    int64 // height at started; raised by StartAbove

	mu       sync.Mutex
	nets     map[int16]*netState
	rejected map[string]bool // broadcast references the chain refuses
}

type netState struct {
	blocks map[int64][]chain.ObservedTransaction
	mined  map[string]int64  // hash -> height
	refs   map[string]string // broadcast reference -> hash
	status map[string]store.TxStatus
}

// NewChain builds an empty fake chain.
func NewChain(opts Options) *Chain {
	opts.defaults()
	return &Chain{opts: opts, started: time.Now(), base: Genesis, nets: map[int16]*netState{}, rejected: map[string]bool{}}
}

// StartAbove lifts the head above a height already recorded in the database.
// The fake chain lives in memory and would otherwise restart at Genesis on
// every boot, leaving scanners that stored a higher cursor waiting for a head
// that never arrives. The app calls this with the highest stored cursor.
func (c *Chain) StartAbove(height int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if height < c.base {
		return
	}
	c.base = height + 1
	c.started = time.Now()
}

func (c *Chain) net(id int16) *netState {
	s, ok := c.nets[id]
	if !ok {
		s = &netState{blocks: map[int64][]chain.ObservedTransaction{}, mined: map[string]int64{}, refs: map[string]string{}, status: map[string]store.TxStatus{}}
		c.nets[id] = s
	}
	return s
}

// Head is the current height: the base plus one block per BlockTime elapsed.
func (c *Chain) Head() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.head()
}

// head is Head for callers that already hold the lock.
func (c *Chain) head() int64 {
	return c.base + int64(time.Since(c.started)/c.opts.BlockTime)
}

// blockTime is when a height was (or will be) mined; callers hold the lock.
func (c *Chain) blockTime(height int64) time.Time {
	return c.started.Add(time.Duration(height-c.base) * c.opts.BlockTime)
}

// BlockHash is the deterministic hash of a height on a network.
func (c *Chain) BlockHash(networkID int16, height int64) string {
	var buf [10]byte
	binary.BigEndian.PutUint16(buf[0:2], uint16(networkID))
	binary.BigEndian.PutUint64(buf[2:10], uint64(height))
	sum := sha256.Sum256(buf[:])
	return hex.EncodeToString(sum[:])
}

// Deposit injects an incoming transfer to one of our addresses, as if a
// customer had paid. It is mined into the next block and the scanner picks it
// up from there. This is the dev faucet: nothing else creates deposits.
func (c *Chain) Deposit(net store.Network, assetID int16, to string, memo *string, amount decimal.Decimal) chain.ObservedTransaction {
	var seed [16]byte
	_, _ = rand.Read(seed[:])
	payerSeed := sha256.Sum256(seed[:])
	payer := Format(net.Family, payerSeed[:])
	return c.mine(net, chain.ObservedTransaction{
		From: &payer, To: &to,
		Transfers: []chain.ObservedTransfer{{AssetID: assetID, LogIndex: 0, From: &payer, To: to, ToMemo: memo, Amount: amount}},
	}, seed[:])
}

// mine stamps a transaction with a hash and fee and puts it in the next block.
func (c *Chain) mine(net store.Network, obs chain.ObservedTransaction, seed []byte) chain.ObservedTransaction {
	c.mu.Lock()
	defer c.mu.Unlock()
	height := c.head() + 1
	sum := sha256.Sum256(append(seed, []byte(fmt.Sprint(net.ID, height))...))
	obs.Hash = FormatHash(net.Family, sum[:])
	obs.NetworkID = net.ID
	obs.Status = store.TxStatusPending
	obs.BlockNumber = &height
	hash := c.BlockHash(net.ID, height)
	obs.BlockHash = &hash
	ts := c.blockTime(height)
	obs.BlockTimestamp = &ts
	fee := c.opts.NetworkFee
	obs.FeeNative = &fee

	s := c.net(net.ID)
	s.blocks[height] = append(s.blocks[height], obs)
	s.mined[obs.Hash] = height
	s.status[obs.Hash] = store.TxStatusPending
	return obs
}

// MinedAt returns the height a hash was mined at, which may still be ahead of
// the head (the transaction is then the fake chain's mempool).
func (c *Chain) MinedAt(networkID int16, hash string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.net(networkID).mined[hash]
	return h, ok
}

// mempoolView is what a node returns for a transaction it has accepted but
// not yet included: no block fields.
func mempoolView(obs chain.ObservedTransaction) chain.ObservedTransaction {
	obs.BlockNumber, obs.BlockHash, obs.BlockTimestamp = nil, nil, nil
	return obs
}

// Drop marks a mined transaction as dropped so the reconcilers see a payout
// or deposit fail. Reports whether the hash was known.
func (c *Chain) Drop(networkID int16, hash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.net(networkID)
	if _, ok := s.mined[hash]; !ok {
		return false
	}
	s.status[hash] = store.TxStatusDropped
	return true
}

// Reject makes the next broadcast carrying this reference fail as if the
// network refused it.
func (c *Chain) Reject(reference string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rejected[reference] = true
}

// Scanner ---------------------------------------------------------------------------

// Head implements chain.Scanner.
func (a *Adapter) Head(ctx context.Context, net store.Network) (int64, error) {
	return a.chain.Head(), nil
}

// Earliest implements chain.Scanner. The fake chain has no history before the
// height it started at, like a pruned node.
func (a *Adapter) Earliest(ctx context.Context, net store.Network) (int64, error) {
	c := a.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base, nil
}

// ScanBlock implements chain.Scanner. The fake chain only ever holds our own
// transactions, so the watched list is not needed to filter.
func (a *Adapter) ScanBlock(ctx context.Context, net store.Network, height int64, _ []chain.WatchedAddress) (chain.Block, error) {
	c := a.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.net(net.ID)
	txs := make([]chain.ObservedTransaction, 0, len(s.blocks[height]))
	for _, tx := range s.blocks[height] {
		tx.Status = s.status[tx.Hash]
		txs = append(txs, tx)
	}
	return chain.Block{
		Height: height, Hash: c.BlockHash(net.ID, height), ParentHash: c.BlockHash(net.ID, height-1),
		Timestamp: c.blockTime(height), Transactions: txs,
	}, nil
}

// TransactionStatus implements chain.Scanner. A transaction the fake chain
// never mined reads as dropped, which is what a real node reports for a hash
// it has forgotten.
func (a *Adapter) TransactionStatus(ctx context.Context, net store.Network, tx store.Transaction) (chain.TxState, error) {
	c := a.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.net(net.ID)
	height, ok := s.mined[tx.Hash]
	if !ok {
		return chain.TxState{Status: store.TxStatusDropped}, nil
	}
	if st := s.status[tx.Hash]; st == store.TxStatusDropped || st == store.TxStatusFailed {
		return chain.TxState{Status: st}, nil
	}
	fee := c.opts.NetworkFee
	state := chain.TxState{Status: store.TxStatusPending, FeeNative: &fee}
	if height <= c.head() {
		hash := c.BlockHash(net.ID, height)
		state.BlockNumber, state.BlockHash = &height, &hash
	}
	return state, nil
}

// Broadcaster ------------------------------------------------------------------------

// Broadcast implements chain.Broadcaster. Repeating a reference returns the
// transaction already mined for it instead of sending a second one.
func (a *Adapter) Broadcast(ctx context.Context, req chain.BroadcastRequest) (chain.ObservedTransaction, error) {
	c := a.chain
	c.mu.Lock()
	if c.rejected[req.Reference] {
		delete(c.rejected, req.Reference)
		c.mu.Unlock()
		return chain.ObservedTransaction{}, fmt.Errorf("%w: devchain was told to reject %s", chain.ErrBroadcastRejected, req.Reference)
	}
	s := c.net(req.Network.ID)
	if hash, ok := s.refs[req.Reference]; ok {
		if height, ok := s.mined[hash]; ok {
			for _, tx := range s.blocks[height] {
				if tx.Hash == hash {
					c.mu.Unlock()
					return mempoolView(tx), nil
				}
			}
		}
	}
	c.mu.Unlock()

	seed := sha256.Sum256([]byte(req.Reference))
	obs := c.mine(req.Network, chain.ObservedTransaction{
		From: &req.From.Address, To: &req.To,
		Transfers: []chain.ObservedTransfer{{
			AssetID: req.Asset.ID, LogIndex: 0, From: &req.From.Address, To: req.To, ToMemo: req.ToMemo, Amount: req.Amount,
		}},
	}, seed[:])
	c.mu.Lock()
	c.net(req.Network.ID).refs[req.Reference] = obs.Hash
	c.mu.Unlock()
	return mempoolView(obs), nil
}

// FormatHash renders seed bytes as a transaction hash in the family's style,
// matching the networks.tx_hash_regex seeded in 00009.
func FormatHash(family store.NetworkFamily, seed []byte) string {
	switch family {
	case store.NetworkFamilyEvm:
		return "0x" + hex.EncodeToString(seed[:32])
	case store.NetworkFamilySolana:
		double := sha256.Sum256(append([]byte("sig"), seed...))
		return pad(base58(append(seed[:32], double[:]...)), 87, '1')[:87]
	default: // tron, bitcoin: 64 lowercase hex
		return hex.EncodeToString(seed[:32])
	}
}
