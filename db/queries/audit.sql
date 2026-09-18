-- Audit trail and API idempotency. Owned by internal/audit.

-- name: InsertAuditLog :one
INSERT INTO audit_logs (organization_id, actor_user_id, actor_api_key_id, action, entity_type, entity_id, ip_address, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: ListAuditLogs :many
SELECT * FROM audit_logs
WHERE organization_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: ListAuditLogsForEntity :many
SELECT * FROM audit_logs
WHERE entity_type = $1 AND entity_id = $2
ORDER BY created_at DESC, id DESC
LIMIT $3;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys
WHERE organization_id = $1 AND idempotency_key = $2 AND expires_at > now();

-- name: InsertIdempotencyKey :execrows
INSERT INTO idempotency_keys (organization_id, idempotency_key, request_hash)
VALUES ($1, $2, $3)
ON CONFLICT (organization_id, idempotency_key) DO NOTHING;

-- name: StoreIdempotentResponse :exec
UPDATE idempotency_keys
SET response_code = $3, response_body = $4
WHERE organization_id = $1 AND idempotency_key = $2;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at < now();
