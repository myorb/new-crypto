// Package chain is the platform's edge to the blockchains: wallets and the
// addresses derived from them, the transactions and transfers observed on
// chain, and the scanner cursors. It publishes facts (a transfer arrived at
// one of our addresses) and knows nothing about invoices or merchants beyond
// the organization stamped on a deposit address. Commerce subscribes.
package chain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/reference"
	"templ-app/internal/store"
)

var (
	ErrNoWallet       = errors.New("chain: no active wallet for this network and provider")
	ErrNoAdapter      = errors.New("chain: no adapter registered for provider")
	ErrInvalidAddress = errors.New("chain: address is not valid for this network")
	ErrUnknownAddress = errors.New("chain: address is not one of ours")
	ErrNotFound       = postgres.ErrNotFound
)

// Adapter is what a provider integration must implement. Scanning and
// broadcasting loops live in the worker and call these; the Service only
// needs address derivation to allocate deposit addresses.
type Adapter interface {
	// Code matches payment_providers.code ("tron_rpc", "fireblocks").
	Code() string
	// DeriveAddress returns the address at derivation index for a wallet. For
	// custodial providers it creates the address remotely and returns its id.
	DeriveAddress(ctx context.Context, wallet store.Wallet, index int64) (DerivedAddress, error)
}

// DerivedAddress is an adapter's answer to DeriveAddress.
type DerivedAddress struct {
	Address    string
	Memo       *string
	ExternalID *string
}

// Registry maps provider codes to adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

// Register adds or replaces an adapter.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Code()] = a
}

// Get returns the adapter for a provider code.
func (r *Registry) Get(code string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[code]
	return a, ok
}

// TransferHandler is how commerce subscribes to incoming transfers. It is
// called after the transfer row is committed; handlers must be idempotent.
type TransferHandler interface {
	HandleIncomingTransfer(ctx context.Context, transfer store.Transfer, tx store.Transaction) error
}

// Service is the chain domain.
type Service struct {
	q        *store.Queries
	pool     *pgxpool.Pool
	catalog  *reference.Catalog
	adapters *Registry
	log      *slog.Logger

	mu       sync.RWMutex
	handlers []TransferHandler
}

// New builds the service.
func New(pool *pgxpool.Pool, catalog *reference.Catalog, adapters *Registry, log *slog.Logger) *Service {
	if adapters == nil {
		adapters = NewRegistry()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{q: store.New(pool), pool: pool, catalog: catalog, adapters: adapters, log: log}
}

// WithTx binds the service to a transaction. Subscribers are shared.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	cp := &Service{q: s.q.WithTx(tx), catalog: s.catalog.WithTx(tx), adapters: s.adapters, log: s.log}
	s.mu.RLock()
	cp.handlers = s.handlers
	s.mu.RUnlock()
	return cp
}

// Subscribe registers a handler for incoming transfers.
func (s *Service) Subscribe(h TransferHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = append(s.handlers, h)
}

// Wallets ---------------------------------------------------------------------------

// WalletInput describes a new wallet.
type WalletInput struct {
	ProviderID       int16
	NetworkID        int16
	Kind             store.WalletKind
	Name             string
	KeyRef           *string // KMS / HSM reference for self custody
	Xpub             *string
	DerivationPath   *string
	ExternalWalletID *string // vault id at a custodial provider
}

// CreateWallet registers a wallet the platform controls.
func (s *Service) CreateWallet(ctx context.Context, in WalletInput) (store.Wallet, error) {
	if in.KeyRef == nil && in.ExternalWalletID == nil {
		return store.Wallet{}, errors.New("chain: wallet needs key_ref or external_wallet_id")
	}
	return s.q.CreateWallet(ctx, store.CreateWalletParams{
		ProviderID: in.ProviderID, NetworkID: in.NetworkID, Kind: in.Kind, Name: in.Name,
		KeyRef: in.KeyRef, Xpub: in.Xpub, DerivationPath: in.DerivationPath, ExternalWalletID: in.ExternalWalletID,
	})
}

// Wallets lists every wallet with network and provider codes.
func (s *Service) Wallets(ctx context.Context) ([]store.ListWalletsRow, error) {
	return s.q.ListWallets(ctx)
}

// Wallet returns one wallet.
func (s *Service) Wallet(ctx context.Context, id uuid.UUID) (store.Wallet, error) {
	w, err := s.q.GetWallet(ctx, id)
	return w, postgres.MapNotFound(err)
}

// SetWalletActive enables or retires a wallet.
func (s *Service) SetWalletActive(ctx context.Context, id uuid.UUID, active bool) error {
	return s.q.SetWalletActive(ctx, store.SetWalletActiveParams{ID: id, IsActive: active})
}

// Addresses -------------------------------------------------------------------------

// AllocateDepositAddress derives a fresh address for a merchant on a network.
// The provider is routed through the catalog (merchant preference first).
// Reserving the index and inserting the row happen in one transaction so two
// concurrent allocations never share an index.
func (s *Service) AllocateDepositAddress(ctx context.Context, orgID uuid.UUID, networkID int16, preferredProvider *int16) (store.Address, error) {
	route, err := s.catalog.RouteDeposit(ctx, networkID, preferredProvider)
	if err != nil {
		return store.Address{}, err
	}
	provider, err := s.catalog.Provider(ctx, route.ProviderID)
	if err != nil {
		return store.Address{}, err
	}
	adapter, ok := s.adapters.Get(provider.Code)
	if !ok {
		return store.Address{}, fmt.Errorf("%w: %s", ErrNoAdapter, provider.Code)
	}
	run := func(q *store.Queries) (store.Address, error) {
		wallet, err := q.GetActiveWallet(ctx, store.GetActiveWalletParams{
			NetworkID: networkID, Kind: store.WalletKindDeposit, ProviderID: pgtype.Int2{Int16: route.ProviderID, Valid: true},
		})
		if err != nil {
			if postgres.IsNotFound(err) {
				return store.Address{}, ErrNoWallet
			}
			return store.Address{}, err
		}
		index, err := q.ReserveWalletIndex(ctx, wallet.ID)
		if err != nil {
			return store.Address{}, fmt.Errorf("chain: reserve index: %w", err)
		}
		derived, err := adapter.DeriveAddress(ctx, wallet, index)
		if err != nil {
			return store.Address{}, fmt.Errorf("chain: derive address: %w", err)
		}
		addr, err := q.CreateAddress(ctx, store.CreateAddressParams{
			NetworkID: networkID, ProviderID: route.ProviderID, WalletID: wallet.ID,
			OrganizationID: uuid.NullUUID{UUID: orgID, Valid: true},
			Address:        derived.Address, Memo: derived.Memo, Kind: store.WalletKindDeposit,
			DerivationIndex: pgtype.Int8{Int64: index, Valid: true}, ExternalAddressID: derived.ExternalID,
		})
		if err != nil {
			if postgres.IsCheckViolation(err) {
				return store.Address{}, fmt.Errorf("%w: %v", ErrInvalidAddress, err)
			}
			return store.Address{}, fmt.Errorf("chain: create address: %w", err)
		}
		return addr, nil
	}
	if s.pool == nil {
		return run(s.q)
	}
	return postgres.InTxRet(ctx, s.pool, func(tx pgx.Tx) (store.Address, error) { return run(s.q.WithTx(tx)) })
}

// RegisterPlatformAddress records a hot / cold / fee address that already
// exists at the provider (no derivation).
func (s *Service) RegisterPlatformAddress(ctx context.Context, walletID uuid.UUID, kind store.WalletKind, address string, memo *string, externalID *string) (store.Address, error) {
	if kind == store.WalletKindDeposit {
		return store.Address{}, errors.New("chain: deposit addresses are allocated, not registered")
	}
	w, err := s.Wallet(ctx, walletID)
	if err != nil {
		return store.Address{}, err
	}
	addr, err := s.q.CreateAddress(ctx, store.CreateAddressParams{
		NetworkID: w.NetworkID, ProviderID: w.ProviderID, WalletID: walletID, Address: address, Memo: memo, Kind: kind, ExternalAddressID: externalID,
	})
	if err != nil && postgres.IsCheckViolation(err) {
		return store.Address{}, ErrInvalidAddress
	}
	return addr, err
}

// Address returns an address row.
func (s *Service) Address(ctx context.Context, id uuid.UUID) (store.Address, error) {
	a, err := s.q.GetAddress(ctx, id)
	return a, postgres.MapNotFound(err)
}

// Lookup finds one of our addresses on a network, or ErrUnknownAddress.
func (s *Service) Lookup(ctx context.Context, networkID int16, address string, memo *string) (store.Address, error) {
	a, err := s.q.FindAddress(ctx, store.FindAddressParams{NetworkID: networkID, Address: address, Memo: memo})
	if err != nil {
		if postgres.IsNotFound(err) {
			return store.Address{}, ErrUnknownAddress
		}
		return store.Address{}, err
	}
	return a, nil
}

// OrganizationAddresses lists a merchant's deposit addresses.
func (s *Service) OrganizationAddresses(ctx context.Context, orgID uuid.UUID, limit, offset int32) ([]store.ListOrganizationAddressesRow, error) {
	return s.q.ListOrganizationAddresses(ctx, store.ListOrganizationAddressesParams{OrganizationID: uuid.NullUUID{UUID: orgID, Valid: true}, Limit: limit, Offset: offset})
}

// PlatformAddresses lists hot, cold and fee addresses.
func (s *Service) PlatformAddresses(ctx context.Context) ([]store.ListPlatformAddressesRow, error) {
	return s.q.ListPlatformAddresses(ctx)
}

// HotAddress returns the active hot address on a network for a provider.
func (s *Service) HotAddress(ctx context.Context, networkID, providerID int16) (store.Address, error) {
	rows, err := s.q.ListPlatformAddresses(ctx)
	if err != nil {
		return store.Address{}, err
	}
	for _, r := range rows {
		if r.Address.NetworkID == networkID && r.Address.ProviderID == providerID && r.Address.Kind == store.WalletKindHot {
			return r.Address, nil
		}
	}
	return store.Address{}, ErrNotFound
}

// WatchedAddress is what a scanner needs to filter a block.
type WatchedAddress struct {
	Address string
	Memo    *string
}

// WatchedAddresses lists the active addresses on a network.
func (s *Service) WatchedAddresses(ctx context.Context, networkID int16) ([]WatchedAddress, error) {
	rows, err := s.q.ListWatchedAddresses(ctx, networkID)
	if err != nil {
		return nil, err
	}
	out := make([]WatchedAddress, len(rows))
	for i, r := range rows {
		out[i] = WatchedAddress{Address: r.Address, Memo: r.Memo}
	}
	return out, nil
}

// SetBalance records an address's on-chain balance as of a block.
func (s *Service) SetBalance(ctx context.Context, addressID uuid.UUID, assetID int16, balance decimal.Decimal, block int64) error {
	return s.q.UpsertAddressBalance(ctx, store.UpsertAddressBalanceParams{AddressID: addressID, AssetID: assetID, Balance: money.ToNumeric(balance), AsOfBlock: block})
}

// Balances lists the recorded balances of an address.
func (s *Service) Balances(ctx context.Context, addressID uuid.UUID) ([]store.ListAddressBalancesRow, error) {
	return s.q.ListAddressBalances(ctx, addressID)
}

// Cursors -----------------------------------------------------------------------------

// Cursor returns where a scanner left off; a zero cursor when it never ran.
func (s *Service) Cursor(ctx context.Context, providerID, networkID int16) (store.ChainCursor, error) {
	c, err := s.q.GetChainCursor(ctx, store.GetChainCursorParams{ProviderID: providerID, NetworkID: networkID})
	if err != nil {
		if postgres.IsNotFound(err) {
			return store.ChainCursor{ProviderID: providerID, NetworkID: networkID}, nil
		}
		return store.ChainCursor{}, err
	}
	return c, nil
}

// SaveCursor stores scanner progress.
func (s *Service) SaveCursor(ctx context.Context, providerID, networkID int16, block int64, blockHash, external *string) error {
	return s.q.UpsertChainCursor(ctx, store.UpsertChainCursorParams{
		ProviderID: providerID, NetworkID: networkID, LastScannedBlock: block, LastScannedHash: blockHash, ExternalCursor: external,
	})
}

// Observations -------------------------------------------------------------------------

// ObservedTransaction is what a scanner or provider callback saw on chain.
type ObservedTransaction struct {
	NetworkID      int16
	ProviderID     int16
	Hash           string
	BlockNumber    *int64
	BlockHash      *string
	BlockTimestamp *time.Time
	From           *string
	To             *string
	Status         store.TxStatus // pending / confirmed / failed / dropped
	Confirmations  int32
	FeeNative      *decimal.Decimal
	FeeDetails     []byte
	ExternalTxID   *string
	Raw            []byte
	Transfers      []ObservedTransfer
}

// ObservedTransfer is one value movement inside a transaction.
type ObservedTransfer struct {
	AssetID  int16
	LogIndex int32
	From     *string
	To       string
	ToMemo   *string
	Amount   decimal.Decimal
}

// Observation is the stored result of RecordTransaction.
type Observation struct {
	Transaction store.Transaction
	Transfers   []store.Transfer
	// New holds the transfers that were inserted by this call (not seen before)
	// and land on one of our addresses; subscribers are notified about these.
	New []store.Transfer
}

// RecordTransaction upserts a transaction and its transfers, resolving which
// of our addresses each transfer touches, then notifies subscribers about
// newly seen incoming transfers. Safe to call repeatedly for the same hash.
func (s *Service) RecordTransaction(ctx context.Context, obs ObservedTransaction) (Observation, error) {
	if obs.Status == "" {
		obs.Status = store.TxStatusPending
	}
	var out Observation
	var fee pgtype.Numeric
	if obs.FeeNative != nil {
		fee = money.ToNumeric(*obs.FeeNative)
	}
	var block pgtype.Int8
	if obs.BlockNumber != nil {
		block = pgtype.Int8{Int64: *obs.BlockNumber, Valid: true}
	}
	run := func(q *store.Queries) error {
		tx, err := q.UpsertTransaction(ctx, store.UpsertTransactionParams{
			NetworkID: obs.NetworkID, ProviderID: obs.ProviderID, Hash: obs.Hash, BlockNumber: block, BlockHash: obs.BlockHash,
			BlockTimestamp: obs.BlockTimestamp, FromAddress: obs.From, ToAddress: obs.To, Status: obs.Status,
			Confirmations: obs.Confirmations, FeeNative: fee, FeeDetails: obs.FeeDetails, ExternalTxID: obs.ExternalTxID, Raw: obs.Raw,
		})
		if err != nil {
			if postgres.IsCheckViolation(err) {
				return fmt.Errorf("chain: tx hash %q rejected: %w", obs.Hash, err)
			}
			return fmt.Errorf("chain: upsert transaction: %w", err)
		}
		out.Transaction = tx
		for _, t := range obs.Transfers {
			if !t.Amount.IsPositive() {
				continue
			}
			direction := store.TransferDirectionInternal
			var addrID uuid.NullUUID
			toAddr, toErr := q.FindAddress(ctx, store.FindAddressParams{NetworkID: obs.NetworkID, Address: t.To, Memo: t.ToMemo})
			var fromOurs bool
			if t.From != nil {
				if _, err := q.FindAddress(ctx, store.FindAddressParams{NetworkID: obs.NetworkID, Address: *t.From}); err == nil {
					fromOurs = true
				}
			}
			switch {
			case toErr == nil && fromOurs:
				direction = store.TransferDirectionInternal
				addrID = uuid.NullUUID{UUID: toAddr.ID, Valid: true}
			case toErr == nil:
				direction = store.TransferDirectionIn
				addrID = uuid.NullUUID{UUID: toAddr.ID, Valid: true}
			case fromOurs:
				direction = store.TransferDirectionOut
			default:
				continue // not ours at all; scanners should not hand us these, but be safe
			}
			row, err := q.CreateTransfer(ctx, store.CreateTransferParams{
				TransactionID: tx.ID, NetworkID: obs.NetworkID, AssetID: t.AssetID, LogIndex: t.LogIndex, FromAddress: t.From,
				ToAddress: t.To, ToMemo: t.ToMemo, Amount: money.ToNumeric(t.Amount), Direction: direction, AddressID: addrID,
			})
			if err != nil {
				if postgres.IsNotFound(err) {
					// already recorded on an earlier pass
					existing, err := q.GetTransferByTxLog(ctx, store.GetTransferByTxLogParams{TransactionID: tx.ID, LogIndex: t.LogIndex})
					if err != nil {
						return err
					}
					out.Transfers = append(out.Transfers, existing)
					continue
				}
				return fmt.Errorf("chain: create transfer: %w", err)
			}
			out.Transfers = append(out.Transfers, row)
			if direction == store.TransferDirectionIn {
				out.New = append(out.New, row)
				_ = q.TouchAddress(ctx, toAddr.ID)
			}
		}
		return nil
	}
	var err error
	if s.pool == nil {
		err = run(s.q)
	} else {
		err = postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error { return run(s.q.WithTx(tx)) })
	}
	if err != nil {
		return Observation{}, err
	}
	s.notify(ctx, out)
	return out, nil
}

// notify hands new incoming transfers to subscribers. Failures are logged;
// handlers are idempotent and the reconciliation workers pick up stragglers.
func (s *Service) notify(ctx context.Context, obs Observation) {
	s.mu.RLock()
	handlers := s.handlers
	s.mu.RUnlock()
	for _, t := range obs.New {
		for _, h := range handlers {
			if err := h.HandleIncomingTransfer(ctx, t, obs.Transaction); err != nil {
				s.log.ErrorContext(ctx, "chain: transfer handler failed", "transfer", t.ID, "tx", obs.Transaction.Hash, "err", err)
			}
		}
	}
}

// UpdateConfirmations refreshes a pending transaction's depth and status.
func (s *Service) UpdateConfirmations(ctx context.Context, txID uuid.UUID, confirmations int32, block *int64, status store.TxStatus) (store.Transaction, error) {
	var b pgtype.Int8
	if block != nil {
		b = pgtype.Int8{Int64: *block, Valid: true}
	}
	return s.q.UpdateTransactionConfirmations(ctx, store.UpdateTransactionConfirmationsParams{ID: txID, Confirmations: confirmations, BlockNumber: b, Status: status})
}

// Transaction returns a transaction by id.
func (s *Service) Transaction(ctx context.Context, id uuid.UUID) (store.Transaction, error) {
	t, err := s.q.GetTransaction(ctx, id)
	return t, postgres.MapNotFound(err)
}

// TransactionByHash returns a transaction by network and hash.
func (s *Service) TransactionByHash(ctx context.Context, networkID int16, hash string) (store.Transaction, error) {
	t, err := s.q.GetTransactionByHash(ctx, store.GetTransactionByHashParams{NetworkID: networkID, Hash: hash})
	return t, postgres.MapNotFound(err)
}

// PendingTransactions lists what the confirmation tracker must re-check.
func (s *Service) PendingTransactions(ctx context.Context, networkID int16, limit int32) ([]store.Transaction, error) {
	return s.q.ListPendingTransactions(ctx, store.ListPendingTransactionsParams{NetworkID: networkID, Limit: limit})
}

// Transfer returns a transfer by id.
func (s *Service) Transfer(ctx context.Context, id uuid.UUID) (store.Transfer, error) {
	t, err := s.q.GetTransfer(ctx, id)
	return t, postgres.MapNotFound(err)
}

// TransfersForAddress lists an address's history with the enclosing transactions.
func (s *Service) TransfersForAddress(ctx context.Context, addressID uuid.UUID, limit, offset int32) ([]store.ListTransfersForAddressRow, error) {
	return s.q.ListTransfersForAddress(ctx, store.ListTransfersForAddressParams{AddressID: uuid.NullUUID{UUID: addressID, Valid: true}, Limit: limit, Offset: offset})
}

// Confirmed reports whether a transaction has reached its network's threshold.
func (s *Service) Confirmed(ctx context.Context, tx store.Transaction) (bool, error) {
	n, err := s.catalog.Network(ctx, tx.NetworkID)
	if err != nil {
		return false, err
	}
	return tx.Status == store.TxStatusConfirmed || tx.Confirmations >= n.RequiredConfirmations, nil
}
