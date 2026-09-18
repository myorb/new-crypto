package chain

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"templ-app/internal/store"
)

// Scanning ---------------------------------------------------------------------------

// ScanStats reports one Scan call.
type ScanStats struct {
	From, To     int64 // first and last block scanned (To < From when nothing was)
	Head         int64
	Transactions int
	Rewound      bool // a reorg was detected and the cursor moved back
	// Skipped counts blocks the provider can no longer serve, which the
	// cursor jumped over. Anything but zero means history was not scanned.
	Skipped int64
}

// Scan advances the cursor of one provider on one network: it asks the
// adapter for the head, scans at most maxBlocks new blocks, records every
// transaction that touches our addresses and saves the cursor after each
// block. The first run on a network starts at the head rather than replaying
// history. If a block's parent hash does not match the stored cursor the
// cursor is rewound by the network's confirmation depth and the call returns;
// RecordTransaction is idempotent so re-scanning is safe.
func (s *Service) Scan(ctx context.Context, providerID, networkID int16, maxBlocks int) (ScanStats, error) {
	if maxBlocks <= 0 {
		maxBlocks = 100
	}
	scanner, net, err := s.scanner(ctx, providerID, networkID)
	if err != nil {
		return ScanStats{}, err
	}
	cur, err := s.Cursor(ctx, providerID, networkID)
	if err != nil {
		return ScanStats{}, err
	}
	head, err := scanner.Head(ctx, net)
	if err != nil {
		return ScanStats{}, fmt.Errorf("chain: head: %w", err)
	}
	st := ScanStats{Head: head, From: cur.LastScannedBlock + 1, To: cur.LastScannedBlock}
	if cur.UpdatedAt.IsZero() {
		// never ran: start from the head rather than from genesis
		st.From, st.To = head+1, head
		return st, s.SaveCursor(ctx, providerID, networkID, head, nil, nil)
	}
	if cur.LastScannedBlock > head {
		// The provider is serving an older head than the one we scanned from,
		// usually a failover to a lagging node. Waiting is right: rewinding
		// would re-scan everything each time the lagging node answers.
		s.log.WarnContext(ctx, "chain: provider head is behind the cursor", "network", net.Code,
			"head", head, "cursor", cur.LastScannedBlock)
		return st, nil
	}
	if cur.LastScannedBlock == head {
		return st, nil
	}
	earliest, err := scanner.Earliest(ctx, net)
	if err != nil {
		return st, fmt.Errorf("chain: earliest block: %w", err)
	}
	if cur.LastScannedBlock+1 < earliest {
		// The provider pruned everything up to earliest; those blocks cannot
		// be fetched from it at all, so grinding through them would spin
		// forever. Skip them loudly: deposits in that range are not seen.
		st.Skipped = earliest - 1 - cur.LastScannedBlock
		s.log.WarnContext(ctx, "chain: skipping history the provider cannot serve", "network", net.Code,
			"cursor", cur.LastScannedBlock, "earliest", earliest, "skipped", st.Skipped)
		cur.LastScannedBlock, cur.LastScannedHash = earliest-1, nil
		st.From = earliest
		if err := s.SaveCursor(ctx, providerID, networkID, cur.LastScannedBlock, nil, nil); err != nil {
			return st, err
		}
		if cur.LastScannedBlock >= head {
			st.To = cur.LastScannedBlock
			return st, nil
		}
	}
	watched, err := s.WatchedAddresses(ctx, networkID)
	if err != nil {
		return st, err
	}
	for h := cur.LastScannedBlock + 1; h <= head && h-cur.LastScannedBlock <= int64(maxBlocks); h++ {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		block, err := scanner.ScanBlock(ctx, net, h, watched)
		if err != nil {
			return st, fmt.Errorf("chain: scan block %d: %w", h, err)
		}
		if h == cur.LastScannedBlock+1 && cur.LastScannedHash != nil && block.ParentHash != "" && block.ParentHash != *cur.LastScannedHash {
			back := max(cur.LastScannedBlock-int64(net.RequiredConfirmations), 0)
			s.log.WarnContext(ctx, "chain: reorg detected, rewinding cursor", "network", net.Code, "from", cur.LastScannedBlock, "to", back)
			st.Rewound = true
			return st, s.SaveCursor(ctx, providerID, networkID, back, nil, nil)
		}
		for _, obs := range block.Transactions {
			s.stamp(&obs, providerID, networkID, block, h)
			obs.Confirmations, obs.Status = finality(net, head, h, obs.Status)
			if _, err := s.RecordTransaction(ctx, obs); err != nil {
				return st, err
			}
			st.Transactions++
		}
		var hash *string
		if block.Hash != "" {
			hash = &block.Hash
		}
		if err := s.SaveCursor(ctx, providerID, networkID, h, hash, nil); err != nil {
			return st, err
		}
		st.To = h
	}
	return st, nil
}

// stamp fills in the fields a scanner is allowed to leave empty.
func (s *Service) stamp(obs *ObservedTransaction, providerID, networkID int16, block Block, height int64) {
	obs.NetworkID, obs.ProviderID = networkID, providerID
	if obs.BlockNumber == nil {
		obs.BlockNumber = &height
	}
	if obs.BlockHash == nil && block.Hash != "" {
		h := block.Hash
		obs.BlockHash = &h
	}
	if obs.BlockTimestamp == nil && !block.Timestamp.IsZero() {
		t := block.Timestamp
		obs.BlockTimestamp = &t
	}
	if obs.Status == "" {
		obs.Status = store.TxStatusPending
	}
}

// finality turns a block position into a confirmation count and status.
// Failed and dropped transactions keep their status whatever the depth.
func finality(net store.Network, head, block int64, status store.TxStatus) (int32, store.TxStatus) {
	depth := head - block + 1
	if depth < 0 {
		depth = 0
	}
	if depth > math.MaxInt32 {
		depth = math.MaxInt32
	}
	conf := int32(depth)
	switch status {
	case store.TxStatusFailed, store.TxStatusDropped:
		return conf, status
	}
	if conf >= net.RequiredConfirmations {
		return conf, store.TxStatusConfirmed
	}
	return conf, store.TxStatusPending
}

func (s *Service) scanner(ctx context.Context, providerID, networkID int16) (Scanner, store.Network, error) {
	provider, err := s.catalog.Provider(ctx, providerID)
	if err != nil {
		return nil, store.Network{}, err
	}
	adapter, ok := s.adapters.Get(provider.Code)
	if !ok {
		return nil, store.Network{}, fmt.Errorf("%w: %s", ErrNoAdapter, provider.Code)
	}
	scanner, ok := adapter.(Scanner)
	if !ok {
		return nil, store.Network{}, fmt.Errorf("%w: %s", ErrNotScanner, provider.Code)
	}
	net, err := s.catalog.Network(ctx, networkID)
	if err != nil {
		return nil, store.Network{}, err
	}
	return scanner, net, nil
}

// Confirmation tracking --------------------------------------------------------------

// TrackStats reports one TrackPending call.
type TrackStats struct {
	Checked, Updated, Confirmed, Failed int
}

// TrackPending re-checks the pending transactions on a network with their
// providers and updates depth and status. This is what moves a deposit or a
// broadcast payout to confirmed when the scanner did not see the block
// itself (transactions we sent, or blocks skipped by a rewind).
func (s *Service) TrackPending(ctx context.Context, networkID int16, limit int32) (TrackStats, error) {
	pending, err := s.PendingTransactions(ctx, networkID, limit)
	if err != nil {
		return TrackStats{}, err
	}
	st := TrackStats{Checked: len(pending)}
	heads := map[int16]int64{}
	scanners := map[int16]Scanner{}
	skip := map[int16]bool{} // providers that report through callbacks, not scanning
	var net store.Network
	for _, tx := range pending {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		if skip[tx.ProviderID] {
			continue
		}
		scanner, ok := scanners[tx.ProviderID]
		if !ok {
			var err error
			if scanner, net, err = s.scanner(ctx, tx.ProviderID, networkID); err != nil {
				if errors.Is(err, ErrNotScanner) || errors.Is(err, ErrNoAdapter) {
					skip[tx.ProviderID] = true
					continue
				}
				return st, err
			}
			head, err := scanner.Head(ctx, net)
			if err != nil {
				return st, fmt.Errorf("chain: head: %w", err)
			}
			scanners[tx.ProviderID], heads[tx.ProviderID] = scanner, head
		}
		state, err := scanner.TransactionStatus(ctx, net, tx)
		if err != nil {
			s.log.WarnContext(ctx, "chain: transaction status failed", "tx", tx.Hash, "err", err)
			continue
		}
		var conf int32
		var status store.TxStatus
		switch {
		case state.Status == store.TxStatusFailed || state.Status == store.TxStatusDropped:
			conf, status = tx.Confirmations, state.Status
		case state.BlockNumber != nil:
			conf, status = finality(net, heads[tx.ProviderID], *state.BlockNumber, store.TxStatusPending)
		default:
			continue // still in the mempool
		}
		if status == store.TxStatusPending && conf == tx.Confirmations {
			continue
		}
		if _, err := s.UpdateConfirmations(ctx, tx.ID, conf, state.BlockNumber, status); err != nil {
			return st, err
		}
		st.Updated++
		switch status {
		case store.TxStatusConfirmed:
			st.Confirmed++
		case store.TxStatusFailed, store.TxStatusDropped:
			st.Failed++
		}
	}
	return st, nil
}

// Broadcasting -----------------------------------------------------------------------

// SendRequest asks the platform to move funds from one of its addresses.
type SendRequest struct {
	FromAddressID uuid.UUID
	AssetID       int16
	To            string
	ToMemo        *string
	Amount        decimal.Decimal
	// Reference identifies the business object ("withdrawal:<id>"); adapters
	// use it as the provider-side idempotency key.
	Reference string
}

// Send broadcasts a transfer through the from-address's provider and records
// the resulting pending transaction so the confirmation tracker picks it up.
// Callers then link the transaction to their payout (MarkBroadcast).
func (s *Service) Send(ctx context.Context, req SendRequest) (Observation, error) {
	if !req.Amount.IsPositive() {
		return Observation{}, errors.New("chain: send amount must be positive")
	}
	if req.Reference == "" {
		return Observation{}, errors.New("chain: send needs a reference")
	}
	from, err := s.Address(ctx, req.FromAddressID)
	if err != nil {
		return Observation{}, err
	}
	wallet, err := s.Wallet(ctx, from.WalletID)
	if err != nil {
		return Observation{}, err
	}
	asset, err := s.catalog.Asset(ctx, req.AssetID)
	if err != nil {
		return Observation{}, err
	}
	if asset.NetworkID != from.NetworkID {
		return Observation{}, fmt.Errorf("chain: asset %s is not on the from-address network", asset.Code)
	}
	provider, err := s.catalog.Provider(ctx, from.ProviderID)
	if err != nil {
		return Observation{}, err
	}
	adapter, ok := s.adapters.Get(provider.Code)
	if !ok {
		return Observation{}, fmt.Errorf("%w: %s", ErrNoAdapter, provider.Code)
	}
	broadcaster, ok := adapter.(Broadcaster)
	if !ok {
		return Observation{}, fmt.Errorf("%w: %s", ErrNotBroadcaster, provider.Code)
	}
	obs, err := broadcaster.Broadcast(ctx, BroadcastRequest{
		Network: asset.Network, Asset: asset, Wallet: wallet, From: from, To: req.To, ToMemo: req.ToMemo, Amount: req.Amount, Reference: req.Reference,
	})
	if err != nil {
		return Observation{}, fmt.Errorf("chain: broadcast: %w", err)
	}
	if obs.Hash == "" {
		return Observation{}, errors.New("chain: adapter returned no transaction hash")
	}
	obs.NetworkID, obs.ProviderID = from.NetworkID, from.ProviderID
	if obs.From == nil {
		f := from.Address
		obs.From = &f
	}
	if obs.To == nil {
		t := req.To
		obs.To = &t
	}
	if obs.Status == "" {
		obs.Status = store.TxStatusPending
	}
	if len(obs.Transfers) == 0 {
		f := from.Address
		obs.Transfers = []ObservedTransfer{{AssetID: req.AssetID, LogIndex: 0, From: &f, To: req.To, ToMemo: req.ToMemo, Amount: req.Amount}}
	}
	out, err := s.RecordTransaction(ctx, obs)
	if err != nil {
		// The funds are already moving; failing to record it is what the
		// confirmation tracker cannot recover from, so say which hash it was.
		return Observation{}, fmt.Errorf("chain: broadcast %s was sent but not recorded: %w", obs.Hash, err)
	}
	return out, nil
}

// Targets ------------------------------------------------------------------------------

// ScanTarget is one provider on one network that the worker can scan.
type ScanTarget struct {
	Provider store.PaymentProvider
	Network  store.Network
}

// ScanTargets lists the enabled provider / network pairs whose adapter
// implements Scanner. The worker calls this on every pass so enabling a
// provider takes effect without a restart.
func (s *Service) ScanTargets(ctx context.Context) ([]ScanTarget, error) {
	nets, err := s.catalog.Networks(ctx)
	if err != nil {
		return nil, err
	}
	var out []ScanTarget
	for _, n := range nets {
		routes, err := s.catalog.ProvidersForNetwork(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			if !r.IsEnabled || !r.SupportsDeposits {
				continue
			}
			provider, err := s.catalog.Provider(ctx, r.ProviderID)
			if err != nil || !provider.IsEnabled {
				continue
			}
			adapter, ok := s.adapters.Get(provider.Code)
			if !ok {
				continue
			}
			if _, ok := adapter.(Scanner); !ok {
				continue
			}
			out = append(out, ScanTarget{Provider: provider, Network: n})
		}
	}
	return out, nil
}

// CanBroadcast reports whether the provider that owns an address has an
// adapter that can send from it.
func (s *Service) CanBroadcast(ctx context.Context, providerID int16) bool {
	provider, err := s.catalog.Provider(ctx, providerID)
	if err != nil {
		return false
	}
	adapter, ok := s.adapters.Get(provider.Code)
	if !ok {
		return false
	}
	_, ok = adapter.(Broadcaster)
	return ok
}
