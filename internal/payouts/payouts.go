// Package payouts owns withdrawals: a merchant asking for funds to leave the
// platform. It prices the fee, locks the funds in the ledger, runs the
// approval flow (four eyes), hands the signed request to the provider via
// the worker, and settles or releases the funds once the chain answers.
package payouts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/events"
	"templ-app/internal/ledger"
	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/pricing"
	"templ-app/internal/reference"
	"templ-app/internal/store"
)

// ResourceType is what events and ledger journals call a withdrawal.
const ResourceType = "withdrawal"

var (
	ErrNotFound          = postgres.ErrNotFound
	ErrBadAmount         = errors.New("payouts: amount must be positive")
	ErrBelowMinimum      = errors.New("payouts: amount is below the asset's minimum withdrawal")
	ErrInsufficientFunds = errors.New("payouts: insufficient available balance")
	ErrInvalidAddress    = errors.New("payouts: destination address is not valid for this network")
	ErrNotWhitelisted    = errors.New("payouts: destination address is not whitelisted")
	ErrSelfApproval      = errors.New("payouts: a withdrawal cannot be approved by the user who requested it")
	ErrWrongState        = errors.New("payouts: withdrawal is not in a state that allows this action")
	ErrExternalIDTaken   = errors.New("payouts: external_id already used by another withdrawal")
	ErrNoRoute           = errors.New("payouts: no provider or hot wallet can send on this network")
)

// Options tune the policy.
type Options struct {
	RequireWhitelist bool // refuse destinations not saved as whitelisted
}

// Service is the payouts domain.
type Service struct {
	q       *store.Queries
	pool    *pgxpool.Pool
	catalog *reference.Catalog
	pricing *pricing.Service
	ledger  *ledger.Service
	chain   *chain.Service
	events  *events.Service
	opts    Options
	log     *slog.Logger
}

// New builds the service.
func New(pool *pgxpool.Pool, catalog *reference.Catalog, pr *pricing.Service, led *ledger.Service, ch *chain.Service, ev *events.Service, opts Options, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{q: store.New(pool), pool: pool, catalog: catalog, pricing: pr, ledger: led, chain: ch, events: ev, opts: opts, log: log}
}

// WithTx binds the service and its collaborators to one transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{
		q: s.q.WithTx(tx), catalog: s.catalog.WithTx(tx), pricing: s.pricing.WithTx(tx), ledger: s.ledger.WithTx(tx),
		chain: s.chain.WithTx(tx), events: s.events.WithTx(tx), opts: s.opts, log: s.log,
	}
}

func (s *Service) inTx(ctx context.Context, fn func(s *Service) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(s.WithTx(tx)) })
}

// Request ----------------------------------------------------------------------------

// RequestInput is a new withdrawal.
type RequestInput struct {
	OrganizationID    uuid.UUID
	AssetID           int16
	ToAddress         string
	ToMemo            *string
	Amount            decimal.Decimal
	ExternalID        *string
	RequestedBy       uuid.NullUUID // dashboard user
	RequestedByAPIKey uuid.NullUUID // or API key
	Metadata          map[string]any
	// AutoApprove skips the approval step (for API keys the merchant marked as
	// trusted, or amounts under a policy threshold decided by the caller).
	AutoApprove bool
}

// Request validates, prices, locks funds and records the withdrawal as
// pending approval (or approved when AutoApprove is set).
func (s *Service) Request(ctx context.Context, in RequestInput) (store.Withdrawal, error) {
	if !in.Amount.IsPositive() {
		return store.Withdrawal{}, ErrBadAmount
	}
	asset, err := s.catalog.EnabledAsset(ctx, in.AssetID)
	if err != nil {
		return store.Withdrawal{}, err
	}
	if in.Amount.LessThan(money.FromNumeric(asset.MinWithdrawal)) {
		return store.Withdrawal{}, ErrBelowMinimum
	}
	meta := []byte("{}")
	if in.Metadata != nil {
		if meta, err = json.Marshal(in.Metadata); err != nil {
			return store.Withdrawal{}, err
		}
	}
	var out store.Withdrawal
	err = s.inTx(ctx, func(s *Service) error {
		if s.opts.RequireWhitelist {
			ok, err := s.q.IsWithdrawalAddressWhitelisted(ctx, store.IsWithdrawalAddressWhitelistedParams{
				OrganizationID: in.OrganizationID, NetworkID: asset.NetworkID, Address: in.ToAddress, Memo: in.ToMemo,
			})
			if err != nil {
				return err
			}
			if !ok {
				return ErrNotWhitelisted
			}
		}
		fees, err := s.pricing.WithdrawalFees(ctx, in.OrganizationID, in.AssetID, in.Amount)
		if err != nil {
			return err
		}
		total := in.Amount.Add(fees.Fee)
		available, err := s.ledger.Available(ctx, in.OrganizationID, in.AssetID)
		if err != nil {
			return err
		}
		if available.LessThan(total) {
			return fmt.Errorf("%w: available %s, need %s", ErrInsufficientFunds, available, total)
		}
		status := store.WithdrawalStatusPendingApproval
		if in.AutoApprove {
			status = store.WithdrawalStatusApproved
		}
		w, err := s.q.CreateWithdrawal(ctx, store.CreateWithdrawalParams{
			OrganizationID: in.OrganizationID, ExternalID: in.ExternalID, AssetID: in.AssetID, ToAddress: in.ToAddress, ToMemo: in.ToMemo,
			Amount: money.ToNumeric(in.Amount), FeeAmount: money.ToNumeric(fees.Fee), FeeBpsApplied: pgtype.Int4{Int32: fees.FeeBps, Valid: true},
			FeeScheduleID: fees.ScheduleID, RequestedBy: in.RequestedBy, RequestedByApiKey: in.RequestedByAPIKey, Metadata: meta, Status: status,
		})
		if err != nil {
			switch {
			case postgres.IsUniqueViolation(err):
				return ErrExternalIDTaken
			case postgres.IsCheckViolation(err):
				return ErrInvalidAddress
			}
			return fmt.Errorf("payouts: create withdrawal: %w", err)
		}
		if _, err := s.ledger.Post(ctx, ledger.WithdrawalRequested(w.ID, w.OrganizationID, w.AssetID, total)); err != nil {
			return err
		}
		if _, err := s.events.Emit(ctx, w.OrganizationID, events.WithdrawalRequested, ResourceType, w.ID, s.view(ctx, w)); err != nil {
			return err
		}
		if in.AutoApprove {
			if _, err := s.events.Emit(ctx, w.OrganizationID, events.WithdrawalApproved, ResourceType, w.ID, s.view(ctx, w)); err != nil {
				return err
			}
		}
		out = w
		return nil
	})
	return out, err
}

// Approval -----------------------------------------------------------------------------

// Approve is the second pair of eyes. The approver must differ from the requester.
func (s *Service) Approve(ctx context.Context, orgID, id, approverID uuid.UUID) (store.Withdrawal, error) {
	var out store.Withdrawal
	err := s.inTx(ctx, func(s *Service) error {
		w, err := s.q.GetWithdrawalForOrganization(ctx, store.GetWithdrawalForOrganizationParams{ID: id, OrganizationID: orgID})
		if err != nil {
			return postgres.MapNotFound(err)
		}
		if w.RequestedBy.Valid && w.RequestedBy.UUID == approverID {
			return ErrSelfApproval
		}
		w, err = s.q.ApproveWithdrawal(ctx, store.ApproveWithdrawalParams{ID: id, ApprovedBy: uuid.NullUUID{UUID: approverID, Valid: true}})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrWrongState
			}
			return err
		}
		out = w
		_, err = s.events.Emit(ctx, w.OrganizationID, events.WithdrawalApproved, ResourceType, w.ID, s.view(ctx, w))
		return err
	})
	return out, err
}

// Reject declines a pending withdrawal and releases the locked funds.
func (s *Service) Reject(ctx context.Context, orgID, id, approverID uuid.UUID, reason string) (store.Withdrawal, error) {
	var out store.Withdrawal
	err := s.inTx(ctx, func(s *Service) error {
		if _, err := s.q.GetWithdrawalForOrganization(ctx, store.GetWithdrawalForOrganizationParams{ID: id, OrganizationID: orgID}); err != nil {
			return postgres.MapNotFound(err)
		}
		w, err := s.q.RejectWithdrawal(ctx, store.RejectWithdrawalParams{ID: id, ApprovedBy: uuid.NullUUID{UUID: approverID, Valid: true}, FailureReason: &reason})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrWrongState
			}
			return err
		}
		out = w
		return s.release(ctx, w, "rejected", events.WithdrawalRejected)
	})
	return out, err
}

// Cancel lets the merchant withdraw a request that has not been sent yet.
func (s *Service) Cancel(ctx context.Context, orgID, id uuid.UUID) (store.Withdrawal, error) {
	var out store.Withdrawal
	err := s.inTx(ctx, func(s *Service) error {
		w, err := s.q.CancelWithdrawal(ctx, store.CancelWithdrawalParams{ID: id, OrganizationID: orgID})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrWrongState
			}
			return err
		}
		out = w
		return s.release(ctx, w, "cancelled", events.WithdrawalCancelled)
	})
	return out, err
}

// release reverses the lock posting and announces the terminal state.
func (s *Service) release(ctx context.Context, w store.Withdrawal, reason, eventType string) error {
	total := money.FromNumeric(w.Amount).Add(money.FromNumeric(w.FeeAmount))
	if _, err := s.ledger.Post(ctx, ledger.WithdrawalReleased(w.ID, w.OrganizationID, w.AssetID, total, reason)); err != nil && !errors.Is(err, ledger.ErrAlreadyPosted) {
		return err
	}
	_, err := s.events.Emit(ctx, w.OrganizationID, eventType, ResourceType, w.ID, s.view(ctx, w))
	return err
}

// Execution (driven by the worker) ---------------------------------------------------------

// ToProcess lists approved and queued withdrawals the broadcaster must send.
func (s *Service) ToProcess(ctx context.Context, limit int32) ([]store.Withdrawal, error) {
	return s.q.ListWithdrawalsToProcess(ctx, limit)
}

// Route picks provider and hot address for a withdrawal and marks it queued.
func (s *Service) Route(ctx context.Context, id uuid.UUID) (store.Withdrawal, store.Address, error) {
	w, err := s.q.GetWithdrawal(ctx, id)
	if err != nil {
		return store.Withdrawal{}, store.Address{}, postgres.MapNotFound(err)
	}
	asset, err := s.catalog.Asset(ctx, w.AssetID)
	if err != nil {
		return store.Withdrawal{}, store.Address{}, err
	}
	route, err := s.catalog.RouteWithdrawal(ctx, asset.NetworkID, nil)
	if err != nil {
		return store.Withdrawal{}, store.Address{}, ErrNoRoute
	}
	hot, err := s.chain.HotAddress(ctx, asset.NetworkID, route.ProviderID)
	if err != nil {
		return store.Withdrawal{}, store.Address{}, ErrNoRoute
	}
	if w.Status == store.WithdrawalStatusApproved {
		w, err = s.q.QueueWithdrawal(ctx, store.QueueWithdrawalParams{
			ID: id, ProviderID: pgtype.Int2{Int16: route.ProviderID, Valid: true}, FromAddressID: uuid.NullUUID{UUID: hot.ID, Valid: true},
		})
		if err != nil {
			return store.Withdrawal{}, store.Address{}, postgres.MapNotFound(err)
		}
	}
	return w, hot, nil
}

// MarkBroadcast records the transaction the provider sent.
func (s *Service) MarkBroadcast(ctx context.Context, id, txID uuid.UUID, externalRef *string) (store.Withdrawal, error) {
	var out store.Withdrawal
	err := s.inTx(ctx, func(s *Service) error {
		w, err := s.q.MarkWithdrawalBroadcast(ctx, store.MarkWithdrawalBroadcastParams{ID: id, TransactionID: uuid.NullUUID{UUID: txID, Valid: true}, ExternalRef: externalRef})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrWrongState
			}
			return err
		}
		out = w
		_, err = s.events.Emit(ctx, w.OrganizationID, events.WithdrawalBroadcast, ResourceType, w.ID, s.view(ctx, w))
		return err
	})
	return out, err
}

// Complete settles a broadcast withdrawal once its transaction is confirmed:
// locked funds leave, the platform books its fee and the gas it paid.
func (s *Service) Complete(ctx context.Context, id uuid.UUID, networkFeeNative *decimal.Decimal) (store.Withdrawal, error) {
	var out store.Withdrawal
	err := s.inTx(ctx, func(s *Service) error {
		w, err := s.q.CompleteWithdrawal(ctx, store.CompleteWithdrawalParams{ID: id, NetworkFeeNative: money.NullNumeric(networkFeeNative)})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrWrongState
			}
			return err
		}
		out = w
		amount, fee := money.FromNumeric(w.Amount), money.FromNumeric(w.FeeAmount)
		if _, err := s.ledger.Post(ctx, ledger.WithdrawalCompleted(w.ID, w.OrganizationID, w.AssetID, amount, fee)); err != nil && !errors.Is(err, ledger.ErrAlreadyPosted) {
			return err
		}
		if networkFeeNative != nil && networkFeeNative.IsPositive() {
			asset, err := s.catalog.Asset(ctx, w.AssetID)
			if err != nil {
				return err
			}
			native, err := s.catalog.NativeAsset(ctx, asset.NetworkID)
			if err != nil {
				return err
			}
			if _, err := s.ledger.Post(ctx, ledger.NetworkFeePaid(ledger.RefWithdrawal, w.ID, native.ID, *networkFeeNative)); err != nil && !errors.Is(err, ledger.ErrAlreadyPosted) {
				return err
			}
		}
		_, err = s.events.Emit(ctx, w.OrganizationID, events.WithdrawalCompleted, ResourceType, w.ID, s.view(ctx, w))
		return err
	})
	return out, err
}

// Fail records a broadcast or queued withdrawal that did not go through and
// returns the funds to the merchant.
func (s *Service) Fail(ctx context.Context, id uuid.UUID, reason string) (store.Withdrawal, error) {
	var out store.Withdrawal
	err := s.inTx(ctx, func(s *Service) error {
		w, err := s.q.FailWithdrawal(ctx, store.FailWithdrawalParams{ID: id, FailureReason: &reason})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrWrongState
			}
			return err
		}
		out = w
		return s.release(ctx, w, "failed", events.WithdrawalFailed)
	})
	return out, err
}

// Stats summarises one reconciliation pass.
type Stats struct {
	Checked, Completed, Failed int
}

// ReconcileBroadcast settles or fails broadcast withdrawals based on their
// transaction's state.
func (s *Service) ReconcileBroadcast(ctx context.Context, batch int32) (Stats, error) {
	rows, err := s.q.ListBroadcastWithdrawals(ctx, batch)
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
			if _, err := s.Fail(ctx, r.Withdrawal.ID, "transaction "+string(r.TxStatus)); err != nil {
				s.log.ErrorContext(ctx, "payouts: fail failed", "withdrawal", r.Withdrawal.ID, "err", err)
				continue
			}
			st.Failed++
		case r.TxStatus == store.TxStatusConfirmed || r.Confirmations >= r.RequiredConfirmations:
			var fee *decimal.Decimal
			if r.FeeNative.Valid {
				f := money.FromNumeric(r.FeeNative)
				fee = &f
			}
			if _, err := s.Complete(ctx, r.Withdrawal.ID, fee); err != nil {
				s.log.ErrorContext(ctx, "payouts: complete failed", "withdrawal", r.Withdrawal.ID, "err", err)
				continue
			}
			st.Completed++
		}
	}
	return st, nil
}

// Reads -----------------------------------------------------------------------------------

// Row is a withdrawal with the columns the dashboard lists.
type Row = store.ListWithdrawalsRow

// List pages a merchant's withdrawals, optionally by status.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, status *store.WithdrawalStatus, limit, offset int32) ([]Row, error) {
	var st store.NullWithdrawalStatus
	if status != nil {
		st = store.NullWithdrawalStatus{WithdrawalStatus: *status, Valid: true}
	}
	return s.q.ListWithdrawals(ctx, store.ListWithdrawalsParams{OrganizationID: orgID, Status: st, Limit: limit, Offset: offset})
}

// Get returns one withdrawal scoped to its merchant.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (store.Withdrawal, error) {
	w, err := s.q.GetWithdrawalForOrganization(ctx, store.GetWithdrawalForOrganizationParams{ID: id, OrganizationID: orgID})
	return w, postgres.MapNotFound(err)
}

// Counts returns withdrawals per status.
func (s *Service) Counts(ctx context.Context, orgID uuid.UUID) (map[store.WithdrawalStatus]int64, error) {
	rows, err := s.q.CountWithdrawalsByStatus(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[store.WithdrawalStatus]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Count
	}
	return out, nil
}

// Saved addresses ------------------------------------------------------------------------------

// AddAddress saves a destination for reuse.
func (s *Service) AddAddress(ctx context.Context, orgID uuid.UUID, networkID int16, address string, memo *string, label string, whitelisted bool, createdBy uuid.NullUUID) (store.WithdrawalAddress, error) {
	wa, err := s.q.CreateWithdrawalAddress(ctx, store.CreateWithdrawalAddressParams{
		OrganizationID: orgID, NetworkID: networkID, Address: address, Memo: memo, Label: label, IsWhitelisted: whitelisted, CreatedBy: createdBy,
	})
	if err != nil {
		switch {
		case postgres.IsCheckViolation(err):
			return store.WithdrawalAddress{}, ErrInvalidAddress
		case postgres.IsUniqueViolation(err):
			return store.WithdrawalAddress{}, errors.New("payouts: this address is already saved")
		}
	}
	return wa, err
}

// Addresses lists a merchant's saved destinations.
func (s *Service) Addresses(ctx context.Context, orgID uuid.UUID) ([]store.ListWithdrawalAddressesRow, error) {
	return s.q.ListWithdrawalAddresses(ctx, orgID)
}

// SetWhitelisted flips the whitelist flag on a saved address.
func (s *Service) SetWhitelisted(ctx context.Context, orgID, id uuid.UUID, whitelisted bool) error {
	n, err := s.q.SetWithdrawalAddressWhitelisted(ctx, store.SetWithdrawalAddressWhitelistedParams{ID: id, OrganizationID: orgID, IsWhitelisted: whitelisted})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveAddress deletes a saved destination.
func (s *Service) RemoveAddress(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteWithdrawalAddress(ctx, store.DeleteWithdrawalAddressParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// View -------------------------------------------------------------------------------------------

// WithdrawalView is the API / webhook shape of a withdrawal.
type WithdrawalView struct {
	ID          uuid.UUID `json:"id"`
	ExternalID  *string   `json:"external_id,omitempty"`
	Status      string    `json:"status"`
	Asset       string    `json:"asset"`
	Network     string    `json:"network"`
	ToAddress   string    `json:"to_address"`
	ToMemo      *string   `json:"to_memo,omitempty"`
	Amount      string    `json:"amount"`
	Fee         string    `json:"fee"`
	Total       string    `json:"total"`
	TxID        *string   `json:"transaction_id,omitempty"`
	Reason      *string   `json:"failure_reason,omitempty"`
	CreatedAt   string    `json:"created_at"`
	CompletedAt *string   `json:"completed_at,omitempty"`
}

func (s *Service) view(ctx context.Context, w store.Withdrawal) WithdrawalView {
	v := WithdrawalView{
		ID: w.ID, ExternalID: w.ExternalID, Status: string(w.Status), ToAddress: w.ToAddress, ToMemo: w.ToMemo,
		Reason: w.FailureReason, CreatedAt: w.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if w.TransactionID.Valid {
		id := w.TransactionID.UUID.String()
		v.TxID = &id
	}
	if w.CompletedAt != nil {
		t := w.CompletedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		v.CompletedAt = &t
	}
	decimals := int16(money.Scale)
	if a, err := s.catalog.Asset(ctx, w.AssetID); err == nil {
		v.Asset, v.Network, decimals = a.Code, a.Network.Code, a.Decimals
	}
	amount, fee := money.FromNumeric(w.Amount), money.FromNumeric(w.FeeAmount)
	v.Amount, v.Fee, v.Total = money.Format(amount, decimals), money.Format(fee, decimals), money.Format(amount.Add(fee), decimals)
	return v
}
