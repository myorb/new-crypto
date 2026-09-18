-- Treasury: movements between platform-controlled addresses (sweeps,
-- fee top-ups, hot/cold rebalancing). Owned by internal/treasury.

-- name: CreateInternalTransfer :one
INSERT INTO internal_transfers (kind, asset_id, provider_id, from_address_id, to_address_id, amount)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetInternalTransfer :one
SELECT * FROM internal_transfers WHERE id = $1;

-- name: ListInternalTransfers :many
SELECT sqlc.embed(t), a.code AS asset_code, fa.address AS from_address, ta.address AS to_address
FROM internal_transfers t
JOIN assets a     ON a.id = t.asset_id
JOIN addresses fa ON fa.id = t.from_address_id
JOIN addresses ta ON ta.id = t.to_address_id
ORDER BY t.created_at DESC
LIMIT $1 OFFSET $2;

-- name: ListQueuedInternalTransfers :many
SELECT * FROM internal_transfers
WHERE status = 'queued'
ORDER BY created_at
LIMIT $1;

-- name: ListBroadcastInternalTransfers :many
SELECT sqlc.embed(t), tx.status AS tx_status, tx.confirmations, tx.fee_native, n.required_confirmations
FROM internal_transfers t
JOIN transactions tx ON tx.id = t.transaction_id
JOIN assets a        ON a.id = t.asset_id
JOIN networks n      ON n.id = a.network_id
WHERE t.status = 'broadcast'
ORDER BY t.created_at
LIMIT $1;

-- name: MarkInternalTransferBroadcast :one
UPDATE internal_transfers
SET status = 'broadcast', transaction_id = $2, external_ref = $3
WHERE id = $1 AND status = 'queued'
RETURNING *;

-- name: CompleteInternalTransfer :one
UPDATE internal_transfers
SET status = 'confirmed', energy_used = $2, bandwidth_used = $3, cost_native = $4
WHERE id = $1 AND status = 'broadcast'
RETURNING *;

-- name: FailInternalTransfer :one
UPDATE internal_transfers
SET status = 'failed', failure_reason = $2
WHERE id = $1 AND status IN ('queued', 'broadcast')
RETURNING *;

-- Deposit addresses holding at least the threshold of an asset and with no
-- sweep already in flight.
-- name: ListSweepCandidates :many
SELECT sqlc.embed(b), sqlc.embed(ad)
FROM address_balances b
JOIN addresses ad ON ad.id = b.address_id
WHERE b.asset_id = $1
  AND ad.kind = 'deposit'
  AND ad.is_active
  AND b.balance >= $2
  AND NOT EXISTS (
      SELECT 1 FROM internal_transfers it
      WHERE it.from_address_id = b.address_id AND it.asset_id = b.asset_id AND it.status IN ('queued', 'broadcast')
  )
ORDER BY b.balance DESC
LIMIT $3;
