-- Reference data: providers, networks and assets the platform supports.
-- Read-mostly; the app may only flip enable flags. Owned by internal/reference.

-- name: ListNetworks :many
SELECT * FROM networks
ORDER BY code;

-- name: ListEnabledNetworks :many
SELECT * FROM networks
WHERE is_enabled
ORDER BY code;

-- name: GetNetworkByCode :one
SELECT * FROM networks
WHERE code = $1;

-- name: GetNetworkByID :one
SELECT * FROM networks
WHERE id = $1;

-- name: SetNetworkEnabled :exec
UPDATE networks SET is_enabled = $2
WHERE id = $1;

-- name: ListAssets :many
SELECT * FROM assets
ORDER BY network_id, code;

-- name: ListEnabledAssets :many
SELECT sqlc.embed(a), n.code AS network_code, n.name AS network_name
FROM assets a
JOIN networks n ON n.id = a.network_id
WHERE a.is_enabled AND n.is_enabled
ORDER BY n.code, a.code;

-- name: GetAssetByCode :one
SELECT * FROM assets
WHERE code = $1;

-- name: GetAssetByID :one
SELECT * FROM assets
WHERE id = $1;

-- name: GetNativeAssetForNetwork :one
SELECT * FROM assets
WHERE network_id = $1 AND kind = 'native';

-- name: SetAssetEnabled :exec
UPDATE assets SET is_enabled = $2
WHERE id = $1;

-- name: ListEnabledProviders :many
SELECT * FROM payment_providers
WHERE is_enabled
ORDER BY code;

-- name: GetProviderByCode :one
SELECT * FROM payment_providers
WHERE code = $1;

-- name: GetProviderByID :one
SELECT * FROM payment_providers
WHERE id = $1;

-- name: SetProviderEnabled :exec
UPDATE payment_providers SET is_enabled = $2
WHERE id = $1;

-- name: ListProviderNetworks :many
SELECT sqlc.embed(pn), sqlc.embed(p)
FROM provider_networks pn
JOIN payment_providers p ON p.id = pn.provider_id
WHERE pn.network_id = $1 AND pn.is_enabled AND p.is_enabled
ORDER BY pn.priority, p.code;

-- name: GetProviderNetwork :one
SELECT * FROM provider_networks
WHERE provider_id = $1 AND network_id = $2;

-- name: GetProviderAsset :one
SELECT * FROM provider_assets
WHERE provider_id = $1 AND asset_id = $2;
