// Package ledger is the only writer of money. Every balance the platform shows
// is derived from journals posted here; other domains describe what happened
// (a payment credited, a withdrawal completed) and the ledger records it.
//
// Sign convention: entries are signed and a journal's entries sum to zero.
// Positive means credit, negative means debit. Merchant and revenue accounts
// (merchant_*, platform_fee_revenue) are credit-normal: a positive balance is
// value held for the merchant or earned by the platform. Asset accounts
// (platform_hot_wallet, platform_cold_wallet) and the expense account
// (platform_network_fees) are debit-normal: custodied funds show as negative
// balances and fees paid to networks as negative expense. The merchant_balances
// view only exposes the merchant accounts, so merchants see positive numbers.
//
// The database double-checks all of this: journals must balance at COMMIT,
// entries are immutable, and ledger_balances is maintained by a trigger.
package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/store"
)

var (
	ErrUnbalanced     = errors.New("ledger: posting does not balance")
	ErrEmptyPosting   = errors.New("ledger: posting has no lines")
	ErrZeroLine       = errors.New("ledger: posting line has zero amount")
	ErrMixedAssets    = errors.New("ledger: posting lines must share one asset")
	ErrAlreadyPosted  = errors.New("ledger: this business event was already posted")
	ErrBadAccountType = errors.New("ledger: merchant account types need an organization and platform types must not have one")
)

// Reference types used in journals.
const (
	RefPayment          = "payment"
	RefWithdrawal       = "withdrawal"
	RefInternalTransfer = "internal_transfer"
	RefAdjustment       = "adjustment"
)

// Service posts journals and reads balances.
type Service struct {
	q    *store.Queries
	pool *pgxpool.Pool // nil when bound to a transaction
}

// New builds the service.
func New(pool *pgxpool.Pool) *Service { return &Service{q: store.New(pool), pool: pool} }

// WithTx binds the service to a caller's transaction. Post then writes into
// that transaction and the caller decides when to commit.
func (s *Service) WithTx(tx pgx.Tx) *Service { return &Service{q: s.q.WithTx(tx)} }

// Line is one signed movement on one account.
type Line struct {
	OrganizationID uuid.NullUUID // required for merchant_* types, must be empty for platform_* types
	AssetID        int16
	Type           store.LedgerAccountType
	Amount         decimal.Decimal // positive = credit, negative = debit
}

// Posting is a balanced set of lines that records one business event exactly
// once. (EventType, ReferenceType, ReferenceID) is the idempotency key.
type Posting struct {
	EventType     string // "payment.credited", "withdrawal.requested", ...
	ReferenceType string // RefPayment, RefWithdrawal, ...
	ReferenceID   uuid.UUID
	Description   string
	Lines         []Line
}

// Validate checks the posting in Go before touching the database so callers
// get a precise error rather than a deferred-trigger failure at commit.
func (p Posting) Validate() error {
	if len(p.Lines) < 2 {
		return ErrEmptyPosting
	}
	if p.EventType == "" || p.ReferenceType == "" || p.ReferenceID == uuid.Nil {
		return errors.New("ledger: event type, reference type and reference id are required")
	}
	sum := decimal.Zero
	asset := p.Lines[0].AssetID
	for _, l := range p.Lines {
		if l.Amount.IsZero() {
			return ErrZeroLine
		}
		if l.AssetID != asset {
			return ErrMixedAssets
		}
		isMerchant := len(l.Type) > 9 && l.Type[:9] == "merchant_"
		if isMerchant != l.OrganizationID.Valid {
			return ErrBadAccountType
		}
		sum = sum.Add(l.Amount)
	}
	if !sum.IsZero() {
		return fmt.Errorf("%w: sum = %s", ErrUnbalanced, sum)
	}
	return nil
}

// Post writes the journal and its entries. When the service is not bound to
// a transaction it opens one. Posting the same event twice returns
// ErrAlreadyPosted without writing anything.
func (s *Service) Post(ctx context.Context, p Posting) (store.LedgerJournal, error) {
	if err := p.Validate(); err != nil {
		return store.LedgerJournal{}, err
	}
	if s.pool != nil {
		return postgres.InTxRet(ctx, s.pool, func(tx pgx.Tx) (store.LedgerJournal, error) {
			return s.WithTx(tx).post(ctx, p)
		})
	}
	return s.post(ctx, p)
}

func (s *Service) post(ctx context.Context, p Posting) (store.LedgerJournal, error) {
	var desc *string
	if p.Description != "" {
		desc = &p.Description
	}
	journal, err := s.q.CreateLedgerJournal(ctx, store.CreateLedgerJournalParams{
		EventType: p.EventType, ReferenceType: p.ReferenceType, ReferenceID: p.ReferenceID, Description: desc,
	})
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return store.LedgerJournal{}, ErrAlreadyPosted
		}
		return store.LedgerJournal{}, fmt.Errorf("ledger: create journal: %w", err)
	}
	for _, l := range p.Lines {
		acct, err := s.account(ctx, l.OrganizationID, l.AssetID, l.Type)
		if err != nil {
			return store.LedgerJournal{}, err
		}
		if _, err := s.q.CreateLedgerEntry(ctx, store.CreateLedgerEntryParams{
			JournalID: journal.ID, AccountID: acct.ID, Amount: money.ToNumeric(l.Amount),
		}); err != nil {
			return store.LedgerJournal{}, fmt.Errorf("ledger: create entry: %w", err)
		}
	}
	return journal, nil
}

// account returns the account row, creating it on first use.
func (s *Service) account(ctx context.Context, org uuid.NullUUID, assetID int16, typ store.LedgerAccountType) (store.LedgerAccount, error) {
	params := store.GetLedgerAccountParams{OrganizationID: org, AssetID: assetID, Type: typ}
	acct, err := s.q.GetLedgerAccount(ctx, params)
	if err == nil {
		return acct, nil
	}
	if !postgres.IsNotFound(err) {
		return store.LedgerAccount{}, fmt.Errorf("ledger: get account: %w", err)
	}
	if _, err := s.q.CreateLedgerAccount(ctx, store.CreateLedgerAccountParams{OrganizationID: org, AssetID: assetID, Type: typ}); err != nil {
		return store.LedgerAccount{}, fmt.Errorf("ledger: create account: %w", err)
	}
	acct, err = s.q.GetLedgerAccount(ctx, params)
	if err != nil {
		return store.LedgerAccount{}, fmt.Errorf("ledger: reload account: %w", err)
	}
	return acct, nil
}

// Balance is one merchant's position in one asset.
type Balance struct {
	AssetID     int16
	AssetCode   string
	Symbol      string
	NetworkCode string
	Available   decimal.Decimal
	Pending     decimal.Decimal
	Locked      decimal.Decimal
}

func toBalance(r store.MerchantBalance) Balance {
	return Balance{
		AssetID: r.AssetID, AssetCode: r.AssetCode, Symbol: r.Symbol, NetworkCode: r.NetworkCode,
		Available: money.FromNumeric(r.Available), Pending: money.FromNumeric(r.Pending), Locked: money.FromNumeric(r.Locked),
	}
}

// MerchantBalances lists every asset a merchant holds.
func (s *Service) MerchantBalances(ctx context.Context, orgID uuid.UUID) ([]Balance, error) {
	rows, err := s.q.GetMerchantBalances(ctx, uuid.NullUUID{UUID: orgID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]Balance, len(rows))
	for i, r := range rows {
		out[i] = toBalance(r)
	}
	return out, nil
}

// MerchantBalance returns one merchant's position in one asset; zero if the
// merchant never held it.
func (s *Service) MerchantBalance(ctx context.Context, orgID uuid.UUID, assetID int16) (Balance, error) {
	r, err := s.q.GetMerchantBalance(ctx, store.GetMerchantBalanceParams{
		OrganizationID: uuid.NullUUID{UUID: orgID, Valid: true}, AssetID: assetID,
	})
	if err != nil {
		if postgres.IsNotFound(err) {
			return Balance{AssetID: assetID}, nil
		}
		return Balance{}, err
	}
	return toBalance(r), nil
}

// Available is the spendable balance of a merchant in one asset.
func (s *Service) Available(ctx context.Context, orgID uuid.UUID, assetID int16) (decimal.Decimal, error) {
	b, err := s.MerchantBalance(ctx, orgID, assetID)
	return b.Available, err
}

// JournalFor returns the journal for a business event, or ErrNotFound.
func (s *Service) JournalFor(ctx context.Context, eventType, refType string, refID uuid.UUID) (store.LedgerJournal, error) {
	j, err := s.q.GetLedgerJournalByReference(ctx, store.GetLedgerJournalByReferenceParams{
		EventType: eventType, ReferenceType: refType, ReferenceID: refID,
	})
	return j, postgres.MapNotFound(err)
}

// Entries lists the lines of a journal with their accounts.
func (s *Service) Entries(ctx context.Context, journalID uuid.UUID) ([]store.ListLedgerEntriesForJournalRow, error) {
	return s.q.ListLedgerEntriesForJournal(ctx, journalID)
}

// PlatformBalance is one platform account's position.
type PlatformBalance struct {
	Account store.LedgerAccount
	Balance decimal.Decimal
}

// PlatformBalances lists the platform's own accounts (fees, wallets).
func (s *Service) PlatformBalances(ctx context.Context) ([]PlatformBalance, error) {
	rows, err := s.q.ListPlatformBalances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PlatformBalance, len(rows))
	for i, r := range rows {
		out[i] = PlatformBalance{Account: r.LedgerAccount, Balance: money.FromNumeric(r.Balance)}
	}
	return out, nil
}

// Helpers that build the standard postings so the sign convention lives in
// one file. Amounts are all positive inputs.

func merchant(org uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: org, Valid: true} }

// PaymentCredited: the merchant is paid `amount` of which `fees` stays with the
// platform; the hot wallet now custodies the full amount.
func PaymentCredited(paymentID, org uuid.UUID, assetID int16, amount, fees decimal.Decimal) Posting {
	return Posting{
		EventType: "payment.credited", ReferenceType: RefPayment, ReferenceID: paymentID,
		Description: "deposit credited to merchant",
		Lines: compact(
			Line{OrganizationID: merchant(org), AssetID: assetID, Type: store.LedgerAccountTypeMerchantAvailable, Amount: amount.Sub(fees)},
			Line{AssetID: assetID, Type: store.LedgerAccountTypePlatformFeeRevenue, Amount: fees},
			Line{AssetID: assetID, Type: store.LedgerAccountTypePlatformHotWallet, Amount: amount.Neg()},
		),
	}
}

// WithdrawalRequested moves amount+fee from available to locked.
func WithdrawalRequested(withdrawalID, org uuid.UUID, assetID int16, total decimal.Decimal) Posting {
	return Posting{
		EventType: "withdrawal.requested", ReferenceType: RefWithdrawal, ReferenceID: withdrawalID,
		Description: "funds locked for withdrawal",
		Lines: []Line{
			{OrganizationID: merchant(org), AssetID: assetID, Type: store.LedgerAccountTypeMerchantAvailable, Amount: total.Neg()},
			{OrganizationID: merchant(org), AssetID: assetID, Type: store.LedgerAccountTypeMerchantLocked, Amount: total},
		},
	}
}

// WithdrawalReleased reverses WithdrawalRequested (rejected, cancelled, failed).
func WithdrawalReleased(withdrawalID, org uuid.UUID, assetID int16, total decimal.Decimal, reason string) Posting {
	return Posting{
		EventType: "withdrawal." + reason, ReferenceType: RefWithdrawal, ReferenceID: withdrawalID,
		Description: "locked funds released back to merchant",
		Lines: []Line{
			{OrganizationID: merchant(org), AssetID: assetID, Type: store.LedgerAccountTypeMerchantLocked, Amount: total.Neg()},
			{OrganizationID: merchant(org), AssetID: assetID, Type: store.LedgerAccountTypeMerchantAvailable, Amount: total},
		},
	}
}

// WithdrawalCompleted: locked funds leave; the platform keeps the fee and the
// hot wallet holds `amount` less.
func WithdrawalCompleted(withdrawalID, org uuid.UUID, assetID int16, amount, fee decimal.Decimal) Posting {
	return Posting{
		EventType: "withdrawal.completed", ReferenceType: RefWithdrawal, ReferenceID: withdrawalID,
		Description: "withdrawal paid out",
		Lines: compact(
			Line{OrganizationID: merchant(org), AssetID: assetID, Type: store.LedgerAccountTypeMerchantLocked, Amount: amount.Add(fee).Neg()},
			Line{AssetID: assetID, Type: store.LedgerAccountTypePlatformFeeRevenue, Amount: fee},
			Line{AssetID: assetID, Type: store.LedgerAccountTypePlatformHotWallet, Amount: amount},
		),
	}
}

// NetworkFeePaid records the native coin a sweep or payout burned as gas.
func NetworkFeePaid(refType string, refID uuid.UUID, nativeAssetID int16, cost decimal.Decimal) Posting {
	return Posting{
		EventType: refType + ".network_fee", ReferenceType: refType, ReferenceID: refID,
		Description: "network fee paid from hot wallet",
		Lines: []Line{
			{AssetID: nativeAssetID, Type: store.LedgerAccountTypePlatformNetworkFees, Amount: cost.Neg()},
			{AssetID: nativeAssetID, Type: store.LedgerAccountTypePlatformHotWallet, Amount: cost},
		},
	}
}

// Rebalance moves custody between hot and cold wallets.
func Rebalance(transferID uuid.UUID, assetID int16, amount decimal.Decimal, toCold bool) Posting {
	from, to := store.LedgerAccountTypePlatformHotWallet, store.LedgerAccountTypePlatformColdWallet
	if !toCold {
		from, to = to, from
	}
	return Posting{
		EventType: "internal_transfer.rebalanced", ReferenceType: RefInternalTransfer, ReferenceID: transferID,
		Description: "custody moved between platform wallets",
		Lines: []Line{
			{AssetID: assetID, Type: from, Amount: amount},     // holds less (toward zero)
			{AssetID: assetID, Type: to, Amount: amount.Neg()}, // holds more (more negative)
		},
	}
}

// compact drops zero lines (a fee of 0 must not produce an entry, the
// database rejects amount = 0).
func compact(lines ...Line) []Line {
	out := make([]Line, 0, len(lines))
	for _, l := range lines {
		if !l.Amount.IsZero() {
			out = append(out, l)
		}
	}
	return out
}
