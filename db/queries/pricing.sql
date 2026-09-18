-- Pricing: exchange rates. Fee schedules live in fees.sql. Owned by internal/pricing.

-- name: InsertExchangeRate :one
INSERT INTO exchange_rates (base_asset_id, quote, rate, source)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetLatestExchangeRate :one
SELECT * FROM exchange_rates
WHERE base_asset_id = $1 AND quote = $2
ORDER BY fetched_at DESC, id DESC
LIMIT 1;

-- name: ListLatestExchangeRates :many
SELECT DISTINCT ON (base_asset_id) *
FROM exchange_rates
WHERE quote = $1
ORDER BY base_asset_id, fetched_at DESC, id DESC;

-- name: DeleteExchangeRatesBefore :execrows
DELETE FROM exchange_rates WHERE fetched_at < $1;
