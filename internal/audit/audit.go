// Package audit writes the immutable audit trail and implements API
// idempotency keys. It depends on nothing else and everything may call it.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"templ-app/internal/postgres"
	"templ-app/internal/store"
)

// ErrIdempotencyMismatch means the same key was reused with a different
// request body: the client must not get the cached response.
var ErrIdempotencyMismatch = errors.New("audit: idempotency key reused with a different request")

// ErrIdempotencyInFlight means another request with this key is still being
// processed and has not stored a response yet.
var ErrIdempotencyInFlight = errors.New("audit: idempotent request still in progress")

// Service records audit entries and idempotency state.
type Service struct {
	q *store.Queries
}

// New builds the service.
func New(pool *pgxpool.Pool) *Service { return &Service{q: store.New(pool)} }

// WithTx binds the service to a transaction so the audit row commits or rolls
// back with the business change it describes.
func (s *Service) WithTx(tx pgx.Tx) *Service { return &Service{q: s.q.WithTx(tx)} }

// Entry is one audit record. Actor fields are optional: system jobs have none.
type Entry struct {
	OrganizationID uuid.NullUUID
	ActorUserID    uuid.NullUUID
	ActorAPIKeyID  uuid.NullUUID
	Action         string // "invoice.created", "api_key.revoked", ...
	EntityType     string // "invoice", "api_key", ...
	EntityID       string
	IP             *netip.Addr
	Metadata       any // marshalled to JSONB; nil -> {}
}

// Record inserts an audit row.
func (s *Service) Record(ctx context.Context, e Entry) error {
	if e.Action == "" {
		return errors.New("audit: action is required")
	}
	meta := []byte("{}")
	if e.Metadata != nil {
		var err error
		if meta, err = json.Marshal(e.Metadata); err != nil {
			return fmt.Errorf("audit: marshal metadata: %w", err)
		}
	}
	var entityType, entityID *string
	if e.EntityType != "" {
		entityType = &e.EntityType
	}
	if e.EntityID != "" {
		entityID = &e.EntityID
	}
	_, err := s.q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		OrganizationID: e.OrganizationID,
		ActorUserID:    e.ActorUserID,
		ActorApiKeyID:  e.ActorAPIKeyID,
		Action:         e.Action,
		EntityType:     entityType,
		EntityID:       entityID,
		IpAddress:      e.IP,
		Metadata:       meta,
	})
	return err
}

// List returns an organization's audit trail, newest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, limit, offset int32) ([]store.AuditLog, error) {
	return s.q.ListAuditLogs(ctx, store.ListAuditLogsParams{
		OrganizationID: uuid.NullUUID{UUID: orgID, Valid: true},
		Limit:          limit,
		Offset:         offset,
	})
}

// ForEntity returns the trail of one entity.
func (s *Service) ForEntity(ctx context.Context, entityType, entityID string, limit int32) ([]store.AuditLog, error) {
	return s.q.ListAuditLogsForEntity(ctx, store.ListAuditLogsForEntityParams{
		EntityType: &entityType,
		EntityID:   &entityID,
		Limit:      limit,
	})
}

// RequestHash fingerprints a request body for idempotency comparison.
func RequestHash(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

// Replay is a stored response for an idempotency key.
type Replay struct {
	StatusCode int
	Body       []byte
}

// BeginIdempotent claims an idempotency key for a request. It returns
// (nil, nil) when the caller owns the key and should execute the request,
// a Replay when an identical earlier request already completed,
// ErrIdempotencyInFlight when an identical request is still running, and
// ErrIdempotencyMismatch when the body differs from the first use.
func (s *Service) BeginIdempotent(ctx context.Context, orgID uuid.UUID, key string, body []byte) (*Replay, error) {
	hash := RequestHash(body)
	n, err := s.q.InsertIdempotencyKey(ctx, store.InsertIdempotencyKeyParams{
		OrganizationID: orgID, IdempotencyKey: key, RequestHash: hash,
	})
	if err != nil {
		return nil, err
	}
	if n == 1 {
		return nil, nil
	}
	existing, err := s.q.GetIdempotencyKey(ctx, store.GetIdempotencyKeyParams{OrganizationID: orgID, IdempotencyKey: key})
	if err != nil {
		if postgres.IsNotFound(err) {
			// The earlier row expired between the insert conflict and this read; let the caller retry.
			return nil, ErrIdempotencyInFlight
		}
		return nil, err
	}
	if string(existing.RequestHash) != string(hash) {
		return nil, ErrIdempotencyMismatch
	}
	if !existing.ResponseCode.Valid {
		return nil, ErrIdempotencyInFlight
	}
	return &Replay{StatusCode: int(existing.ResponseCode.Int16), Body: existing.ResponseBody}, nil
}

// CompleteIdempotent stores the response for a claimed key.
func (s *Service) CompleteIdempotent(ctx context.Context, orgID uuid.UUID, key string, status int, body []byte) error {
	if body == nil {
		body = []byte("null")
	}
	return s.q.StoreIdempotentResponse(ctx, store.StoreIdempotentResponseParams{
		OrganizationID: orgID,
		IdempotencyKey: key,
		ResponseCode:   toInt2(status),
		ResponseBody:   body,
	})
}

// PurgeExpiredIdempotencyKeys deletes keys past their 24 hour window.
func (s *Service) PurgeExpiredIdempotencyKeys(ctx context.Context) (int64, error) {
	return s.q.DeleteExpiredIdempotencyKeys(ctx)
}
