// Package events is the outbound side of the platform: every domain records
// what happened here (Emit) and this package fans it out to the merchant's
// webhook endpoints with signed, retried deliveries. It also logs inbound
// provider callbacks. It knows nothing about invoices or payments.
package events

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"templ-app/internal/postgres"
	"templ-app/internal/secrets"
	"templ-app/internal/store"
)

var (
	ErrBadURL            = errors.New("events: endpoint URL must be https and public")
	ErrEncryptionMissing = errors.New("events: encryption key required for webhook secrets")
	ErrNotFound          = postgres.ErrNotFound
)

// Well-known event types. Payload shapes are documented next to the emitter.
const (
	InvoiceCreated      = "invoice.created"
	InvoicePartial      = "invoice.partially_paid"
	InvoicePaid         = "invoice.paid"
	InvoiceConfirmed    = "invoice.confirmed"
	InvoiceCompleted    = "invoice.completed"
	InvoiceExpired      = "invoice.expired"
	InvoiceCancelled    = "invoice.cancelled"
	PaymentLinkCreated  = "payment_link.created"
	PaymentLinkUpdated  = "payment_link.updated"
	CustomerCreated     = "customer.created"
	CustomerBlocked     = "customer.blocked"
	PaymentDetected     = "payment.detected"
	PaymentConfirmed    = "payment.confirmed"
	PaymentCredited     = "payment.credited"
	PaymentReverted     = "payment.reverted"
	WithdrawalRequested = "withdrawal.requested"
	WithdrawalApproved  = "withdrawal.approved"
	WithdrawalRejected  = "withdrawal.rejected"
	WithdrawalCancelled = "withdrawal.cancelled"
	WithdrawalBroadcast = "withdrawal.broadcast"
	WithdrawalCompleted = "withdrawal.completed"
	WithdrawalFailed    = "withdrawal.failed"
)

// Options tune delivery.
type Options struct {
	Timeout     time.Duration // per HTTP attempt; default 10s
	BaseBackoff time.Duration // first retry delay; default 30s
	MaxBackoff  time.Duration // cap; default 6h
	UserAgent   string
	// AllowPrivate disables the SSRF guard. Only for local development.
	AllowPrivate bool
	// TLSClientConfig overrides certificate verification; tests point it at
	// an httptest server's certificate. Leave nil in production.
	TLSClientConfig *tls.Config
}

// Service is the events domain.
type Service struct {
	q      *store.Queries
	pool   *pgxpool.Pool
	keys   *secrets.Keyring
	client *http.Client
	opts   Options
}

// New builds the service. keys may be nil, in which case endpoints cannot be
// created (their secrets need encrypting) but Emit still records events.
func New(pool *pgxpool.Pool, keys *secrets.Keyring, opts Options) *Service {
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = 30 * time.Second
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 6 * time.Hour
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "templ-app-webhooks/1.0"
	}
	return &Service{q: store.New(pool), pool: pool, keys: keys, client: newClient(opts), opts: opts}
}

// WithTx binds the service to a transaction so Emit commits with the change
// it announces.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{q: s.q.WithTx(tx), keys: s.keys, client: s.client, opts: s.opts}
}

// Emit ------------------------------------------------------------------------------

// Emit records an event and queues a delivery to every active endpoint that
// subscribes to its type. payload is marshalled to JSON.
func (s *Service) Emit(ctx context.Context, orgID uuid.UUID, eventType, resourceType string, resourceID uuid.UUID, payload any) (store.Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return store.Event{}, fmt.Errorf("events: marshal payload: %w", err)
	}
	ev, err := s.q.CreateEvent(ctx, store.CreateEventParams{
		OrganizationID: orgID, Type: eventType, ResourceType: resourceType, ResourceID: resourceID, Payload: body,
	})
	if err != nil {
		return store.Event{}, fmt.Errorf("events: create event: %w", err)
	}
	endpoints, err := s.q.ListActiveEndpointsForEvent(ctx, store.ListActiveEndpointsForEventParams{OrganizationID: orgID, EventType: eventType})
	if err != nil {
		return store.Event{}, fmt.Errorf("events: list endpoints: %w", err)
	}
	for _, ep := range endpoints {
		if _, err := s.q.CreateWebhookDelivery(ctx, store.CreateWebhookDeliveryParams{EndpointID: ep.ID, EventID: ev.ID}); err != nil && !postgres.IsNotFound(err) {
			return store.Event{}, fmt.Errorf("events: queue delivery: %w", err)
		}
	}
	return ev, nil
}

// Events lists an organization's events, newest first.
func (s *Service) Events(ctx context.Context, orgID uuid.UUID, eventType *string, limit, offset int32) ([]store.Event, error) {
	return s.q.ListEvents(ctx, store.ListEventsParams{OrganizationID: orgID, Type: eventType, Limit: limit, Offset: offset})
}

// ForResource lists the events of one resource.
func (s *Service) ForResource(ctx context.Context, resourceType string, resourceID uuid.UUID) ([]store.Event, error) {
	return s.q.ListEventsForResource(ctx, store.ListEventsForResourceParams{ResourceType: resourceType, ResourceID: resourceID})
}

// Endpoints ------------------------------------------------------------------------

// EndpointInput describes a webhook endpoint.
type EndpointInput struct {
	URL         string
	Description *string
	EventTypes  []string // empty = all ("*")
	CreatedBy   uuid.NullUUID
}

// CreateEndpoint registers an endpoint and returns its signing secret once.
func (s *Service) CreateEndpoint(ctx context.Context, orgID uuid.UUID, in EndpointInput) (string, store.WebhookEndpoint, error) {
	if s.keys == nil {
		return "", store.WebhookEndpoint{}, ErrEncryptionMissing
	}
	if err := ValidateEndpointURL(in.URL, s.opts.AllowPrivate); err != nil {
		return "", store.WebhookEndpoint{}, err
	}
	secret, err := secrets.Token("whsec_")
	if err != nil {
		return "", store.WebhookEndpoint{}, err
	}
	enc, err := s.keys.EncryptString(secret)
	if err != nil {
		return "", store.WebhookEndpoint{}, err
	}
	types := in.EventTypes
	if len(types) == 0 {
		types = []string{"*"}
	}
	ep, err := s.q.CreateWebhookEndpoint(ctx, store.CreateWebhookEndpointParams{
		OrganizationID: orgID, Url: in.URL, SecretEnc: enc, EventTypes: types, Description: in.Description, CreatedBy: in.CreatedBy,
	})
	if err != nil {
		return "", store.WebhookEndpoint{}, err
	}
	return secret, ep, nil
}

// Endpoints lists an organization's endpoints.
func (s *Service) Endpoints(ctx context.Context, orgID uuid.UUID) ([]store.WebhookEndpoint, error) {
	return s.q.ListWebhookEndpoints(ctx, orgID)
}

// Endpoint returns one endpoint or ErrNotFound.
func (s *Service) Endpoint(ctx context.Context, orgID, id uuid.UUID) (store.WebhookEndpoint, error) {
	ep, err := s.q.GetWebhookEndpoint(ctx, store.GetWebhookEndpointParams{ID: id, OrganizationID: orgID})
	return ep, postgres.MapNotFound(err)
}

// UpdateEndpoint changes URL, subscriptions, description or active flag.
func (s *Service) UpdateEndpoint(ctx context.Context, orgID, id uuid.UUID, in EndpointInput, active bool) (store.WebhookEndpoint, error) {
	if err := ValidateEndpointURL(in.URL, s.opts.AllowPrivate); err != nil {
		return store.WebhookEndpoint{}, err
	}
	types := in.EventTypes
	if len(types) == 0 {
		types = []string{"*"}
	}
	ep, err := s.q.UpdateWebhookEndpoint(ctx, store.UpdateWebhookEndpointParams{
		ID: id, OrganizationID: orgID, Url: in.URL, EventTypes: types, Description: in.Description, IsActive: active,
	})
	return ep, postgres.MapNotFound(err)
}

// RotateSecret issues a new signing secret and returns it once.
func (s *Service) RotateSecret(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	if s.keys == nil {
		return "", ErrEncryptionMissing
	}
	secret, err := secrets.Token("whsec_")
	if err != nil {
		return "", err
	}
	enc, err := s.keys.EncryptString(secret)
	if err != nil {
		return "", err
	}
	n, err := s.q.RotateWebhookEndpointSecret(ctx, store.RotateWebhookEndpointSecretParams{ID: id, OrganizationID: orgID, SecretEnc: enc})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrNotFound
	}
	return secret, nil
}

// DeleteEndpoint removes an endpoint and its delivery history.
func (s *Service) DeleteEndpoint(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteWebhookEndpoint(ctx, store.DeleteWebhookEndpointParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Deliveries lists an endpoint's delivery attempts.
func (s *Service) Deliveries(ctx context.Context, endpointID uuid.UUID, limit, offset int32) ([]store.ListWebhookDeliveriesForEndpointRow, error) {
	return s.q.ListWebhookDeliveriesForEndpoint(ctx, store.ListWebhookDeliveriesForEndpointParams{EndpointID: endpointID, Limit: limit, Offset: offset})
}

// DeliveryCounts returns how many deliveries sit in each status.
func (s *Service) DeliveryCounts(ctx context.Context, orgID uuid.UUID) (map[store.WebhookDeliveryStatus]int64, error) {
	rows, err := s.q.CountWebhookDeliveriesByStatus(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[store.WebhookDeliveryStatus]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Count
	}
	return out, nil
}

// Retry re-queues a failed or exhausted delivery.
func (s *Service) Retry(ctx context.Context, orgID, deliveryID uuid.UUID) error {
	n, err := s.q.RetryWebhookDelivery(ctx, store.RetryWebhookDeliveryParams{ID: deliveryID, OrganizationID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delivery worker ----------------------------------------------------------------

// Stats summarises one DeliverDue pass.
type Stats struct {
	Claimed, Succeeded, Failed, Exhausted int
}

// DeliverDue claims up to batch due deliveries and attempts each once. Run it
// from a worker loop; several workers may run concurrently.
func (s *Service) DeliverDue(ctx context.Context, batch int32) (Stats, error) {
	var st Stats
	if s.keys == nil {
		return st, ErrEncryptionMissing
	}
	lease := (s.opts.Timeout + 5*time.Second).Seconds()
	claimed, err := s.q.ClaimDueWebhookDeliveries(ctx, store.ClaimDueWebhookDeliveriesParams{LeaseSeconds: lease, BatchSize: batch})
	if err != nil {
		return st, fmt.Errorf("events: claim deliveries: %w", err)
	}
	st.Claimed = len(claimed)
	for _, d := range claimed {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		status, dErr := s.attempt(ctx, d.ID)
		if dErr == nil {
			st.Succeeded++
			continue
		}
		var code pgtype.Int2
		if status > 0 {
			code = pgtype.Int2{Int16: int16(status), Valid: true}
		}
		msg := dErr.Error()
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		if err := s.q.MarkWebhookDeliveryFailed(ctx, store.MarkWebhookDeliveryFailedParams{
			ID: d.ID, LastStatusCode: code, LastError: &msg, NextAttemptAt: time.Now().Add(s.backoff(d.Attempts)),
		}); err != nil {
			return st, fmt.Errorf("events: mark failed: %w", err)
		}
		if d.Attempts >= d.MaxAttempts {
			st.Exhausted++
		} else {
			st.Failed++
		}
	}
	return st, nil
}

// attempt performs one HTTP delivery. It returns the response status (0 when
// no response) and nil on 2xx.
func (s *Service) attempt(ctx context.Context, deliveryID uuid.UUID) (int, error) {
	row, err := s.q.GetWebhookDeliveryContext(ctx, deliveryID)
	if err != nil {
		return 0, fmt.Errorf("load delivery: %w", err)
	}
	if !row.WebhookEndpoint.IsActive {
		return 0, errors.New("endpoint is inactive")
	}
	secret, err := s.keys.DecryptString(row.WebhookEndpoint.SecretEnc)
	if err != nil {
		return 0, fmt.Errorf("decrypt secret: %w", err)
	}
	body, err := json.Marshal(Envelope{
		ID: row.Event.ID, Type: row.Event.Type, CreatedAt: row.Event.CreatedAt.UTC(),
		ResourceType: row.Event.ResourceType, ResourceID: row.Event.ResourceID, Data: json.RawMessage(row.Event.Payload),
	})
	if err != nil {
		return 0, err
	}
	ts := time.Now().Unix()
	sig := Sign(secret, ts, body)

	reqCtx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, row.WebhookEndpoint.Url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.opts.UserAgent)
	req.Header.Set(HeaderID, row.Event.ID.String())
	req.Header.Set(HeaderDelivery, row.WebhookDelivery.ID.String())
	req.Header.Set(HeaderTimestamp, fmt.Sprint(ts))
	req.Header.Set(HeaderSignature, sig)

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("endpoint returned HTTP %d", resp.StatusCode)
	}
	if err := s.q.MarkWebhookDeliverySucceeded(ctx, store.MarkWebhookDeliverySucceededParams{
		ID: deliveryID, LastStatusCode: pgtype.Int2{Int16: int16(resp.StatusCode), Valid: true},
	}); err != nil {
		return resp.StatusCode, fmt.Errorf("mark succeeded: %w", err)
	}
	return resp.StatusCode, nil
}

// backoff grows exponentially with jitter: 30s, 1m, 2m, 4m ... capped.
func (s *Service) backoff(attempts int32) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(float64(s.opts.BaseBackoff) * math.Pow(2, float64(attempts-1)))
	if d > s.opts.MaxBackoff {
		d = s.opts.MaxBackoff
	}
	jitter := time.Duration(rand.Int64N(int64(d / 5)))
	return d + jitter
}

// Wire format ---------------------------------------------------------------------

// Header names sent with every delivery.
const (
	HeaderID        = "Webhook-Id"        // event id, stable across retries
	HeaderDelivery  = "Webhook-Delivery"  // delivery id, unique per endpoint
	HeaderTimestamp = "Webhook-Timestamp" // unix seconds, part of the signed string
	HeaderSignature = "Webhook-Signature" // "v1=<hex hmac-sha256>"
)

// Envelope is the JSON body merchants receive.
type Envelope struct {
	ID           uuid.UUID       `json:"id"`
	Type         string          `json:"type"`
	CreatedAt    time.Time       `json:"created_at"`
	ResourceType string          `json:"resource_type"`
	ResourceID   uuid.UUID       `json:"resource_id"`
	Data         json.RawMessage `json:"data"`
}

// Sign computes the signature header value for a body sent at ts.
// signed string = "<ts>.<body>", key = the endpoint's whsec_ secret.
func Sign(secret string, ts int64, body []byte) string {
	msg := append([]byte(fmt.Sprintf("%d.", ts)), body...)
	return "v1=" + secrets.SignHMAC([]byte(secret), msg)
}

// Verify is the merchant-side check, exported so integration docs and tests
// can share one implementation. tolerance bounds replay.
func Verify(secret string, header string, ts int64, body []byte, tolerance time.Duration) bool {
	if tolerance > 0 && time.Since(time.Unix(ts, 0)).Abs() > tolerance {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "v1="); ok {
			msg := append([]byte(fmt.Sprintf("%d.", ts)), body...)
			if secrets.VerifyHMAC([]byte(secret), msg, v) {
				return true
			}
		}
	}
	return false
}

// SSRF guard --------------------------------------------------------------------------

// ValidateEndpointURL accepts only https URLs to public hosts. It is also
// used by checkout for per-invoice callback URLs.
func ValidateEndpointURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ErrBadURL
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		if !allowPrivate {
			return ErrBadURL
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil && !allowPrivate && !isPublic(ip) {
		return ErrBadURL
	}
	return nil
}

func isPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	// carrier-grade NAT and IPv4 special ranges not covered above
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4"} {
		if p := netip.MustParsePrefix(cidr); p.Contains(ip) {
			return false
		}
	}
	return true
}

// newClient builds an HTTP client that re-checks the resolved IP at dial time
// so a public hostname cannot point at an internal address (DNS rebinding).
func newClient(opts Options) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy:               nil, // never honour HTTP_PROXY for webhooks
		MaxIdleConns:        50,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		TLSClientConfig:     opts.TLSClientConfig,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !opts.AllowPrivate && !isPublic(ip) {
					return nil, fmt.Errorf("events: %s resolves to non-public address %s", host, ip)
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("events: %s did not resolve", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	return &http.Client{
		Timeout:   opts.Timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // treat redirects as failures; never follow to an internal host
		},
	}
}

// Inbound provider callbacks ----------------------------------------------------------

// RecordProviderEvent stores a callback from a custodial provider or node
// service, deduplicating on the provider's own event id. created is false
// when the event was already recorded.
func (s *Service) RecordProviderEvent(ctx context.Context, providerID int16, externalID, eventType *string, payload []byte, signatureValid *bool) (store.ProviderWebhookEvent, bool, error) {
	var sig pgtype.Bool
	if signatureValid != nil {
		sig = pgtype.Bool{Bool: *signatureValid, Valid: true}
	}
	ev, err := s.q.InsertProviderWebhookEvent(ctx, store.InsertProviderWebhookEventParams{
		ProviderID: providerID, ExternalEventID: externalID, EventType: eventType, Payload: payload, SignatureValid: sig,
	})
	if err != nil {
		if postgres.IsNotFound(err) {
			return store.ProviderWebhookEvent{}, false, nil
		}
		return store.ProviderWebhookEvent{}, false, err
	}
	return ev, true, nil
}

// UnprocessedProviderEvents lists callbacks a worker still has to handle.
func (s *Service) UnprocessedProviderEvents(ctx context.Context, limit int32) ([]store.ProviderWebhookEvent, error) {
	return s.q.ListUnprocessedProviderWebhookEvents(ctx, limit)
}

// MarkProviderEventProcessed closes a callback, with an error message if it failed.
func (s *Service) MarkProviderEventProcessed(ctx context.Context, id int64, processErr error) error {
	var msg *string
	if processErr != nil {
		m := processErr.Error()
		msg = &m
	}
	return s.q.MarkProviderWebhookEventProcessed(ctx, store.MarkProviderWebhookEventProcessedParams{ID: id, Error: msg})
}
