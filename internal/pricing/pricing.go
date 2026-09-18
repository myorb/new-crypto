// Package pricing answers two questions for the commerce layer: what does a
// fiat price cost in a given asset right now (Quote), and what does the
// platform charge on a deposit or withdrawal (DepositFees, WithdrawalFees).
// It owns exchange rates and fee schedules and depends only on the catalog.
package pricing

import (
	"context"
	"errors"
	"fmt"
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
	ErrNoRate    = errors.New("pricing: no exchange rate for asset in this currency")
	ErrStaleRate = errors.New("pricing: exchange rate is too old")
	ErrBadAmount = errors.New("pricing: amount must be positive")
)

// Options tune the service.
type Options struct {
	MaxRateAge time.Duration // reject rates older than this when quoting; default 10 min
	RateLock   time.Duration // how long a quote stays payable at the quoted rate; default 15 min
}

// Service is the pricing domain.
type Service struct {
	q       *store.Queries
	pool    *pgxpool.Pool
	catalog *reference.Catalog
	opts    Options
}

// New builds the service.
func New(pool *pgxpool.Pool, catalog *reference.Catalog, opts Options) *Service {
	if opts.MaxRateAge == 0 {
		opts.MaxRateAge = 10 * time.Minute
	}
	if opts.RateLock == 0 {
		opts.RateLock = 15 * time.Minute
	}
	return &Service{q: store.New(pool), pool: pool, catalog: catalog, opts: opts}
}

// WithTx binds the service to a transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{q: s.q.WithTx(tx), catalog: s.catalog.WithTx(tx), opts: s.opts}
}

// RateLock is the configured quote validity window.
func (s *Service) RateLock() time.Duration { return s.opts.RateLock }

// Rates -----------------------------------------------------------------------

// RecordRate stores a fresh mid-market rate: `rate` units of `quote` buy one
// unit of the asset (e.g. USDT_TRON/USD = 0.9998).
func (s *Service) RecordRate(ctx context.Context, assetID int16, quote string, rate decimal.Decimal, source string) (store.ExchangeRate, error) {
	if !rate.IsPositive() {
		return store.ExchangeRate{}, ErrBadAmount
	}
	return s.q.InsertExchangeRate(ctx, store.InsertExchangeRateParams{
		BaseAssetID: assetID, Quote: quote, Rate: money.ToNumeric(rate), Source: source,
	})
}

// LatestRate returns the newest stored rate regardless of age.
func (s *Service) LatestRate(ctx context.Context, assetID int16, quote string) (store.ExchangeRate, error) {
	r, err := s.q.GetLatestExchangeRate(ctx, store.GetLatestExchangeRateParams{BaseAssetID: assetID, Quote: quote})
	if err != nil {
		if postgres.IsNotFound(err) {
			return store.ExchangeRate{}, ErrNoRate
		}
		return store.ExchangeRate{}, err
	}
	return r, nil
}

// FreshRate is LatestRate that also enforces MaxRateAge.
func (s *Service) FreshRate(ctx context.Context, assetID int16, quote string) (store.ExchangeRate, error) {
	r, err := s.LatestRate(ctx, assetID, quote)
	if err != nil {
		return store.ExchangeRate{}, err
	}
	if time.Since(r.FetchedAt) > s.opts.MaxRateAge {
		return store.ExchangeRate{}, ErrStaleRate
	}
	return r, nil
}

// LatestRates returns the newest rate per asset in one quote currency.
func (s *Service) LatestRates(ctx context.Context, quote string) ([]store.ExchangeRate, error) {
	return s.q.ListLatestExchangeRates(ctx, quote)
}

// PurgeRatesBefore drops rate history older than t (retention job).
func (s *Service) PurgeRatesBefore(ctx context.Context, t time.Time) (int64, error) {
	return s.q.DeleteExchangeRatesBefore(ctx, t)
}

// Fee schedules -------------------------------------------------------------------

// Schedule resolves the fee schedule that applies to (merchant, asset). With
// no configured schedule at all it returns a zero-fee schedule so callers
// never branch on "no fees configured".
func (s *Service) Schedule(ctx context.Context, orgID uuid.UUID, assetID int16) (store.FeeSchedule, error) {
	fs, err := s.q.ResolveFeeSchedule(ctx, store.ResolveFeeScheduleParams{
		OrganizationID: uuid.NullUUID{UUID: orgID, Valid: true},
		AssetID:        pgtype.Int2{Int16: assetID, Valid: true},
	})
	if err != nil {
		if postgres.IsNotFound(err) {
			return store.FeeSchedule{NetworkFeePayer: store.FeePayerMerchant, PassNetworkFee: true}, nil
		}
		return store.FeeSchedule{}, err
	}
	return fs, nil
}

// ScheduleInput is the editable part of a fee schedule.
type ScheduleInput struct {
	OrganizationID     uuid.NullUUID // empty = platform default
	AssetID            *int16        // nil = every asset
	DepositFeeBps      int32
	DepositFeeFixed    decimal.Decimal
	DepositFeeMin      *decimal.Decimal
	DepositFeeMax      *decimal.Decimal
	DepositNetworkFee  decimal.Decimal
	NetworkFeePayer    store.FeePayer
	SpreadBps          int32
	WithdrawalFeeBps   int32
	WithdrawalFeeFixed decimal.Decimal
	PassNetworkFee     bool
	FixedFeeCurrency   *string // required when AssetID is nil and any fixed amount is non-zero
	EffectiveFrom      time.Time
}

// ReplaceSchedule closes the currently open schedule for the same scope and
// inserts the new one in one transaction. Rows are never repriced in place.
func (s *Service) ReplaceSchedule(ctx context.Context, in ScheduleInput) (store.FeeSchedule, error) {
	if in.NetworkFeePayer == "" {
		in.NetworkFeePayer = store.FeePayerMerchant
	}
	if in.EffectiveFrom.IsZero() {
		in.EffectiveFrom = time.Now()
	}
	return postgres.InTxRet(ctx, s.pool, func(tx pgx.Tx) (store.FeeSchedule, error) {
		q := s.q.WithTx(tx)
		var assetID pgtype.Int2
		if in.AssetID != nil {
			assetID = pgtype.Int2{Int16: *in.AssetID, Valid: true}
		}
		// Close the exact-scope predecessor, if one is open.
		existing, err := q.ListFeeSchedules(ctx, in.OrganizationID)
		if err != nil {
			return store.FeeSchedule{}, err
		}
		for _, fs := range existing {
			if fs.EffectiveTo == nil && fs.AssetID == assetID {
				if _, err := q.CloseFeeSchedule(ctx, store.CloseFeeScheduleParams{EffectiveTo: &in.EffectiveFrom, ID: fs.ID}); err != nil {
					return store.FeeSchedule{}, err
				}
			}
		}
		return q.CreateFeeSchedule(ctx, store.CreateFeeScheduleParams{
			OrganizationID:     in.OrganizationID,
			AssetID:            assetID,
			DepositFeeBps:      in.DepositFeeBps,
			DepositFeeFixed:    money.ToNumeric(in.DepositFeeFixed),
			DepositFeeMin:      money.NullNumeric(in.DepositFeeMin),
			DepositFeeMax:      money.NullNumeric(in.DepositFeeMax),
			DepositNetworkFee:  money.ToNumeric(in.DepositNetworkFee),
			NetworkFeePayer:    in.NetworkFeePayer,
			SpreadBps:          in.SpreadBps,
			WithdrawalFeeBps:   in.WithdrawalFeeBps,
			WithdrawalFeeFixed: money.ToNumeric(in.WithdrawalFeeFixed),
			PassNetworkFee:     in.PassNetworkFee,
			FixedFeeCurrency:   in.FixedFeeCurrency,
			EffectiveFrom:      in.EffectiveFrom,
		})
	})
}

// Schedules lists the history for one scope (platform when org is empty).
func (s *Service) Schedules(ctx context.Context, org uuid.NullUUID) ([]store.FeeSchedule, error) {
	return s.q.ListFeeSchedules(ctx, org)
}

// Quotes ----------------------------------------------------------------------------

// Quote is what a payer is asked to send for an invoice priced in fiat.
type Quote struct {
	AssetID      int16
	AmountDue    decimal.Decimal // in asset units, rounded up to the asset's decimals
	ExchangeRate decimal.Decimal // price_currency per 1 asset as quoted (after spread)
	SourceRate   decimal.Decimal // mid-market rate the quote came from
	SpreadBps    int32
	RateID       *int64 // exchange_rates.id, nil when price is already in the asset
	LockedUntil  time.Time
}

// Quote prices `priceAmount` of `priceCurrency` in the asset for a merchant,
// applying the merchant's spread. When the invoice is already priced in the
// asset itself the quote is 1:1 with no spread.
func (s *Service) Quote(ctx context.Context, orgID uuid.UUID, assetID int16, priceCurrency string, priceAmount decimal.Decimal) (Quote, error) {
	if !priceAmount.IsPositive() {
		return Quote{}, ErrBadAmount
	}
	asset, err := s.catalog.EnabledAsset(ctx, assetID)
	if err != nil {
		return Quote{}, err
	}
	lockedUntil := time.Now().Add(s.opts.RateLock)
	if priceCurrency == asset.Code {
		return Quote{
			AssetID: assetID, AmountDue: money.RoundUp(priceAmount, asset.Decimals),
			ExchangeRate: decimal.NewFromInt(1), SourceRate: decimal.NewFromInt(1), LockedUntil: lockedUntil,
		}, nil
	}
	rate, err := s.FreshRate(ctx, assetID, priceCurrency)
	if err != nil {
		return Quote{}, err
	}
	fs, err := s.Schedule(ctx, orgID, assetID)
	if err != nil {
		return Quote{}, err
	}
	source := money.FromNumeric(rate.Rate)
	// The merchant is quoted a slightly worse rate: fewer fiat units per coin,
	// so the payer sends a little more. spread_amount is realised at payment time.
	quoted := source.Sub(money.Bps(source, fs.SpreadBps))
	if !quoted.IsPositive() {
		return Quote{}, fmt.Errorf("pricing: spread %d bps leaves no rate", fs.SpreadBps)
	}
	amountDue := money.RoundUp(priceAmount.Div(quoted), asset.Decimals)
	id := rate.ID
	return Quote{
		AssetID: assetID, AmountDue: amountDue, ExchangeRate: quoted, SourceRate: source,
		SpreadBps: fs.SpreadBps, RateID: &id, LockedUntil: lockedUntil,
	}, nil
}

// Fees --------------------------------------------------------------------------------

// DepositFees is the breakdown charged on one incoming payment.
type DepositFees struct {
	Fee        decimal.Decimal // percentage + fixed, clamped to [min, max]
	FeeBps     int32
	Spread     decimal.Decimal // value captured between source and quoted rate
	NetworkFee decimal.Decimal // flat charge when the merchant bears it
	ScheduleID uuid.NullUUID
}

// Total is everything the platform keeps from the payment.
func (f DepositFees) Total() decimal.Decimal { return f.Fee.Add(f.Spread).Add(f.NetworkFee) }

// DepositFees computes the platform's take on `amount` of the asset. The
// option (if the payment matched an invoice) supplies the quoted and source
// rates for the spread; without one only the schedule applies.
func (s *Service) DepositFees(ctx context.Context, orgID uuid.UUID, assetID int16, amount decimal.Decimal, option *store.InvoicePaymentOption) (DepositFees, error) {
	if !amount.IsPositive() {
		return DepositFees{}, ErrBadAmount
	}
	asset, err := s.catalog.Asset(ctx, assetID)
	if err != nil {
		return DepositFees{}, err
	}
	fs, err := s.Schedule(ctx, orgID, assetID)
	if err != nil {
		return DepositFees{}, err
	}
	out := DepositFees{FeeBps: fs.DepositFeeBps, ScheduleID: uuid.NullUUID{UUID: fs.ID, Valid: fs.ID != uuid.Nil}}

	fixed, err := s.inAsset(ctx, asset, fs, money.FromNumeric(fs.DepositFeeFixed), option)
	if err != nil {
		return DepositFees{}, err
	}
	fee := money.Bps(amount, fs.DepositFeeBps).Add(fixed)
	var min, max *decimal.Decimal
	if fs.DepositFeeMin.Valid {
		v, err := s.inAsset(ctx, asset, fs, money.FromNumeric(fs.DepositFeeMin), option)
		if err != nil {
			return DepositFees{}, err
		}
		min = &v
	}
	if fs.DepositFeeMax.Valid {
		v, err := s.inAsset(ctx, asset, fs, money.FromNumeric(fs.DepositFeeMax), option)
		if err != nil {
			return DepositFees{}, err
		}
		max = &v
	}
	out.Fee = money.RoundDown(money.Clamp(fee, min, max), asset.Decimals)

	if option != nil && option.SourceRate.Valid && option.ExchangeRate.Valid {
		src, quoted := money.FromNumeric(option.SourceRate), money.FromNumeric(option.ExchangeRate)
		if src.IsPositive() && quoted.IsPositive() && quoted.LessThan(src) {
			// amount at quoted rate is worth amount*quoted in fiat; at source it would have
			// taken amount*quoted/src coins. The difference stays with the platform.
			out.Spread = money.RoundDown(amount.Sub(amount.Mul(quoted).Div(src)), asset.Decimals)
		}
	}
	if fs.NetworkFeePayer == store.FeePayerMerchant {
		nf, err := s.inAsset(ctx, asset, fs, money.FromNumeric(fs.DepositNetworkFee), option)
		if err != nil {
			return DepositFees{}, err
		}
		out.NetworkFee = money.RoundDown(nf, asset.Decimals)
	}
	// The database refuses fees above the amount; keep the merchant's net >= 0.
	if out.Total().GreaterThan(amount) {
		over := out.Total().Sub(amount)
		out.NetworkFee = decimal.Max(decimal.Zero, out.NetworkFee.Sub(over))
		if out.Total().GreaterThan(amount) {
			out.Fee = decimal.Max(decimal.Zero, amount.Sub(out.Spread))
		}
	}
	return out, nil
}

// WithdrawalFees is the platform fee on a payout (network gas is separate).
type WithdrawalFees struct {
	Fee        decimal.Decimal
	FeeBps     int32
	ScheduleID uuid.NullUUID
}

// WithdrawalFees computes the platform fee on a payout of `amount`.
func (s *Service) WithdrawalFees(ctx context.Context, orgID uuid.UUID, assetID int16, amount decimal.Decimal) (WithdrawalFees, error) {
	if !amount.IsPositive() {
		return WithdrawalFees{}, ErrBadAmount
	}
	asset, err := s.catalog.Asset(ctx, assetID)
	if err != nil {
		return WithdrawalFees{}, err
	}
	fs, err := s.Schedule(ctx, orgID, assetID)
	if err != nil {
		return WithdrawalFees{}, err
	}
	fixed, err := s.inAsset(ctx, asset, fs, money.FromNumeric(fs.WithdrawalFeeFixed), nil)
	if err != nil {
		return WithdrawalFees{}, err
	}
	fee := money.RoundDown(money.Bps(amount, fs.WithdrawalFeeBps).Add(fixed), asset.Decimals)
	return WithdrawalFees{Fee: fee, FeeBps: fs.WithdrawalFeeBps, ScheduleID: uuid.NullUUID{UUID: fs.ID, Valid: fs.ID != uuid.Nil}}, nil
}

// inAsset converts a schedule's fixed amount to asset units. Asset-scoped
// schedules are already denominated in the asset. Currency-denominated ones
// use the option's quoted rate when the currencies match (so the fee the
// payer saw is the fee charged), otherwise the latest stored rate.
func (s *Service) inAsset(ctx context.Context, asset reference.Asset, fs store.FeeSchedule, v decimal.Decimal, option *store.InvoicePaymentOption) (decimal.Decimal, error) {
	if v.IsZero() {
		return decimal.Zero, nil
	}
	if fs.FixedFeeCurrency == nil {
		return v, nil
	}
	currency := *fs.FixedFeeCurrency
	if option != nil && option.ExchangeRate.Valid {
		// The option's rate is price_currency per asset. We only know the invoice
		// currency through the caller; the common case (USD fees, USD invoices)
		// matches, so try the option first and fall back to a stored rate.
		if rate := money.FromNumeric(option.ExchangeRate); rate.IsPositive() {
			if r, err := s.LatestRate(ctx, asset.ID, currency); err == nil && ratesClose(money.FromNumeric(r.Rate), rate) {
				return v.Div(rate), nil
			}
		}
	}
	r, err := s.LatestRate(ctx, asset.ID, currency)
	if err != nil {
		return decimal.Zero, fmt.Errorf("pricing: convert %s fee to %s: %w", currency, asset.Code, err)
	}
	return v.Div(money.FromNumeric(r.Rate)), nil
}

// ratesClose reports whether two rates are within 5 %, i.e. plausibly the same
// currency pair.
func ratesClose(a, b decimal.Decimal) bool {
	if !a.IsPositive() || !b.IsPositive() {
		return false
	}
	ratio := a.Div(b)
	return ratio.GreaterThan(decimal.NewFromFloat(0.95)) && ratio.LessThan(decimal.NewFromFloat(1.05))
}
