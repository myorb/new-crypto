package checkout

// Payment links: a reusable checkout template. Opening a link (OpenLink)
// creates an ordinary invoice priced from the link, so everything downstream
// (options, deposits, events, the ledger) is unchanged. The link only counts
// uses when one of its invoices is confirmed.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"templ-app/internal/events"
	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/store"
)

var (
	ErrLinkNotFound   = errors.New("checkout: payment link not found")
	ErrLinkClosed     = errors.New("checkout: payment link is no longer active")
	ErrLinkName       = errors.New("checkout: payment link needs a name")
	ErrLinkPrice      = errors.New("checkout: a payment link has either a fixed price or an amount range, not both")
	ErrLinkAmount     = errors.New("checkout: amount is outside the range this link accepts")
	ErrLinkNeedAmount = errors.New("checkout: this link asks the payer for an amount")
	ErrLinkSlugTaken  = errors.New("checkout: payment link slug is already in use")
	ErrLinkBadSlug    = errors.New("checkout: slug must be 6-64 characters of letters, digits, '-' or '_'")
)

// LinkResourceType is what events call a payment link.
const LinkResourceType = "payment_link"

var slugRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)

// Link is a payment link with the assets it accepts.
type Link struct {
	store.PaymentLink
	Assets []store.Asset // empty = every asset the merchant has enabled
}

// Open reports whether the link still accepts payers.
func (l Link) Open(now time.Time) bool {
	if l.Status != store.PaymentLinkStatusActive {
		return false
	}
	if l.ExpiresAt != nil && !now.Before(*l.ExpiresAt) {
		return false
	}
	if l.MaxUses.Valid && l.UsesCount >= l.MaxUses.Int32 {
		return false
	}
	return true
}

// LinkInput is a new or edited payment link.
type LinkInput struct {
	OrganizationID  uuid.UUID
	Slug            string // empty = generated
	Name            string
	Description     *string
	PriceCurrency   string           // "USD", "EUR" or an asset code; ignored on update
	PriceAmount     *decimal.Decimal // nil = the payer chooses
	MinAmount       *decimal.Decimal // bounds for payer-chosen amounts
	MaxAmount       *decimal.Decimal
	MaxUses         *int32 // nil = unlimited, 1 = single use
	InvoiceTTL      time.Duration
	CollectEmail    bool
	CustomerID      uuid.NullUUID
	CallbackURL     *string
	ReturnURL       *string
	Metadata        map[string]any
	ExpiresAt       *time.Time
	AssetIDs        []int16 // empty = the merchant's enabled assets
	CreatedBy       uuid.NullUUID
	CreatedByAPIKey uuid.NullUUID
}

func (in *LinkInput) validate(s *Service) ([]byte, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, ErrLinkName
	}
	if in.PriceAmount != nil {
		if !in.PriceAmount.IsPositive() {
			return nil, ErrBadPrice
		}
		if in.MinAmount != nil || in.MaxAmount != nil {
			return nil, ErrLinkPrice
		}
	}
	for _, b := range []*decimal.Decimal{in.MinAmount, in.MaxAmount} {
		if b != nil && !b.IsPositive() {
			return nil, ErrBadPrice
		}
	}
	if in.MinAmount != nil && in.MaxAmount != nil && in.MinAmount.GreaterThan(*in.MaxAmount) {
		return nil, ErrLinkPrice
	}
	if in.CallbackURL != nil && *in.CallbackURL != "" {
		if err := events.ValidateEndpointURL(*in.CallbackURL, s.opts.AllowPrivate); err != nil {
			return nil, ErrBadCallbackURL
		}
	}
	if in.ReturnURL != nil && *in.ReturnURL != "" {
		u, err := url.Parse(*in.ReturnURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, ErrBadReturnURL
		}
	}
	meta := []byte("{}")
	if in.Metadata != nil {
		var err error
		if meta, err = json.Marshal(in.Metadata); err != nil {
			return nil, fmt.Errorf("checkout: metadata: %w", err)
		}
	}
	return meta, nil
}

// ttlSeconds clamps the per-invoice lifetime to the service bounds.
func (s *Service) ttlSeconds(d time.Duration) int32 {
	if d <= 0 {
		d = s.opts.DefaultTTL
	}
	if d > s.opts.MaxTTL {
		d = s.opts.MaxTTL
	}
	if d < time.Minute {
		d = time.Minute
	}
	return int32(d / time.Second)
}

// NewSlug returns a random URL-safe token for a link.
func NewSlug() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// CreateLink stores a payment link and its accepted assets.
func (s *Service) CreateLink(ctx context.Context, in LinkInput) (Link, error) {
	meta, err := in.validate(s)
	if err != nil {
		return Link{}, err
	}
	in.PriceCurrency = strings.ToUpper(strings.TrimSpace(in.PriceCurrency))
	if !currencyRe.MatchString(in.PriceCurrency) {
		return Link{}, ErrBadCurrency
	}
	if in.Slug == "" {
		in.Slug = NewSlug()
	} else if !slugRe.MatchString(in.Slug) {
		return Link{}, ErrLinkBadSlug
	}

	var out Link
	err = s.inTx(ctx, func(s *Service) error {
		if in.CustomerID.Valid {
			if _, err := s.customers.Get(ctx, in.OrganizationID, in.CustomerID.UUID); err != nil {
				return ErrCustomerUnknown
			}
		}
		l, err := s.q.CreatePaymentLink(ctx, store.CreatePaymentLinkParams{
			OrganizationID: in.OrganizationID, Slug: in.Slug, Name: in.Name, Description: in.Description,
			PriceCurrency: in.PriceCurrency, PriceAmount: money.NullNumeric(in.PriceAmount),
			MinAmount: money.NullNumeric(in.MinAmount), MaxAmount: money.NullNumeric(in.MaxAmount),
			MaxUses: nullInt32(in.MaxUses), InvoiceTtlSeconds: s.ttlSeconds(in.InvoiceTTL),
			CollectEmail: in.CollectEmail, CustomerID: in.CustomerID, CallbackUrl: in.CallbackURL,
			ReturnUrl: in.ReturnURL, Metadata: meta, ExpiresAt: in.ExpiresAt,
			CreatedBy: in.CreatedBy, CreatedByApiKey: in.CreatedByAPIKey,
		})
		if err != nil {
			if postgres.IsUniqueViolation(err) {
				return ErrLinkSlugTaken
			}
			return fmt.Errorf("checkout: create payment link: %w", err)
		}
		if err := s.setLinkAssets(ctx, l.ID, in.AssetIDs); err != nil {
			return err
		}
		out, err = s.loadLink(ctx, l)
		if err != nil {
			return err
		}
		_, err = s.events.Emit(ctx, l.OrganizationID, events.PaymentLinkCreated, LinkResourceType, l.ID, ViewLink(out, ""))
		return err
	})
	return out, err
}

// UpdateLink edits an existing link. The currency, slug and customer are fixed
// once the link exists: change those by creating a new link.
func (s *Service) UpdateLink(ctx context.Context, id uuid.UUID, in LinkInput) (Link, error) {
	meta, err := in.validate(s)
	if err != nil {
		return Link{}, err
	}
	var out Link
	err = s.inTx(ctx, func(s *Service) error {
		l, err := s.q.UpdatePaymentLink(ctx, store.UpdatePaymentLinkParams{
			ID: id, OrganizationID: in.OrganizationID, Name: in.Name, Description: in.Description,
			PriceAmount: money.NullNumeric(in.PriceAmount), MinAmount: money.NullNumeric(in.MinAmount),
			MaxAmount: money.NullNumeric(in.MaxAmount), MaxUses: nullInt32(in.MaxUses),
			InvoiceTtlSeconds: s.ttlSeconds(in.InvoiceTTL), CollectEmail: in.CollectEmail,
			CallbackUrl: in.CallbackURL, ReturnUrl: in.ReturnURL, Metadata: meta, ExpiresAt: in.ExpiresAt,
		})
		if err != nil {
			if postgres.IsNotFound(err) {
				return ErrLinkNotFound
			}
			return err
		}
		if err := s.setLinkAssets(ctx, l.ID, in.AssetIDs); err != nil {
			return err
		}
		out, err = s.loadLink(ctx, l)
		if err != nil {
			return err
		}
		_, err = s.events.Emit(ctx, l.OrganizationID, events.PaymentLinkUpdated, LinkResourceType, l.ID, ViewLink(out, ""))
		return err
	})
	return out, err
}

// setLinkAssets replaces the accepted asset list. An empty list means "every
// asset the merchant has enabled at payment time".
func (s *Service) setLinkAssets(ctx context.Context, linkID uuid.UUID, assetIDs []int16) error {
	if err := s.q.ClearPaymentLinkAssets(ctx, linkID); err != nil {
		return err
	}
	for _, id := range assetIDs {
		if _, err := s.catalog.Asset(ctx, id); err != nil {
			return err
		}
		if err := s.q.AddPaymentLinkAsset(ctx, store.AddPaymentLinkAssetParams{PaymentLinkID: linkID, AssetID: id}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) loadLink(ctx context.Context, l store.PaymentLink) (Link, error) {
	assets, err := s.q.ListPaymentLinkAssets(ctx, l.ID)
	if err != nil {
		return Link{}, err
	}
	return Link{PaymentLink: l, Assets: assets}, nil
}

// GetLink returns one link of the organization.
func (s *Service) GetLink(ctx context.Context, orgID, id uuid.UUID) (Link, error) {
	l, err := s.q.GetPaymentLink(ctx, store.GetPaymentLinkParams{ID: id, OrganizationID: orgID})
	if err != nil {
		if postgres.IsNotFound(err) {
			return Link{}, ErrLinkNotFound
		}
		return Link{}, err
	}
	return s.loadLink(ctx, l)
}

// LinkBySlug is the public lookup used by the hosted page.
func (s *Service) LinkBySlug(ctx context.Context, slug string) (Link, error) {
	l, err := s.q.GetPaymentLinkBySlug(ctx, slug)
	if err != nil {
		if postgres.IsNotFound(err) {
			return Link{}, ErrLinkNotFound
		}
		return Link{}, err
	}
	return s.loadLink(ctx, l)
}

// LinkSummary is a link with the invoices it produced.
type LinkSummary struct {
	store.PaymentLink
	Invoices int64
	Paid     int64
	Revenue  decimal.Decimal
}

// Conversion is paid invoices per view, as a percentage.
func (l LinkSummary) Conversion() float64 {
	if l.ViewCount == 0 {
		return 0
	}
	return float64(l.Paid) / float64(l.ViewCount) * 100
}

// Links lists the merchant's links newest first, with their totals.
func (s *Service) Links(ctx context.Context, orgID uuid.UUID, status *store.PaymentLinkStatus, limit, offset int32) ([]LinkSummary, error) {
	rows, err := s.q.ListPaymentLinks(ctx, store.ListPaymentLinksParams{
		OrganizationID: orgID, Status: nullLinkStatus(status), Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]LinkSummary, len(rows))
	for i, r := range rows {
		out[i] = LinkSummary{PaymentLink: r.PaymentLink, Invoices: r.Invoices, Paid: r.Paid, Revenue: money.FromNumeric(r.Revenue)}
	}
	return out, nil
}

// LinkAssets maps link id to accepted assets for a page of links.
func (s *Service) LinkAssets(ctx context.Context, linkIDs []uuid.UUID) (map[uuid.UUID][]store.ListPaymentLinkAssetCodesRow, error) {
	rows, err := s.q.ListPaymentLinkAssetCodes(ctx, linkIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]store.ListPaymentLinkAssetCodesRow, len(linkIDs))
	for _, r := range rows {
		out[r.PaymentLinkID] = append(out[r.PaymentLinkID], r)
	}
	return out, nil
}

// CountLinks counts links, optionally in one status.
func (s *Service) CountLinks(ctx context.Context, orgID uuid.UUID, status *store.PaymentLinkStatus) (int64, error) {
	return s.q.CountPaymentLinks(ctx, store.CountPaymentLinksParams{OrganizationID: orgID, Status: nullLinkStatus(status)})
}

// LinkStats are the dashboard headline figures for links.
type LinkStats struct {
	Active   int64 // links currently open
	Expiring int64 // of those, expiring before `before`
	Views    int64 // hosted page opens, all time
	Paid     int64 // invoices paid through a link since `since`
	UsedLink int64 // distinct links that were paid since `since`
	Revenue  decimal.Decimal
}

// Conversion is paid invoices per view, as a percentage.
func (s LinkStats) Conversion() float64 {
	if s.Views == 0 {
		return 0
	}
	return float64(s.Paid) / float64(s.Views) * 100
}

// LinkStats computes the links page headline. currency selects which invoices
// count towards Revenue; before bounds "expiring soon".
func (s *Service) LinkStats(ctx context.Context, orgID uuid.UUID, currency string, since, before time.Time) (LinkStats, error) {
	a, err := s.q.PaymentLinkStats(ctx, store.PaymentLinkStatsParams{OrganizationID: orgID, Before: &before})
	if err != nil {
		return LinkStats{}, err
	}
	b, err := s.q.SumPaymentLinkInvoices(ctx, store.SumPaymentLinkInvoicesParams{OrganizationID: orgID, Currency: currency, Since: since})
	if err != nil {
		return LinkStats{}, err
	}
	return LinkStats{Active: a.Active, Expiring: a.Expiring, Views: a.Views, Paid: b.Paid, UsedLink: b.Links, Revenue: money.FromNumeric(b.Revenue)}, nil
}

// SetLinkStatus pauses, resumes or archives a link.
func (s *Service) SetLinkStatus(ctx context.Context, orgID, id uuid.UUID, status store.PaymentLinkStatus) (Link, error) {
	l, err := s.q.SetPaymentLinkStatus(ctx, store.SetPaymentLinkStatusParams{ID: id, OrganizationID: orgID, Status: status})
	if err != nil {
		if postgres.IsNotFound(err) {
			return Link{}, ErrLinkNotFound
		}
		return Link{}, err
	}
	return s.loadLink(ctx, l)
}

// RecordLinkView counts one open of the hosted page.
func (s *Service) RecordLinkView(ctx context.Context, id uuid.UUID) error {
	return s.q.RecordPaymentLinkView(ctx, id)
}

// ExpireDueLinks closes links past their expiry. Called by the worker next to
// ExpireDue.
func (s *Service) ExpireDueLinks(ctx context.Context) (int, error) {
	rows, err := s.q.ExpirePaymentLinks(ctx)
	return len(rows), err
}

// OpenLink creates an invoice from a link. amount is required when the link
// lets the payer choose, and ignored otherwise. email is stored (and resolved
// to a customer) when the link collects one.
func (s *Service) OpenLink(ctx context.Context, slug string, amount *decimal.Decimal, email *string) (Invoice, error) {
	var out Invoice
	err := s.inTx(ctx, func(s *Service) error {
		l, err := s.LinkBySlug(ctx, slug)
		if err != nil {
			return err
		}
		if !l.Open(time.Now()) {
			return ErrLinkClosed
		}
		price, err := linkPrice(l, amount)
		if err != nil {
			return err
		}
		assetIDs := make([]int16, len(l.Assets))
		for i, a := range l.Assets {
			assetIDs[i] = a.ID
		}
		in := CreateInput{
			OrganizationID: l.OrganizationID, PriceCurrency: l.PriceCurrency, PriceAmount: price,
			Description: l.Description, CustomerID: l.CustomerID,
			PaymentLinkID: uuid.NullUUID{UUID: l.ID, Valid: true},
			CallbackURL:   l.CallbackUrl, ReturnURL: l.ReturnUrl,
			ExpiresIn: time.Duration(l.InvoiceTtlSeconds) * time.Second,
			AssetIDs:  assetIDs,
		}
		if l.CollectEmail && email != nil && strings.TrimSpace(*email) != "" {
			in.CustomerEmail = email
		}
		if len(l.Metadata) > 0 {
			if err := json.Unmarshal(l.Metadata, &in.Metadata); err != nil {
				return fmt.Errorf("checkout: payment link metadata: %w", err)
			}
		}
		out, err = s.Create(ctx, in)
		return err
	})
	return out, err
}

// linkPrice resolves what this opening of the link costs.
func linkPrice(l Link, amount *decimal.Decimal) (decimal.Decimal, error) {
	if l.PriceAmount.Valid {
		return money.FromNumeric(l.PriceAmount), nil
	}
	if amount == nil {
		return decimal.Zero, ErrLinkNeedAmount
	}
	if !amount.IsPositive() {
		return decimal.Zero, ErrBadPrice
	}
	if l.MinAmount.Valid && amount.LessThan(money.FromNumeric(l.MinAmount)) {
		return decimal.Zero, ErrLinkAmount
	}
	if l.MaxAmount.Valid && amount.GreaterThan(money.FromNumeric(l.MaxAmount)) {
		return decimal.Zero, ErrLinkAmount
	}
	return *amount, nil
}

// LinkView is the JSON shape merchants see for a link. host, when given,
// renders the public URL.
type LinkView struct {
	ID            uuid.UUID       `json:"id"`
	Slug          string          `json:"slug"`
	URL           string          `json:"url,omitempty"`
	Name          string          `json:"name"`
	Description   *string         `json:"description,omitempty"`
	Status        string          `json:"status"`
	PriceCurrency string          `json:"price_currency"`
	PriceAmount   *string         `json:"price_amount,omitempty"`
	MinAmount     *string         `json:"min_amount,omitempty"`
	MaxAmount     *string         `json:"max_amount,omitempty"`
	Assets        []string        `json:"assets"`
	MaxUses       *int32          `json:"max_uses,omitempty"`
	UsesCount     int32           `json:"uses_count"`
	ViewCount     int64           `json:"view_count"`
	CollectEmail  bool            `json:"collect_email"`
	ReturnURL     *string         `json:"return_url,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ViewLink converts a link to its API shape. host is the hosted-checkout host
// ("pay.example.com"); pass "" to omit the URL.
func ViewLink(l Link, host string) LinkView {
	v := LinkView{
		ID: l.ID, Slug: l.Slug, Name: l.Name, Description: l.Description, Status: string(l.Status),
		PriceCurrency: l.PriceCurrency, UsesCount: l.UsesCount, ViewCount: l.ViewCount,
		CollectEmail: l.CollectEmail, ReturnURL: l.ReturnUrl, Metadata: json.RawMessage(l.Metadata),
		ExpiresAt: l.ExpiresAt, CreatedAt: l.CreatedAt, Assets: make([]string, len(l.Assets)),
	}
	if host != "" {
		v.URL = "https://" + host + "/l/" + l.Slug
	}
	if len(v.Metadata) == 0 {
		v.Metadata = json.RawMessage("{}")
	}
	for i, a := range l.Assets {
		v.Assets[i] = a.Code
	}
	if l.PriceAmount.Valid {
		s := money.FromNumeric(l.PriceAmount).String()
		v.PriceAmount = &s
	}
	if l.MinAmount.Valid {
		s := money.FromNumeric(l.MinAmount).String()
		v.MinAmount = &s
	}
	if l.MaxAmount.Valid {
		s := money.FromNumeric(l.MaxAmount).String()
		v.MaxAmount = &s
	}
	if l.MaxUses.Valid {
		m := l.MaxUses.Int32
		v.MaxUses = &m
	}
	return v
}

func nullInt32(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}

func nullLinkStatus(st *store.PaymentLinkStatus) store.NullPaymentLinkStatus {
	if st == nil {
		return store.NullPaymentLinkStatus{}
	}
	return store.NullPaymentLinkStatus{PaymentLinkStatus: *st, Valid: true}
}
