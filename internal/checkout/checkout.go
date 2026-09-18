// Package checkout owns invoices: what a merchant asked a customer to pay,
// the assets it may be paid in (each with a locked quote), and the address a
// customer was given. It asks pricing for quotes and chain for addresses, and
// it announces state changes through events. Payments move invoices along by
// calling ApplyPayment / ConfirmPayment.
package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/customers"
	"templ-app/internal/events"
	"templ-app/internal/money"
	"templ-app/internal/org"
	"templ-app/internal/postgres"
	"templ-app/internal/pricing"
	"templ-app/internal/reference"
	"templ-app/internal/store"
)

var (
	ErrNotFound          = postgres.ErrNotFound
	ErrBadPrice          = errors.New("checkout: price must be positive")
	ErrBadCurrency       = errors.New("checkout: currency must be an upper-case ISO code or asset code")
	ErrBadCallbackURL    = errors.New("checkout: callback URL must be https and public")
	ErrBadReturnURL      = errors.New("checkout: return URL must be http(s)")
	ErrNoPayableAssets   = errors.New("checkout: no asset could be quoted for this invoice")
	ErrNotPayable        = errors.New("checkout: invoice can no longer be paid")
	ErrOptionUnavailable = errors.New("checkout: this asset is not offered on the invoice")
	ErrOtherOptionChosen = errors.New("checkout: a different asset was already selected for this invoice")
	ErrExternalIDTaken   = errors.New("checkout: external_id already used by another invoice")
	ErrNotCancellable    = errors.New("checkout: only unpaid invoices can be cancelled")
	ErrCustomerBlocked   = errors.New("checkout: customer is blocked")
	ErrCustomerUnknown   = errors.New("checkout: customer does not belong to this organization")
)

var currencyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

// ResourceType is what events and ledger journals call an invoice.
const ResourceType = "invoice"

// Options tune the service.
type Options struct {
	DefaultTTL   time.Duration // invoice lifetime when the caller gives none; default 1h
	MaxTTL       time.Duration // upper bound on requested lifetimes; default 7 days
	AllowPrivate bool          // pass-through to the SSRF check (dev only)
}

// Service is the checkout domain.
type Service struct {
	q         *store.Queries
	pool      *pgxpool.Pool
	catalog   *reference.Catalog
	pricing   *pricing.Service
	chain     *chain.Service
	events    *events.Service
	org       *org.Service
	customers *customers.Service
	opts      Options
}

// New builds the service.
func New(pool *pgxpool.Pool, catalog *reference.Catalog, pr *pricing.Service, ch *chain.Service, ev *events.Service, o *org.Service, cu *customers.Service, opts Options) *Service {
	if opts.DefaultTTL == 0 {
		opts.DefaultTTL = time.Hour
	}
	if opts.MaxTTL == 0 {
		opts.MaxTTL = 7 * 24 * time.Hour
	}
	return &Service{q: store.New(pool), pool: pool, catalog: catalog, pricing: pr, chain: ch, events: ev, org: o, customers: cu, opts: opts}
}

// WithTx binds the service and its collaborators to one transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{
		q: s.q.WithTx(tx), catalog: s.catalog.WithTx(tx), pricing: s.pricing.WithTx(tx),
		chain: s.chain.WithTx(tx), events: s.events.WithTx(tx), org: s.org.WithTx(tx), customers: s.customers.WithTx(tx), opts: s.opts,
	}
}

func (s *Service) inTx(ctx context.Context, fn func(s *Service) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(s.WithTx(tx)) })
}

// Types ---------------------------------------------------------------------------------

// Option is one way to pay an invoice.
type Option struct {
	store.InvoicePaymentOption
	Asset       store.Asset
	NetworkCode string
	NetworkName string
	Address     *string
	AddressMemo *string
}

// Invoice is an invoice with its options.
type Invoice struct {
	store.Invoice
	Options []Option
}

// Selected returns the chosen option, if any.
func (i Invoice) Selected() *Option {
	for k := range i.Options {
		if i.Options[k].IsSelected {
			return &i.Options[k]
		}
	}
	return nil
}

// Payable reports whether a customer may still pay this invoice.
func (i Invoice) Payable(now time.Time) bool {
	switch i.Status {
	case store.InvoiceStatusNew, store.InvoiceStatusPartial:
		return now.Before(i.ExpiresAt)
	}
	return false
}

// CreateInput is a new invoice request.
type CreateInput struct {
	OrganizationID  uuid.UUID
	ExternalID      *string
	PriceCurrency   string // "USD", "EUR" or an asset code like "USDT_TRON"
	PriceAmount     decimal.Decimal
	Description     *string
	CustomerEmail   *string
	CustomerID      uuid.NullUUID // known payer; otherwise resolved from CustomerEmail
	PaymentLinkID   uuid.NullUUID // set by OpenLink
	CallbackURL     *string
	ReturnURL       *string
	Metadata        map[string]any
	ExpiresIn       time.Duration // 0 = DefaultTTL
	AssetIDs        []int16       // empty = merchant's enabled assets, else every enabled asset
	CreatedByAPIKey uuid.NullUUID
}

// Create --------------------------------------------------------------------------------

// Create validates the request, quotes every payable asset and stores the
// invoice with its options in one transaction. Assets without a fresh rate
// are skipped; the invoice fails only when none can be quoted.
func (s *Service) Create(ctx context.Context, in CreateInput) (Invoice, error) {
	if !in.PriceAmount.IsPositive() {
		return Invoice{}, ErrBadPrice
	}
	in.PriceCurrency = strings.ToUpper(strings.TrimSpace(in.PriceCurrency))
	if !currencyRe.MatchString(in.PriceCurrency) {
		return Invoice{}, ErrBadCurrency
	}
	if in.CallbackURL != nil && *in.CallbackURL != "" {
		if err := events.ValidateEndpointURL(*in.CallbackURL, s.opts.AllowPrivate); err != nil {
			return Invoice{}, ErrBadCallbackURL
		}
	}
	if in.ReturnURL != nil && *in.ReturnURL != "" {
		u, err := url.Parse(*in.ReturnURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return Invoice{}, ErrBadReturnURL
		}
	}
	ttl := in.ExpiresIn
	if ttl <= 0 {
		ttl = s.opts.DefaultTTL
	}
	if ttl > s.opts.MaxTTL {
		ttl = s.opts.MaxTTL
	}
	meta := []byte("{}")
	if in.Metadata != nil {
		var err error
		if meta, err = json.Marshal(in.Metadata); err != nil {
			return Invoice{}, fmt.Errorf("checkout: metadata: %w", err)
		}
	}
	if in.CustomerEmail != nil {
		e := strings.ToLower(strings.TrimSpace(*in.CustomerEmail))
		in.CustomerEmail = &e
	}

	var out Invoice
	err := s.inTx(ctx, func(s *Service) error {
		assetIDs, err := s.payableAssets(ctx, in.OrganizationID, in.AssetIDs)
		if err != nil {
			return err
		}
		if err := s.resolveCustomer(ctx, &in); err != nil {
			return err
		}
		inv, err := s.q.CreateInvoice(ctx, store.CreateInvoiceParams{
			OrganizationID: in.OrganizationID, ExternalID: in.ExternalID, PriceCurrency: in.PriceCurrency,
			PriceAmount: money.ToNumeric(in.PriceAmount), Description: in.Description, CustomerEmail: in.CustomerEmail,
			CallbackUrl: in.CallbackURL, ReturnUrl: in.ReturnURL, Metadata: meta,
			ExpiresAt: time.Now().Add(ttl), CreatedByApiKey: in.CreatedByAPIKey,
			CustomerID: in.CustomerID, PaymentLinkID: in.PaymentLinkID,
		})
		if err != nil {
			if postgres.IsUniqueViolation(err) {
				return ErrExternalIDTaken
			}
			return fmt.Errorf("checkout: create invoice: %w", err)
		}
		quoted := 0
		for _, assetID := range assetIDs {
			if _, err := s.quoteOption(ctx, inv, assetID, nil); err != nil {
				if errors.Is(err, pricing.ErrNoRate) || errors.Is(err, pricing.ErrStaleRate) || errors.Is(err, reference.ErrNoProvider) || errors.Is(err, reference.ErrAssetDisabled) {
					continue
				}
				return err
			}
			quoted++
		}
		if quoted == 0 {
			return ErrNoPayableAssets
		}
		out, err = s.load(ctx, inv)
		if err != nil {
			return err
		}
		_, err = s.events.Emit(ctx, inv.OrganizationID, events.InvoiceCreated, ResourceType, inv.ID, View(out))
		return err
	})
	return out, err
}

// resolveCustomer attaches the payer: an explicit CustomerID must belong to
// the organization, otherwise a CustomerEmail is looked up or created.
// Blocked customers cannot open invoices.
func (s *Service) resolveCustomer(ctx context.Context, in *CreateInput) error {
	var c store.Customer
	switch {
	case in.CustomerID.Valid:
		var err error
		if c, err = s.customers.Get(ctx, in.OrganizationID, in.CustomerID.UUID); err != nil {
			if errors.Is(err, customers.ErrNotFound) {
				return ErrCustomerUnknown
			}
			return err
		}
	case in.CustomerEmail != nil && *in.CustomerEmail != "":
		var err error
		if c, err = s.customers.Ensure(ctx, in.OrganizationID, *in.CustomerEmail); err != nil {
			return err
		}
	default:
		return nil
	}
	if c.Status == store.CustomerStatusBlocked {
		return ErrCustomerBlocked
	}
	in.CustomerID = uuid.NullUUID{UUID: c.ID, Valid: true}
	if in.CustomerEmail == nil {
		in.CustomerEmail = c.Email
	}
	return nil
}

// payableAssets decides which assets an invoice offers.
func (s *Service) payableAssets(ctx context.Context, orgID uuid.UUID, requested []int16) ([]int16, error) {
	if len(requested) > 0 {
		return requested, nil
	}
	ids, err := s.org.EnabledAssetIDs(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		return ids, nil
	}
	all, err := s.catalog.Assets(ctx)
	if err != nil {
		return nil, err
	}
	ids = make([]int16, len(all))
	for i, a := range all {
		ids[i] = a.ID
	}
	return ids, nil
}

// quoteOption prices one asset and inserts (or, when existing is given,
// re-prices) the option.
func (s *Service) quoteOption(ctx context.Context, inv store.Invoice, assetID int16, existing *store.InvoicePaymentOption) (store.InvoicePaymentOption, error) {
	asset, err := s.catalog.EnabledAsset(ctx, assetID)
	if err != nil {
		return store.InvoicePaymentOption{}, err
	}
	q, err := s.pricing.Quote(ctx, inv.OrganizationID, assetID, inv.PriceCurrency, money.FromNumeric(inv.PriceAmount))
	if err != nil {
		return store.InvoicePaymentOption{}, err
	}
	var rateID pgtype.Int8
	if q.RateID != nil {
		rateID = pgtype.Int8{Int64: *q.RateID, Valid: true}
	}
	spread := pgtype.Int4{Int32: q.SpreadBps, Valid: true}
	locked := q.LockedUntil
	if existing != nil {
		return s.q.RequoteInvoicePaymentOption(ctx, store.RequoteInvoicePaymentOptionParams{
			ID: existing.ID, AmountDue: money.ToNumeric(q.AmountDue), ExchangeRate: money.ToNumeric(q.ExchangeRate),
			SourceRate: money.ToNumeric(q.SourceRate), SpreadBps: spread, RateID: rateID, RateLockedUntil: &locked,
		})
	}
	var preferred *int16
	if setting, err := s.org.AssetSetting(ctx, inv.OrganizationID, assetID); err == nil && setting.PreferredProviderID.Valid {
		p := setting.PreferredProviderID.Int16
		preferred = &p
	}
	route, err := s.catalog.RouteDeposit(ctx, asset.NetworkID, preferred)
	if err != nil {
		return store.InvoicePaymentOption{}, err
	}
	return s.q.CreateInvoicePaymentOption(ctx, store.CreateInvoicePaymentOptionParams{
		InvoiceID: inv.ID, AssetID: assetID, ProviderID: route.ProviderID, AmountDue: money.ToNumeric(q.AmountDue),
		ExchangeRate: money.ToNumeric(q.ExchangeRate), SourceRate: money.ToNumeric(q.SourceRate),
		SpreadBps: spread, RateID: rateID, RateLockedUntil: &locked,
	})
}

// Reads ----------------------------------------------------------------------------------

func (s *Service) load(ctx context.Context, inv store.Invoice) (Invoice, error) {
	rows, err := s.q.ListInvoicePaymentOptions(ctx, inv.ID)
	if err != nil {
		return Invoice{}, err
	}
	out := Invoice{Invoice: inv, Options: make([]Option, len(rows))}
	for i, r := range rows {
		out.Options[i] = Option{
			InvoicePaymentOption: r.InvoicePaymentOption, Asset: r.Asset, NetworkCode: r.NetworkCode, NetworkName: r.NetworkName,
			Address: r.Address, AddressMemo: r.AddressMemo,
		}
	}
	return out, nil
}

// Get returns an invoice by id (for the hosted checkout page).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Invoice, error) {
	inv, err := s.q.GetInvoice(ctx, id)
	if err != nil {
		return Invoice{}, postgres.MapNotFound(err)
	}
	return s.load(ctx, inv)
}

// GetForOrganization returns an invoice scoped to its merchant.
func (s *Service) GetForOrganization(ctx context.Context, orgID, id uuid.UUID) (Invoice, error) {
	inv, err := s.q.GetInvoiceForOrganization(ctx, store.GetInvoiceForOrganizationParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return Invoice{}, postgres.MapNotFound(err)
	}
	return s.load(ctx, inv)
}

// ByExternalID looks an invoice up by the merchant's own order id.
func (s *Service) ByExternalID(ctx context.Context, orgID uuid.UUID, externalID string) (Invoice, error) {
	inv, err := s.q.GetInvoiceByExternalID(ctx, store.GetInvoiceByExternalIDParams{OrganizationID: orgID, ExternalID: &externalID})
	if err != nil {
		return Invoice{}, postgres.MapNotFound(err)
	}
	return s.load(ctx, inv)
}

// List pages a merchant's invoices, optionally by status.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, status *store.InvoiceStatus, limit, offset int32) ([]store.Invoice, error) {
	var st store.NullInvoiceStatus
	if status != nil {
		st = store.NullInvoiceStatus{InvoiceStatus: *status, Valid: true}
	}
	return s.q.ListInvoices(ctx, store.ListInvoicesParams{OrganizationID: orgID, Status: st, Limit: limit, Offset: offset})
}

// Counts returns invoices per status for filter tabs.
func (s *Service) Counts(ctx context.Context, orgID uuid.UUID) (map[store.InvoiceStatus]int64, error) {
	rows, err := s.q.CountInvoicesByStatus(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[store.InvoiceStatus]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Count
	}
	return out, nil
}

// Customer actions ------------------------------------------------------------------------

// SelectOption is the customer choosing how to pay. It re-quotes when the
// rate lock has lapsed, allocates a deposit address and locks the choice.
// Calling it again for the same asset returns the existing selection.
func (s *Service) SelectOption(ctx context.Context, invoiceID uuid.UUID, assetID int16) (Invoice, error) {
	var out Invoice
	err := s.inTx(ctx, func(s *Service) error {
		inv, err := s.q.GetInvoice(ctx, invoiceID)
		if err != nil {
			return postgres.MapNotFound(err)
		}
		if !(Invoice{Invoice: inv}).Payable(time.Now()) {
			return ErrNotPayable
		}
		if sel, err := s.q.GetSelectedInvoicePaymentOption(ctx, inv.ID); err == nil {
			if sel.AssetID != assetID {
				return ErrOtherOptionChosen
			}
			out, err = s.load(ctx, inv)
			return err
		} else if !postgres.IsNotFound(err) {
			return err
		}
		opt, err := s.q.GetInvoicePaymentOptionByAsset(ctx, store.GetInvoicePaymentOptionByAssetParams{InvoiceID: inv.ID, AssetID: assetID})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrOptionUnavailable
			}
			return err
		}
		if opt.RateLockedUntil == nil || opt.RateLockedUntil.Before(time.Now()) {
			if opt, err = s.quoteOption(ctx, inv, assetID, &opt); err != nil {
				return err
			}
		}
		asset, err := s.catalog.Asset(ctx, assetID)
		if err != nil {
			return err
		}
		addr, err := s.chain.AllocateDepositAddress(ctx, inv.OrganizationID, asset.NetworkID, &opt.ProviderID)
		if err != nil {
			return err
		}
		if _, err := s.q.SelectInvoicePaymentOption(ctx, store.SelectInvoicePaymentOptionParams{
			ID: opt.ID, AddressID: uuid.NullUUID{UUID: addr.ID, Valid: true}, Memo: addr.Memo, RateLockedUntil: opt.RateLockedUntil,
		}); err != nil {
			return fmt.Errorf("checkout: select option: %w", err)
		}
		// The invoice should not outlive the rate lock by much; extend if the
		// lock ends after the current expiry so the payer has the full window.
		if opt.RateLockedUntil != nil && opt.RateLockedUntil.After(inv.ExpiresAt) {
			if err := s.q.ExtendInvoiceExpiry(ctx, store.ExtendInvoiceExpiryParams{ID: inv.ID, ExpiresAt: *opt.RateLockedUntil}); err != nil {
				return err
			}
			inv.ExpiresAt = *opt.RateLockedUntil
		}
		out, err = s.load(ctx, inv)
		return err
	})
	return out, err
}

// Cancel voids an invoice nobody has paid.
func (s *Service) Cancel(ctx context.Context, orgID, id uuid.UUID) error {
	return s.inTx(ctx, func(s *Service) error {
		n, err := s.q.CancelInvoice(ctx, store.CancelInvoiceParams{ID: id, OrganizationID: orgID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotCancellable
		}
		inv, err := s.Get(ctx, id)
		if err != nil {
			return err
		}
		_, err = s.events.Emit(ctx, orgID, events.InvoiceCancelled, ResourceType, id, View(inv))
		return err
	})
}

// Complete is the merchant acknowledging a confirmed invoice as fulfilled.
func (s *Service) Complete(ctx context.Context, orgID, id uuid.UUID) (Invoice, error) {
	var out Invoice
	err := s.inTx(ctx, func(s *Service) error {
		inv, err := s.q.GetInvoiceForOrganization(ctx, store.GetInvoiceForOrganizationParams{ID: id, OrganizationID: orgID})
		if err != nil {
			return postgres.MapNotFound(err)
		}
		if inv.Status != store.InvoiceStatusConfirmed {
			return fmt.Errorf("checkout: invoice is %s, only confirmed invoices can be completed", inv.Status)
		}
		inv, err = s.q.SetInvoiceStatus(ctx, store.SetInvoiceStatusParams{ID: id, Status: store.InvoiceStatusCompleted})
		if err != nil {
			return err
		}
		out, err = s.load(ctx, inv)
		if err != nil {
			return err
		}
		_, err = s.events.Emit(ctx, orgID, events.InvoiceCompleted, ResourceType, id, View(out))
		return err
	})
	return out, err
}

// ExpireDue marks unpaid invoices past their expiry and announces each.
// Partially paid invoices are left alone: the money is real and the
// merchant must decide (refund or accept) through the dashboard.
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	var n int
	err := s.inTx(ctx, func(s *Service) error {
		expired, err := s.q.ExpireInvoices(ctx)
		if err != nil {
			return err
		}
		for _, inv := range expired {
			full, err := s.load(ctx, inv)
			if err != nil {
				return err
			}
			if _, err := s.events.Emit(ctx, inv.OrganizationID, events.InvoiceExpired, ResourceType, inv.ID, View(full)); err != nil {
				return err
			}
		}
		n = len(expired)
		return nil
	})
	return n, err
}

// Payment hooks (called by internal/payments inside its transaction) ------------------------

// Match is the invoice option an incoming transfer belongs to.
type Match struct {
	Option  store.InvoicePaymentOption
	Invoice store.Invoice
}

// FindMatch returns the open invoice option that owns a deposit address for
// an asset, or ErrNotFound when the deposit is unattributed.
func (s *Service) FindMatch(ctx context.Context, addressID uuid.UUID, assetID int16) (Match, error) {
	row, err := s.q.FindOpenInvoiceOptionByAddress(ctx, store.FindOpenInvoiceOptionByAddressParams{
		AddressID: uuid.NullUUID{UUID: addressID, Valid: true}, AssetID: assetID,
	})
	if err != nil {
		return Match{}, postgres.MapNotFound(err)
	}
	return Match{Option: row.InvoicePaymentOption, Invoice: row.Invoice}, nil
}

// ApplyPayment records that `amount` was seen (not yet confirmed) on an
// option and moves the invoice to partial or paid. Late payments on expired
// invoices are still recorded so the merchant sees the money; the status
// then moves from expired to paid / partial as well.
func (s *Service) ApplyPayment(ctx context.Context, optionID uuid.UUID, amount decimal.Decimal) (store.Invoice, error) {
	opt, err := s.q.AddInvoicePaymentOptionPaid(ctx, store.AddInvoicePaymentOptionPaidParams{ID: optionID, AmountPaid: money.ToNumeric(amount)})
	if err != nil {
		return store.Invoice{}, fmt.Errorf("checkout: apply payment: %w", err)
	}
	inv, err := s.q.GetInvoice(ctx, opt.InvoiceID)
	if err != nil {
		return store.Invoice{}, err
	}
	switch inv.Status {
	case store.InvoiceStatusConfirmed, store.InvoiceStatusCompleted, store.InvoiceStatusCancelled:
		return inv, nil // overpayment after settlement: recorded on the option, status unchanged
	}
	target, evType := store.InvoiceStatusPartial, events.InvoicePartial
	if money.FromNumeric(opt.AmountPaid).GreaterThanOrEqual(money.FromNumeric(opt.AmountDue)) {
		target, evType = store.InvoiceStatusPaid, events.InvoicePaid
	}
	if inv.Status == store.InvoiceStatusPaid && target == store.InvoiceStatusPartial {
		return inv, nil
	}
	inv, err = s.q.SetInvoiceStatus(ctx, store.SetInvoiceStatusParams{ID: inv.ID, Status: target})
	if err != nil {
		return store.Invoice{}, err
	}
	full, err := s.load(ctx, inv)
	if err != nil {
		return store.Invoice{}, err
	}
	_, err = s.events.Emit(ctx, inv.OrganizationID, evType, ResourceType, inv.ID, View(full))
	return inv, err
}

// ReversePayment undoes ApplyPayment for a transfer that was dropped.
func (s *Service) ReversePayment(ctx context.Context, optionID uuid.UUID, amount decimal.Decimal) (store.Invoice, error) {
	opt, err := s.q.AddInvoicePaymentOptionPaid(ctx, store.AddInvoicePaymentOptionPaidParams{ID: optionID, AmountPaid: money.ToNumeric(amount.Neg())})
	if err != nil {
		return store.Invoice{}, err
	}
	inv, err := s.q.GetInvoice(ctx, opt.InvoiceID)
	if err != nil {
		return store.Invoice{}, err
	}
	if inv.Status != store.InvoiceStatusPaid && inv.Status != store.InvoiceStatusPartial {
		return inv, nil
	}
	target := store.InvoiceStatusPartial
	paid := money.FromNumeric(opt.AmountPaid)
	if paid.IsZero() || paid.IsNegative() {
		target = store.InvoiceStatusNew
		if inv.ExpiresAt.Before(time.Now()) {
			target = store.InvoiceStatusExpired
		}
	} else if paid.GreaterThanOrEqual(money.FromNumeric(opt.AmountDue)) {
		return inv, nil
	}
	return s.q.SetInvoiceStatus(ctx, store.SetInvoiceStatusParams{ID: inv.ID, Status: target})
}

// ConfirmPayment is called once a payment on the option is credited. When
// the option is fully paid the invoice becomes confirmed.
func (s *Service) ConfirmPayment(ctx context.Context, optionID uuid.UUID) (store.Invoice, error) {
	opt, err := s.q.GetInvoicePaymentOption(ctx, optionID)
	if err != nil {
		return store.Invoice{}, postgres.MapNotFound(err)
	}
	inv, err := s.q.GetInvoice(ctx, opt.InvoiceID)
	if err != nil {
		return store.Invoice{}, err
	}
	if inv.Status == store.InvoiceStatusConfirmed || inv.Status == store.InvoiceStatusCompleted {
		return inv, nil
	}
	if money.FromNumeric(opt.AmountPaid).LessThan(money.FromNumeric(opt.AmountDue)) {
		return inv, nil
	}
	inv, err = s.q.SetInvoiceStatus(ctx, store.SetInvoiceStatusParams{ID: inv.ID, Status: store.InvoiceStatusConfirmed})
	if err != nil {
		return store.Invoice{}, err
	}
	if inv.PaymentLinkID.Valid {
		if _, err := s.q.AddPaymentLinkUse(ctx, inv.PaymentLinkID.UUID); err != nil {
			return store.Invoice{}, fmt.Errorf("checkout: count payment link use: %w", err)
		}
	}
	full, err := s.load(ctx, inv)
	if err != nil {
		return store.Invoice{}, err
	}
	_, err = s.events.Emit(ctx, inv.OrganizationID, events.InvoiceConfirmed, ResourceType, inv.ID, View(full))
	return inv, err
}

// Views ---------------------------------------------------------------------------------------

// InvoiceView is the JSON shape merchants see in the API and in webhooks.
type InvoiceView struct {
	ID            uuid.UUID       `json:"id"`
	ExternalID    *string         `json:"external_id,omitempty"`
	Status        string          `json:"status"`
	PriceCurrency string          `json:"price_currency"`
	PriceAmount   string          `json:"price_amount"`
	Description   *string         `json:"description,omitempty"`
	CustomerEmail *string         `json:"customer_email,omitempty"`
	CustomerID    *uuid.UUID      `json:"customer_id,omitempty"`
	PaymentLinkID *uuid.UUID      `json:"payment_link_id,omitempty"`
	ReturnURL     *string         `json:"return_url,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
	ExpiresAt     time.Time       `json:"expires_at"`
	PaidAt        *time.Time      `json:"paid_at,omitempty"`
	ConfirmedAt   *time.Time      `json:"confirmed_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	Options       []OptionView    `json:"payment_options"`
}

// OptionView is one payment option in the API shape.
type OptionView struct {
	Asset        string     `json:"asset"`
	Symbol       string     `json:"symbol"`
	Network      string     `json:"network"`
	AmountDue    string     `json:"amount_due"`
	AmountPaid   string     `json:"amount_paid"`
	ExchangeRate *string    `json:"exchange_rate,omitempty"`
	RateLocked   *time.Time `json:"rate_locked_until,omitempty"`
	Selected     bool       `json:"selected"`
	Address      *string    `json:"address,omitempty"`
	Memo         *string    `json:"memo,omitempty"`
}

// View converts an invoice to its API shape.
func View(i Invoice) InvoiceView {
	v := InvoiceView{
		ID: i.ID, ExternalID: i.ExternalID, Status: string(i.Status), PriceCurrency: i.PriceCurrency,
		PriceAmount: money.FromNumeric(i.PriceAmount).String(), Description: i.Description, CustomerEmail: i.CustomerEmail,
		ReturnURL: i.ReturnUrl, Metadata: json.RawMessage(i.Metadata), ExpiresAt: i.ExpiresAt, PaidAt: i.PaidAt,
		ConfirmedAt: i.ConfirmedAt, CreatedAt: i.CreatedAt, Options: make([]OptionView, len(i.Options)),
	}
	if len(v.Metadata) == 0 {
		v.Metadata = json.RawMessage("{}")
	}
	if i.CustomerID.Valid {
		v.CustomerID = &i.CustomerID.UUID
	}
	if i.PaymentLinkID.Valid {
		v.PaymentLinkID = &i.PaymentLinkID.UUID
	}
	for k, o := range i.Options {
		ov := OptionView{
			Asset: o.Asset.Code, Symbol: o.Asset.Symbol, Network: o.NetworkCode,
			AmountDue:  money.Format(money.FromNumeric(o.AmountDue), o.Asset.Decimals),
			AmountPaid: money.Format(money.FromNumeric(o.AmountPaid), o.Asset.Decimals),
			RateLocked: o.RateLockedUntil, Selected: o.IsSelected, Address: o.Address, Memo: o.AddressMemo,
		}
		if o.ExchangeRate.Valid {
			r := money.FromNumeric(o.ExchangeRate).String()
			ov.ExchangeRate = &r
		}
		v.Options[k] = ov
	}
	return v
}
