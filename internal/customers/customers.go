// Package customers owns the merchant's payer records. A customer is one row
// per (organization, email) or per merchant-supplied external id; invoices
// point at it. checkout resolves the customer when an invoice is created and
// refuses blocked ones. Activity figures (invoices, paid, volume) are summed
// from invoices at read time, so this package stores identity only.
package customers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/store"
)

var (
	ErrNotFound   = postgres.ErrNotFound
	ErrNoIdentity = errors.New("customers: an email or an external id is required")
	ErrBadEmail   = errors.New("customers: invalid email")
	ErrBadCountry = errors.New("customers: country must be an upper-case ISO 3166-1 alpha-2 code")
	ErrDuplicate  = errors.New("customers: another customer already has this email or external id")
	ErrBlocked    = errors.New("customers: customer is blocked")
)

var (
	emailRe   = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
)

// Service is the customers domain.
type Service struct {
	q    *store.Queries
	pool *pgxpool.Pool
}

// New builds the service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{q: store.New(pool), pool: pool}
}

// WithTx binds the service to one transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service { return &Service{q: s.q.WithTx(tx)} }

// Input is the merchant-editable customer profile.
type Input struct {
	ExternalID  *string
	Email       *string
	Name        *string
	CountryCode *string
	Metadata    map[string]any // nil keeps {} on create and the current value on update
}

func (in *Input) normalize() (meta []byte, country pgtype.Text, err error) {
	in.ExternalID = trimOrNil(in.ExternalID)
	in.Name = trimOrNil(in.Name)
	if in.Email != nil {
		e := strings.ToLower(strings.TrimSpace(*in.Email))
		if e == "" {
			in.Email = nil
		} else {
			if !emailRe.MatchString(e) {
				return nil, country, ErrBadEmail
			}
			in.Email = &e
		}
	}
	if in.CountryCode != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.CountryCode))
		if c != "" {
			if !countryRe.MatchString(c) {
				return nil, country, ErrBadCountry
			}
			country = pgtype.Text{String: c, Valid: true}
		}
	}
	if in.Metadata != nil {
		if meta, err = json.Marshal(in.Metadata); err != nil {
			return nil, country, fmt.Errorf("customers: metadata: %w", err)
		}
	}
	return meta, country, nil
}

func trimOrNil(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

// Create adds a customer. At least one of Email / ExternalID is required.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in Input) (store.Customer, error) {
	meta, country, err := in.normalize()
	if err != nil {
		return store.Customer{}, err
	}
	if in.Email == nil && in.ExternalID == nil {
		return store.Customer{}, ErrNoIdentity
	}
	if meta == nil {
		meta = []byte("{}")
	}
	c, err := s.q.CreateCustomer(ctx, store.CreateCustomerParams{
		OrganizationID: orgID, ExternalID: in.ExternalID, Email: in.Email, Name: in.Name, CountryCode: country, Metadata: meta,
	})
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return store.Customer{}, ErrDuplicate
		}
		return store.Customer{}, fmt.Errorf("customers: create: %w", err)
	}
	return c, nil
}

// Ensure returns the customer with this email, creating it when unknown.
// checkout calls it for every invoice that carries a customer_email.
func (s *Service) Ensure(ctx context.Context, orgID uuid.UUID, email string) (store.Customer, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	if !emailRe.MatchString(e) {
		return store.Customer{}, ErrBadEmail
	}
	c, err := s.q.UpsertCustomerByEmail(ctx, store.UpsertCustomerByEmailParams{OrganizationID: orgID, Email: &e})
	if err != nil {
		return store.Customer{}, fmt.Errorf("customers: ensure: %w", err)
	}
	return c, nil
}

// Get returns one customer of the organization.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (store.Customer, error) {
	c, err := s.q.GetCustomer(ctx, store.GetCustomerParams{ID: id, OrganizationID: orgID})
	return c, postgres.MapNotFound(err)
}

// ByEmail looks a customer up by email.
func (s *Service) ByEmail(ctx context.Context, orgID uuid.UUID, email string) (store.Customer, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	c, err := s.q.GetCustomerByEmail(ctx, store.GetCustomerByEmailParams{OrganizationID: orgID, Email: &e})
	return c, postgres.MapNotFound(err)
}

// ByExternalID looks a customer up by the merchant's own id.
func (s *Service) ByExternalID(ctx context.Context, orgID uuid.UUID, externalID string) (store.Customer, error) {
	c, err := s.q.GetCustomerByExternalID(ctx, store.GetCustomerByExternalIDParams{OrganizationID: orgID, ExternalID: &externalID})
	return c, postgres.MapNotFound(err)
}

// Update replaces the profile fields. Fields left nil are cleared, except
// Metadata, which keeps its current value when nil.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in Input) (store.Customer, error) {
	meta, country, err := in.normalize()
	if err != nil {
		return store.Customer{}, err
	}
	if in.Email == nil && in.ExternalID == nil {
		return store.Customer{}, ErrNoIdentity
	}
	if meta == nil {
		cur, err := s.Get(ctx, orgID, id)
		if err != nil {
			return store.Customer{}, err
		}
		meta = cur.Metadata
	}
	c, err := s.q.UpdateCustomer(ctx, store.UpdateCustomerParams{
		ID: id, OrganizationID: orgID, ExternalID: in.ExternalID, Email: in.Email, Name: in.Name, CountryCode: country, Metadata: meta,
	})
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return store.Customer{}, ErrDuplicate
		}
		return store.Customer{}, postgres.MapNotFound(err)
	}
	return c, nil
}

// Block stops the customer from opening new invoices. Existing invoices are
// untouched: money already on the way is still recognised.
func (s *Service) Block(ctx context.Context, orgID, id uuid.UUID, reason *string) (store.Customer, error) {
	c, err := s.q.SetCustomerStatus(ctx, store.SetCustomerStatusParams{ID: id, OrganizationID: orgID, Status: store.CustomerStatusBlocked, Reason: trimOrNil(reason)})
	return c, postgres.MapNotFound(err)
}

// Unblock lifts a block.
func (s *Service) Unblock(ctx context.Context, orgID, id uuid.UUID) (store.Customer, error) {
	c, err := s.q.SetCustomerStatus(ctx, store.SetCustomerStatusParams{ID: id, OrganizationID: orgID, Status: store.CustomerStatusActive})
	return c, postgres.MapNotFound(err)
}

// Summary is a customer with activity summed from their invoices.
type Summary struct {
	store.Customer
	Invoices       int64
	Paid           int64
	Volume         decimal.Decimal // paid invoices priced in the requested currency
	LastActivityAt time.Time       // newest invoice, or the customer's creation
}

// List returns customers newest-activity first. currency selects which
// invoices count towards Volume (normally the merchant's default currency).
func (s *Service) List(ctx context.Context, orgID uuid.UUID, status *store.CustomerStatus, currency string, limit, offset int32) ([]Summary, error) {
	rows, err := s.q.ListCustomers(ctx, store.ListCustomersParams{
		OrganizationID: orgID, Status: nullStatus(status), Currency: currency, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Summary, len(rows))
	for i, r := range rows {
		out[i] = Summary{Customer: r.Customer, Invoices: r.Invoices, Paid: r.Paid, Volume: money.FromNumeric(r.Volume), LastActivityAt: r.LastActivityAt}
	}
	return out, nil
}

// Count counts customers, optionally in one status.
func (s *Service) Count(ctx context.Context, orgID uuid.UUID, status *store.CustomerStatus) (int64, error) {
	return s.q.CountCustomers(ctx, store.CountCustomersParams{OrganizationID: orgID, Status: nullStatus(status)})
}

// CountSince counts customers first seen since t.
func (s *Service) CountSince(ctx context.Context, orgID uuid.UUID, since time.Time) (int64, error) {
	return s.q.CountCustomersSince(ctx, store.CountCustomersSinceParams{OrganizationID: orgID, CreatedAt: since})
}

// Stats are the dashboard headline figures.
type Stats struct {
	Paying       int64           // customers with at least one paid invoice
	Repeat       int64           // customers with two or more
	AvgVolume    decimal.Decimal // mean lifetime value of paying customers, in currency
	MedianVolume decimal.Decimal
}

// RepeatRate is Repeat / Paying as a percentage.
func (s Stats) RepeatRate() float64 {
	if s.Paying == 0 {
		return 0
	}
	return float64(s.Repeat) / float64(s.Paying) * 100
}

// Stats computes repeat rate and lifetime value in the given currency.
func (s *Service) Stats(ctx context.Context, orgID uuid.UUID, currency string) (Stats, error) {
	r, err := s.q.CustomerStats(ctx, store.CustomerStatsParams{OrganizationID: orgID, PriceCurrency: currency})
	if err != nil {
		return Stats{}, err
	}
	return Stats{Paying: r.Paying, Repeat: r.Repeat, AvgVolume: money.FromNumeric(r.AvgVolume), MedianVolume: money.FromNumeric(r.MedianVolume)}, nil
}

// Invoices lists one customer's invoices, newest first.
func (s *Service) Invoices(ctx context.Context, orgID, id uuid.UUID, limit, offset int32) ([]store.Invoice, error) {
	return s.q.ListCustomerInvoices(ctx, store.ListCustomerInvoicesParams{CustomerID: uuid.NullUUID{UUID: id, Valid: true}, OrganizationID: orgID, Limit: limit, Offset: offset})
}

func nullStatus(st *store.CustomerStatus) store.NullCustomerStatus {
	if st == nil {
		return store.NullCustomerStatus{}
	}
	return store.NullCustomerStatus{CustomerStatus: *st, Valid: true}
}
