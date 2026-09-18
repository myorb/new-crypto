// Package payments turns an on-chain transfer into money a merchant owns.
// It subscribes to chain (HandleIncomingTransfer), matches the transfer to an
// invoice option through checkout, prices fees, and once the network has
// confirmed the transaction posts the ledger journal that credits the
// merchant. Nothing else writes payments.
package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/checkout"
	"templ-app/internal/events"
	"templ-app/internal/ledger"
	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/pricing"
	"templ-app/internal/reference"
	"templ-app/internal/store"
)

// ResourceType is what events and ledger journals call a payment.
const ResourceType = "payment"

var ErrNotFound = postgres.ErrNotFound

// Service is the payments domain.
type Service struct {
	q        *store.Queries
	pool     *pgxpool.Pool
	catalog  *reference.Catalog
	pricing  *pricing.Service
	ledger   *ledger.Service
	checkout *checkout.Service
	chain    *chain.Service
	events   *events.Service
	log      *slog.Logger
}

// New builds the service and subscribes it to incoming transfers.
func New(pool *pgxpool.Pool, catalog *reference.Catalog, pr *pricing.Service, led *ledger.Service, co *checkout.Service, ch *chain.Service, ev *events.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{q: store.New(pool), pool: pool, catalog: catalog, pricing: pr, ledger: led, checkout: co, chain: ch, events: ev, log: log}
	ch.Subscribe(s)
	return s
}

// WithTx binds the service and its collaborators to one transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{
		q: s.q.WithTx(tx), catalog: s.catalog.WithTx(tx), pricing: s.pricing.WithTx(tx), ledger: s.ledger.WithTx(tx),
		checkout: s.checkout.WithTx(tx), chain: s.chain.WithTx(tx), events: s.events.WithTx(tx), log: s.log,
	}
}

func (s *Service) inTx(ctx context.Context, fn func(s *Service) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(s.WithTx(tx)) })
}

// Detection -------------------------------------------------------------------------

// HandleIncomingTransfer implements chain.TransferHandler. It is idempotent:
// a transfer that already produced a payment is ignored. Transfers to
// platform (non-deposit) addresses are not payments and are skipped.
func (s *Service) HandleIncomingTransfer(ctx context.Context, t store.Transfer, tx store.Transaction) error {
	if !t.AddressID.Valid || t.Direction != store.TransferDirectionIn {
		return nil
	}
	return s.inTx(ctx, func(s *Service) error {
		if _, err := s.q.GetPaymentByTransfer(ctx, t.ID); err == nil {
			return nil
		} else if !postgres.IsNotFound(err) {
			return err
		}
		addr, err := s.chain.Address(ctx, t.AddressID.UUID)
		if err != nil {
			return err
		}
		if addr.Kind != store.WalletKindDeposit || !addr.OrganizationID.Valid {
			return nil
		}
		orgID := addr.OrganizationID.UUID
		amount := money.FromNumeric(t.Amount)

		var option *store.InvoicePaymentOption
		var invoiceID, optionID uuid.NullUUID
		if m, err := s.checkout.FindMatch(ctx, addr.ID, t.AssetID); err == nil {
			option = &m.Option
			invoiceID = uuid.NullUUID{UUID: m.Invoice.ID, Valid: true}
			optionID = uuid.NullUUID{UUID: m.Option.ID, Valid: true}
		} else if !postgres.IsNotFound(err) {
			return err
		}

		fees, err := s.pricing.DepositFees(ctx, orgID, t.AssetID, amount, option)
		if err != nil {
			return fmt.Errorf("payments: price fees: %w", err)
		}
		p, err := s.q.CreatePayment(ctx, store.CreatePaymentParams{
			OrganizationID: orgID, InvoiceID: invoiceID, OptionID: optionID, AddressID: addr.ID, AssetID: t.AssetID,
			ProviderID: addr.ProviderID, TransferID: t.ID, Amount: t.Amount,
			FeeAmount: money.ToNumeric(fees.Fee), FeeBpsApplied: pgtype.Int4{Int32: fees.FeeBps, Valid: true},
			SpreadAmount: money.ToNumeric(fees.Spread), NetworkFeeAmount: money.ToNumeric(fees.NetworkFee), FeeScheduleID: fees.ScheduleID,
		})
		if err != nil {
			if postgres.IsUniqueViolation(err) {
				return nil // raced with another worker on the same transfer
			}
			return fmt.Errorf("payments: create payment: %w", err)
		}
		if option != nil {
			if _, err := s.checkout.ApplyPayment(ctx, option.ID, amount); err != nil {
				return err
			}
		}
		if _, err := s.events.Emit(ctx, orgID, events.PaymentDetected, ResourceType, p.ID, s.view(ctx, p, tx)); err != nil {
			return err
		}
		confirmed, err := s.chain.Confirmed(ctx, tx)
		if err != nil {
			return err
		}
		if confirmed {
			return s.credit(ctx, p, tx)
		}
		return nil
	})
}

// Settlement ---------------------------------------------------------------------------

// Stats summarises one reconciliation pass.
type Stats struct {
	Checked, Credited, Reverted int
}

// ReconcileDetected re-checks detected payments against their transaction:
// enough confirmations credits the merchant, a failed or dropped transaction
// reverts the payment. The confirmation tracker in the worker keeps
// transactions current; this only reacts.
func (s *Service) ReconcileDetected(ctx context.Context, batch int32) (Stats, error) {
	rows, err := s.q.ListDetectedPayments(ctx, batch)
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
			if err := s.Revert(ctx, r.Payment.ID, "transaction "+string(r.TxStatus)); err != nil {
				s.log.ErrorContext(ctx, "payments: revert failed", "payment", r.Payment.ID, "err", err)
				continue
			}
			st.Reverted++
		case r.TxStatus == store.TxStatusConfirmed || r.Confirmations >= r.RequiredConfirmations:
			if err := s.Credit(ctx, r.Payment.ID); err != nil {
				s.log.ErrorContext(ctx, "payments: credit failed", "payment", r.Payment.ID, "err", err)
				continue
			}
			st.Credited++
		}
	}
	return st, nil
}

// Credit settles a detected payment: ledger journal, invoice confirmation
// and events, in one transaction. Safe to call twice.
func (s *Service) Credit(ctx context.Context, paymentID uuid.UUID) error {
	return s.inTx(ctx, func(s *Service) error {
		p, err := s.q.GetPayment(ctx, paymentID)
		if err != nil {
			return postgres.MapNotFound(err)
		}
		if p.Status == store.PaymentStatusCredited || p.Status == store.PaymentStatusReverted {
			return nil
		}
		t, err := s.chain.Transfer(ctx, p.TransferID)
		if err != nil {
			return err
		}
		tx, err := s.chain.Transaction(ctx, t.TransactionID)
		if err != nil {
			return err
		}
		return s.credit(ctx, p, tx)
	})
}

func (s *Service) credit(ctx context.Context, p store.Payment, tx store.Transaction) error {
	p, err := s.q.CreditPayment(ctx, p.ID)
	if err != nil {
		if postgres.IsNotFound(err) {
			return nil // another worker got there first
		}
		return fmt.Errorf("payments: credit: %w", err)
	}
	amount := money.FromNumeric(p.Amount)
	fees := money.FromNumeric(p.FeeAmount).Add(money.FromNumeric(p.SpreadAmount)).Add(money.FromNumeric(p.NetworkFeeAmount))
	if _, err := s.ledger.Post(ctx, ledger.PaymentCredited(p.ID, p.OrganizationID, p.AssetID, amount, fees)); err != nil && !errors.Is(err, ledger.ErrAlreadyPosted) {
		return fmt.Errorf("payments: post ledger: %w", err)
	}
	if p.OptionID.Valid {
		if _, err := s.checkout.ConfirmPayment(ctx, p.OptionID.UUID); err != nil {
			return err
		}
	}
	view := s.view(ctx, p, tx)
	if _, err := s.events.Emit(ctx, p.OrganizationID, events.PaymentConfirmed, ResourceType, p.ID, view); err != nil {
		return err
	}
	_, err = s.events.Emit(ctx, p.OrganizationID, events.PaymentCredited, ResourceType, p.ID, view)
	return err
}

// Revert cancels a detected payment whose transaction did not make it on
// chain. Credited payments cannot be reverted here: that is a refund, a
// merchant decision handled through payouts.
func (s *Service) Revert(ctx context.Context, paymentID uuid.UUID, reason string) error {
	return s.inTx(ctx, func(s *Service) error {
		p, err := s.q.RevertPayment(ctx, paymentID)
		if err != nil {
			if postgres.IsNotFound(err) {
				return nil
			}
			return err
		}
		if p.OptionID.Valid {
			if _, err := s.checkout.ReversePayment(ctx, p.OptionID.UUID, money.FromNumeric(p.Amount)); err != nil {
				return err
			}
		}
		_, err = s.events.Emit(ctx, p.OrganizationID, events.PaymentReverted, ResourceType, p.ID, map[string]any{
			"id": p.ID, "invoice_id": p.InvoiceID, "reason": reason,
		})
		return err
	})
}

// Reads ------------------------------------------------------------------------------------

// Row is a payment with the columns the dashboard lists.
type Row = store.ListPaymentsRow

// List pages a merchant's payments, optionally by status.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, status *store.PaymentStatus, limit, offset int32) ([]Row, error) {
	var st store.NullPaymentStatus
	if status != nil {
		st = store.NullPaymentStatus{PaymentStatus: *status, Valid: true}
	}
	return s.q.ListPayments(ctx, store.ListPaymentsParams{OrganizationID: orgID, Status: st, Limit: limit, Offset: offset})
}

// Get returns one payment scoped to its merchant.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (store.Payment, error) {
	p, err := s.q.GetPaymentForOrganization(ctx, store.GetPaymentForOrganizationParams{ID: id, OrganizationID: orgID})
	return p, postgres.MapNotFound(err)
}

// ForInvoice lists the payments made against an invoice.
func (s *Service) ForInvoice(ctx context.Context, invoiceID uuid.UUID) ([]store.Payment, error) {
	return s.q.ListPaymentsForInvoice(ctx, uuid.NullUUID{UUID: invoiceID, Valid: true})
}

// Counts returns payments per status for filter tabs.
func (s *Service) Counts(ctx context.Context, orgID uuid.UUID) (map[store.PaymentStatus]int64, error) {
	rows, err := s.q.CountPaymentsByStatus(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[store.PaymentStatus]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Count
	}
	return out, nil
}

// Volume is gross and fees credited since a point in time.
type Volume struct {
	Gross, Fees decimal.Decimal
}

// VolumeSince sums credited payments of one asset since t.
func (s *Service) VolumeSince(ctx context.Context, orgID uuid.UUID, assetID int16, t time.Time) (Volume, error) {
	r, err := s.q.SumCreditedPayments(ctx, store.SumCreditedPaymentsParams{OrganizationID: orgID, AssetID: assetID, CreditedAt: &t})
	if err != nil {
		return Volume{}, err
	}
	return Volume{Gross: money.FromNumeric(r.Gross), Fees: money.FromNumeric(r.Fees)}, nil
}

// View ------------------------------------------------------------------------------------------

// PaymentView is the API / webhook shape of a payment.
type PaymentView struct {
	ID            uuid.UUID  `json:"id"`
	InvoiceID     *uuid.UUID `json:"invoice_id,omitempty"`
	Status        string     `json:"status"`
	Asset         string     `json:"asset"`
	Network       string     `json:"network"`
	Amount        string     `json:"amount"`
	Fee           string     `json:"fee"`
	Net           string     `json:"net"`
	TxHash        string     `json:"tx_hash"`
	Confirmations int32      `json:"confirmations"`
	Required      int32      `json:"required_confirmations"`
	DetectedAt    time.Time  `json:"detected_at"`
	ConfirmedAt   *time.Time `json:"confirmed_at,omitempty"`
	CreditedAt    *time.Time `json:"credited_at,omitempty"`
}

func (s *Service) view(ctx context.Context, p store.Payment, tx store.Transaction) PaymentView {
	v := PaymentView{
		ID: p.ID, Status: string(p.Status), TxHash: tx.Hash, Confirmations: tx.Confirmations,
		DetectedAt: p.DetectedAt, ConfirmedAt: p.ConfirmedAt, CreditedAt: p.CreditedAt,
	}
	if p.InvoiceID.Valid {
		id := p.InvoiceID.UUID
		v.InvoiceID = &id
	}
	amount := money.FromNumeric(p.Amount)
	fee := money.FromNumeric(p.FeeAmount).Add(money.FromNumeric(p.SpreadAmount)).Add(money.FromNumeric(p.NetworkFeeAmount))
	decimals := int16(money.Scale)
	if a, err := s.catalog.Asset(ctx, p.AssetID); err == nil {
		v.Asset, v.Network, v.Required, decimals = a.Code, a.Network.Code, a.Network.RequiredConfirmations, a.Decimals
	}
	v.Amount, v.Fee, v.Net = money.Format(amount, decimals), money.Format(fee, decimals), money.Format(amount.Sub(fee), decimals)
	return v
}
