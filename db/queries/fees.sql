-- Pricing rules. Percentages are basis points (1 % = 100); see fee_schedules in
-- 00007. Rows are never repriced: close the old one and insert the new one in
-- the same transaction.

-- name: ResolveFeeSchedule :one
-- Most specific open schedule for (organization, asset):
-- (org, asset) → (org, any asset) → (any org, asset) → platform default.
SELECT * FROM fee_schedules
WHERE (organization_id = sqlc.arg('organization_id') OR organization_id IS NULL)
  AND (asset_id = sqlc.arg('asset_id') OR asset_id IS NULL)
  AND effective_from <= now()
  AND (effective_to IS NULL OR effective_to > now())
ORDER BY (organization_id IS NOT NULL) DESC, (asset_id IS NOT NULL) DESC
LIMIT 1;

-- name: ListFeeSchedules :many
-- All schedules of one scope, newest first. NULL organization = platform defaults.
SELECT * FROM fee_schedules
WHERE organization_id IS NOT DISTINCT FROM sqlc.narg('organization_id')
ORDER BY effective_from DESC, created_at DESC;

-- name: CreateFeeSchedule :one
INSERT INTO fee_schedules (
    organization_id, asset_id,
    deposit_fee_bps, deposit_fee_fixed, deposit_fee_min, deposit_fee_max,
    deposit_network_fee, network_fee_payer, spread_bps,
    withdrawal_fee_bps, withdrawal_fee_fixed, pass_network_fee,
    fixed_fee_currency, effective_from
) VALUES (
    sqlc.narg('organization_id'), sqlc.narg('asset_id'),
    sqlc.arg('deposit_fee_bps'), sqlc.arg('deposit_fee_fixed'), sqlc.narg('deposit_fee_min'), sqlc.narg('deposit_fee_max'),
    sqlc.arg('deposit_network_fee'), sqlc.arg('network_fee_payer'), sqlc.arg('spread_bps'),
    sqlc.arg('withdrawal_fee_bps'), sqlc.arg('withdrawal_fee_fixed'), sqlc.arg('pass_network_fee'),
    sqlc.narg('fixed_fee_currency'), sqlc.arg('effective_from')
)
RETURNING *;

-- name: CloseFeeSchedule :execrows
-- Ends an open schedule. Returns 0 rows if it was already closed.
UPDATE fee_schedules
SET effective_to = sqlc.arg('effective_to')
WHERE id = sqlc.arg('id') AND effective_to IS NULL;
