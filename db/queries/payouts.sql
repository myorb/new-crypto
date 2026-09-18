-- Payouts: merchant withdrawals and their saved destination addresses.
-- Owned by internal/payouts.

-- name: CreateWithdrawal :one
INSERT INTO withdrawals (organization_id, external_id, asset_id, to_address, to_memo, amount, fee_amount, fee_bps_applied, fee_schedule_id,
                         requested_by, requested_by_api_key, metadata, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetWithdrawal :one
SELECT * FROM withdrawals WHERE id = $1;

-- name: GetWithdrawalForOrganization :one
SELECT * FROM withdrawals WHERE id = $1 AND organization_id = $2;

-- name: GetWithdrawalByExternalID :one
SELECT * FROM withdrawals WHERE organization_id = $1 AND external_id = $2;

-- name: ListWithdrawals :many
SELECT sqlc.embed(w), sqlc.embed(a), n.code AS network_code, tx.hash AS tx_hash
FROM withdrawals w
JOIN assets a            ON a.id = w.asset_id
JOIN networks n          ON n.id = a.network_id
LEFT JOIN transactions tx ON tx.id = w.transaction_id
WHERE w.organization_id = $1
  AND (w.status = sqlc.narg('status') OR sqlc.narg('status') IS NULL)
ORDER BY w.created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountWithdrawalsByStatus :many
SELECT status, count(*) AS count
FROM withdrawals
WHERE organization_id = $1
GROUP BY status;

-- name: ListWithdrawalsToProcess :many
SELECT * FROM withdrawals
WHERE status IN ('approved', 'queued')
ORDER BY created_at
LIMIT $1;

-- name: ListBroadcastWithdrawals :many
SELECT sqlc.embed(w), tx.status AS tx_status, tx.confirmations, tx.fee_native, n.required_confirmations
FROM withdrawals w
JOIN transactions tx ON tx.id = w.transaction_id
JOIN assets a        ON a.id = w.asset_id
JOIN networks n      ON n.id = a.network_id
WHERE w.status = 'broadcast'
ORDER BY w.created_at
LIMIT $1;

-- name: ApproveWithdrawal :one
UPDATE withdrawals
SET status = 'approved', approved_by = $2, approved_at = now()
WHERE id = $1 AND status = 'pending_approval'
RETURNING *;

-- name: RejectWithdrawal :one
UPDATE withdrawals
SET status = 'rejected', approved_by = $2, approved_at = now(), failure_reason = $3, completed_at = now()
WHERE id = $1 AND status = 'pending_approval'
RETURNING *;

-- name: CancelWithdrawal :one
UPDATE withdrawals
SET status = 'cancelled', completed_at = now()
WHERE id = $1 AND organization_id = $2 AND status IN ('pending_approval', 'approved')
RETURNING *;

-- name: QueueWithdrawal :one
UPDATE withdrawals
SET status = 'queued', provider_id = $2, from_address_id = $3
WHERE id = $1 AND status = 'approved'
RETURNING *;

-- name: MarkWithdrawalBroadcast :one
UPDATE withdrawals
SET status = 'broadcast', transaction_id = $2, external_ref = $3
WHERE id = $1 AND status IN ('approved', 'queued')
RETURNING *;

-- name: CompleteWithdrawal :one
UPDATE withdrawals
SET status = 'confirmed', network_fee_native = $2, completed_at = now()
WHERE id = $1 AND status = 'broadcast'
RETURNING *;

-- name: FailWithdrawal :one
UPDATE withdrawals
SET status = 'failed', failure_reason = $2, completed_at = now()
WHERE id = $1 AND status IN ('approved', 'queued', 'broadcast')
RETURNING *;

-- Saved destination addresses -------------------------------------------------

-- name: CreateWithdrawalAddress :one
INSERT INTO withdrawal_addresses (organization_id, network_id, address, memo, label, is_whitelisted, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetWithdrawalAddress :one
SELECT * FROM withdrawal_addresses WHERE id = $1 AND organization_id = $2;

-- name: ListWithdrawalAddresses :many
SELECT sqlc.embed(wa), n.code AS network_code, n.name AS network_name
FROM withdrawal_addresses wa
JOIN networks n ON n.id = wa.network_id
WHERE wa.organization_id = $1
ORDER BY wa.created_at DESC;

-- name: IsWithdrawalAddressWhitelisted :one
SELECT EXISTS (
    SELECT 1 FROM withdrawal_addresses
    WHERE organization_id = $1 AND network_id = $2 AND address = $3
      AND memo IS NOT DISTINCT FROM sqlc.narg('memo') AND is_whitelisted
) AS whitelisted;

-- name: SetWithdrawalAddressWhitelisted :execrows
UPDATE withdrawal_addresses SET is_whitelisted = $3
WHERE id = $1 AND organization_id = $2;

-- name: DeleteWithdrawalAddress :execrows
DELETE FROM withdrawal_addresses WHERE id = $1 AND organization_id = $2;

-- Dashboard aggregates ----------------------------------------------------------

-- name: SumCompletedWithdrawalsByAsset :many
SELECT asset_id, count(*) AS count, COALESCE(SUM(amount), 0)::crypto_amount AS amount, COALESCE(SUM(fee_amount), 0)::crypto_amount AS fees
FROM withdrawals
WHERE organization_id = $1 AND status = 'confirmed' AND completed_at >= $2
GROUP BY asset_id;
