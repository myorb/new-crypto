// Package treasury moves funds between platform-controlled addresses:
// sweeping deposit addresses into the hot wallet, topping up fee addresses
// and rebalancing between hot and cold. These moves never change what a
// merchant owns; only the native gas they burn and hot/cold custody hit the
// ledger.
package treasury

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/ledger"
	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/reference"
	"templ-app/internal/store"
)

var (
	ErrNotFound    = postgres.ErrNotFound
	ErrWrongState  = errors.New("treasury: transfer is not in a state that allows this action")
	ErrNoHotWallet = errors.New("treasury: no hot address on this network for the provider")
	ErrBadAmount   = errors.New("treasury: amount must be positive")
)

// Service is the treasury domain.
type Service struct {
	q       *store.Queries
	pool    *pgxpool.Pool
	catalog *reference.Catalog
	chain   *chain.Service
	ledger  *ledger.Service
	log     *slog.Logger
}

// New builds the service.
func New(pool *pgxpool.Pool, catalog *reference.Catalog, ch *chain.Service, led *ledger.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{q: store.New(pool), pool: pool, catalog: catalog, chain: ch, ledger: led, log: log}
}

// WithTx binds the service and its collaborators to one transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{q: s.q.WithTx(tx), catalog: s.catalog.WithTx(tx), chain: s.chain.WithTx(tx), ledger: s.ledger.WithTx(tx), log: s.log}
}

func (s *Service) inTx(ctx context.Context, fn func(s *Service) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(s.WithTx(tx)) })
}

// Planning ----------------------------------------------------------------------------

// QueueSweep queues moving `amount` of an asset from a deposit address to
// the hot address of the same provider and network.
func (s *Service) QueueSweep(ctx context.Context, assetID int16, fromAddressID uuid.UUID, amount decimal.Decimal) (store.InternalTransfer, error) {
	if !amount.IsPositive() {
		return store.InternalTransfer{}, ErrBadAmount
	}
	from, err := s.chain.Address(ctx, fromAddressID)
	if err != nil {
		return store.InternalTransfer{}, err
	}
	hot, err := s.chain.HotAddress(ctx, from.NetworkID, from.ProviderID)
	if err != nil {
		return store.InternalTransfer{}, ErrNoHotWallet
	}
	return s.q.CreateInternalTransfer(ctx, store.CreateInternalTransferParams{
		Kind: store.InternalTransferKindSweep, AssetID: assetID, ProviderID: from.ProviderID,
		FromAddressID: from.ID, ToAddressID: hot.ID, Amount: money.ToNumeric(amount),
	})
}

// PlanSweeps queues a sweep for every deposit address holding at least
// `threshold` of the asset that has no sweep in flight.
func (s *Service) PlanSweeps(ctx context.Context, assetID int16, threshold decimal.Decimal, limit int32) ([]store.InternalTransfer, error) {
	rows, err := s.q.ListSweepCandidates(ctx, store.ListSweepCandidatesParams{AssetID: assetID, Balance: money.ToNumeric(threshold), Limit: limit})
	if err != nil {
		return nil, err
	}
	var out []store.InternalTransfer
	for _, r := range rows {
		t, err := s.QueueSweep(ctx, assetID, r.Address.ID, money.FromNumeric(r.AddressBalance.Balance))
		if err != nil {
			s.log.WarnContext(ctx, "treasury: skip sweep", "address", r.Address.Address, "err", err)
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// QueueTransfer queues a fee top-up or a hot/cold rebalance between two
// platform addresses.
func (s *Service) QueueTransfer(ctx context.Context, kind store.InternalTransferKind, assetID int16, fromAddressID, toAddressID uuid.UUID, amount decimal.Decimal) (store.InternalTransfer, error) {
	if !amount.IsPositive() {
		return store.InternalTransfer{}, ErrBadAmount
	}
	if kind == store.InternalTransferKindSweep {
		return store.InternalTransfer{}, errors.New("treasury: use QueueSweep for sweeps")
	}
	from, err := s.chain.Address(ctx, fromAddressID)
	if err != nil {
		return store.InternalTransfer{}, err
	}
	to, err := s.chain.Address(ctx, toAddressID)
	if err != nil {
		return store.InternalTransfer{}, err
	}
	if from.NetworkID != to.NetworkID {
		return store.InternalTransfer{}, errors.New("treasury: addresses are on different networks")
	}
	return s.q.CreateInternalTransfer(ctx, store.CreateInternalTransferParams{
		Kind: kind, AssetID: assetID, ProviderID: from.ProviderID, FromAddressID: from.ID, ToAddressID: to.ID, Amount: money.ToNumeric(amount),
	})
}

// Execution (driven by the worker) ---------------------------------------------------------

// Queued lists transfers the broadcaster must send.
func (s *Service) Queued(ctx context.Context, limit int32) ([]store.InternalTransfer, error) {
	return s.q.ListQueuedInternalTransfers(ctx, limit)
}

// MarkBroadcast records the transaction the provider sent.
func (s *Service) MarkBroadcast(ctx context.Context, id, txID uuid.UUID, externalRef *string) (store.InternalTransfer, error) {
	t, err := s.q.MarkInternalTransferBroadcast(ctx, store.MarkInternalTransferBroadcastParams{ID: id, TransactionID: uuid.NullUUID{UUID: txID, Valid: true}, ExternalRef: externalRef})
	if err != nil && postgres.IsNotFound(err) {
		return store.InternalTransfer{}, ErrWrongState
	}
	return t, err
}

// Cost is what a transfer consumed on chain.
type Cost struct {
	EnergyUsed    *int64
	BandwidthUsed *int64
	CostNative    *decimal.Decimal
}

// Complete settles a broadcast transfer: gas is booked as a platform
// expense and hot/cold rebalances move custody in the ledger.
func (s *Service) Complete(ctx context.Context, id uuid.UUID, cost Cost) (store.InternalTransfer, error) {
	var out store.InternalTransfer
	err := s.inTx(ctx, func(s *Service) error {
		t, err := s.q.CompleteInternalTransfer(ctx, store.CompleteInternalTransferParams{
			ID: id, EnergyUsed: int8(cost.EnergyUsed), BandwidthUsed: int8(cost.BandwidthUsed), CostNative: money.NullNumeric(cost.CostNative),
		})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrWrongState
			}
			return err
		}
		out = t
		asset, err := s.catalog.Asset(ctx, t.AssetID)
		if err != nil {
			return err
		}
		if cost.CostNative != nil && cost.CostNative.IsPositive() {
			native, err := s.catalog.NativeAsset(ctx, asset.NetworkID)
			if err != nil {
				return err
			}
			if _, err := s.ledger.Post(ctx, ledger.NetworkFeePaid(ledger.RefInternalTransfer, t.ID, native.ID, *cost.CostNative)); err != nil && !errors.Is(err, ledger.ErrAlreadyPosted) {
				return err
			}
		}
		if t.Kind == store.InternalTransferKindRebalance {
			from, err := s.chain.Address(ctx, t.FromAddressID)
			if err != nil {
				return err
			}
			to, err := s.chain.Address(ctx, t.ToAddressID)
			if err != nil {
				return err
			}
			switch {
			case from.Kind == store.WalletKindHot && to.Kind == store.WalletKindCold:
				_, err = s.ledger.Post(ctx, ledger.Rebalance(t.ID, t.AssetID, money.FromNumeric(t.Amount), true))
			case from.Kind == store.WalletKindCold && to.Kind == store.WalletKindHot:
				_, err = s.ledger.Post(ctx, ledger.Rebalance(t.ID, t.AssetID, money.FromNumeric(t.Amount), false))
			}
			if err != nil && !errors.Is(err, ledger.ErrAlreadyPosted) {
				return err
			}
		}
		return nil
	})
	return out, err
}

// Fail records a transfer that did not go through.
func (s *Service) Fail(ctx context.Context, id uuid.UUID, reason string) (store.InternalTransfer, error) {
	t, err := s.q.FailInternalTransfer(ctx, store.FailInternalTransferParams{ID: id, FailureReason: &reason})
	if err != nil && postgres.IsNotFound(err) {
		return store.InternalTransfer{}, ErrWrongState
	}
	return t, err
}

// Stats summarises one reconciliation pass.
type Stats struct {
	Checked, Completed, Failed int
}

// ReconcileBroadcast settles or fails broadcast transfers from their
// transaction's state.
func (s *Service) ReconcileBroadcast(ctx context.Context, batch int32) (Stats, error) {
	rows, err := s.q.ListBroadcastInternalTransfers(ctx, batch)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Checked: len(rows)}
	for _, r := range rows {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		switch {
		case r.TxStatus == store.TxStatusFailed || r.TxStatus == store.TxStatusDropped:
			if _, err := s.Fail(ctx, r.InternalTransfer.ID, "transaction "+string(r.TxStatus)); err != nil {
				s.log.ErrorContext(ctx, "treasury: fail failed", "transfer", r.InternalTransfer.ID, "err", err)
				continue
			}
			st.Failed++
		case r.TxStatus == store.TxStatusConfirmed || r.Confirmations >= r.RequiredConfirmations:
			var c Cost
			if r.FeeNative.Valid {
				f := money.FromNumeric(r.FeeNative)
				c.CostNative = &f
			}
			if _, err := s.Complete(ctx, r.InternalTransfer.ID, c); err != nil {
				s.log.ErrorContext(ctx, "treasury: complete failed", "transfer", r.InternalTransfer.ID, "err", err)
				continue
			}
			st.Completed++
		}
	}
	return st, nil
}

// Reads -----------------------------------------------------------------------------------

// Get returns one transfer.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (store.InternalTransfer, error) {
	t, err := s.q.GetInternalTransfer(ctx, id)
	return t, postgres.MapNotFound(err)
}

// List pages transfer history with asset and address labels.
func (s *Service) List(ctx context.Context, limit, offset int32) ([]store.ListInternalTransfersRow, error) {
	return s.q.ListInternalTransfers(ctx, store.ListInternalTransfersParams{Limit: limit, Offset: offset})
}

// Describe renders a transfer for logs.
func Describe(t store.InternalTransfer) string {
	return fmt.Sprintf("%s %s asset=%d %s", t.Kind, money.FromNumeric(t.Amount), t.AssetID, t.Status)
}

func int8(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}
