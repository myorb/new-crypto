-- Events and webhooks: the outbound fan-out of everything that happened,
-- plus the inbound log of provider callbacks. Owned by internal/events.

-- Endpoints -------------------------------------------------------------------

-- name: CreateWebhookEndpoint :one
INSERT INTO webhook_endpoints (organization_id, url, secret_enc, event_types, description, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetWebhookEndpoint :one
SELECT * FROM webhook_endpoints WHERE id = $1 AND organization_id = $2;

-- name: ListWebhookEndpoints :many
SELECT * FROM webhook_endpoints
WHERE organization_id = $1
ORDER BY created_at;

-- name: ListActiveEndpointsForEvent :many
SELECT * FROM webhook_endpoints
WHERE organization_id = $1
  AND is_active
  AND ('*' = ANY(event_types) OR sqlc.arg('event_type')::text = ANY(event_types));

-- name: UpdateWebhookEndpoint :one
UPDATE webhook_endpoints
SET url = $3, event_types = $4, description = $5, is_active = $6
WHERE id = $1 AND organization_id = $2
RETURNING *;

-- name: RotateWebhookEndpointSecret :execrows
UPDATE webhook_endpoints SET secret_enc = $3
WHERE id = $1 AND organization_id = $2;

-- name: DeleteWebhookEndpoint :execrows
DELETE FROM webhook_endpoints WHERE id = $1 AND organization_id = $2;

-- Events ----------------------------------------------------------------------

-- name: CreateEvent :one
INSERT INTO events (organization_id, type, resource_type, resource_id, payload)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE id = $1 AND organization_id = $2;

-- name: ListEvents :many
SELECT * FROM events
WHERE organization_id = $1
  AND (type = sqlc.narg('type') OR sqlc.narg('type') IS NULL)
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListEventsForResource :many
SELECT * FROM events
WHERE resource_type = $1 AND resource_id = $2
ORDER BY created_at DESC;

-- Deliveries ------------------------------------------------------------------

-- name: CreateWebhookDelivery :one
INSERT INTO webhook_deliveries (endpoint_id, event_id)
VALUES ($1, $2)
ON CONFLICT (endpoint_id, event_id) DO NOTHING
RETURNING *;

-- name: GetWebhookDelivery :one
SELECT * FROM webhook_deliveries WHERE id = $1;

-- name: ListWebhookDeliveriesForEndpoint :many
SELECT sqlc.embed(d), e.type AS event_type, e.resource_type, e.resource_id
FROM webhook_deliveries d
JOIN events e ON e.id = d.event_id
WHERE d.endpoint_id = $1
ORDER BY d.created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountWebhookDeliveriesByStatus :many
SELECT d.status, count(*) AS count
FROM webhook_deliveries d
JOIN webhook_endpoints we ON we.id = d.endpoint_id
WHERE we.organization_id = $1
GROUP BY d.status;

-- Atomically claims a batch of due deliveries for one worker: bumps the
-- attempt counter and pushes next_attempt_at out by the lease so a second
-- worker skips them. The worker then records success or failure.
-- name: ClaimDueWebhookDeliveries :many
UPDATE webhook_deliveries d
SET attempts        = d.attempts + 1,
    last_attempt_at = now(),
    next_attempt_at = now() + make_interval(secs => sqlc.arg('lease_seconds')::float8)
FROM (
    SELECT id FROM webhook_deliveries
    WHERE status IN ('pending', 'failed') AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT sqlc.arg('batch_size')
    FOR UPDATE SKIP LOCKED
) due
WHERE d.id = due.id
RETURNING d.*;

-- name: GetWebhookDeliveryContext :one
SELECT sqlc.embed(d), sqlc.embed(e), sqlc.embed(we)
FROM webhook_deliveries d
JOIN events e             ON e.id = d.event_id
JOIN webhook_endpoints we ON we.id = d.endpoint_id
WHERE d.id = $1;

-- name: MarkWebhookDeliverySucceeded :exec
UPDATE webhook_deliveries
SET status = 'succeeded', last_status_code = $2, last_error = NULL, delivered_at = now()
WHERE id = $1;

-- name: MarkWebhookDeliveryFailed :exec
UPDATE webhook_deliveries
SET status           = CASE WHEN attempts >= max_attempts THEN 'exhausted'::webhook_delivery_status ELSE 'failed'::webhook_delivery_status END,
    last_status_code = $2,
    last_error       = $3,
    next_attempt_at  = $4
WHERE id = $1;

-- name: RetryWebhookDelivery :execrows
UPDATE webhook_deliveries d
SET status = 'pending', attempts = 0, next_attempt_at = now(), last_error = NULL
FROM webhook_endpoints we
WHERE d.id = $1 AND we.id = d.endpoint_id AND we.organization_id = $2
  AND d.status IN ('failed', 'exhausted');

-- Inbound provider callbacks ---------------------------------------------------

-- name: InsertProviderWebhookEvent :one
INSERT INTO provider_webhook_events (provider_id, external_event_id, event_type, payload, signature_valid)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (provider_id, external_event_id) WHERE external_event_id IS NOT NULL DO NOTHING
RETURNING *;

-- name: ListUnprocessedProviderWebhookEvents :many
SELECT * FROM provider_webhook_events
WHERE processed_at IS NULL
ORDER BY received_at
LIMIT $1;

-- name: MarkProviderWebhookEventProcessed :exec
UPDATE provider_webhook_events
SET processed_at = now(), error = $2
WHERE id = $1;
