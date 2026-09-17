-- Reference data: networks and assets the platform supports.

-- name: ListEnabledNetworks :many
SELECT * FROM networks
WHERE is_enabled
ORDER BY code;

-- name: GetNetworkByCode :one
SELECT * FROM networks
WHERE code = $1;

-- name: ListEnabledAssets :many
SELECT a.*, n.code AS network_code, n.name AS network_name
FROM assets a
JOIN networks n ON n.id = a.network_id
WHERE a.is_enabled AND n.is_enabled
ORDER BY n.code, a.code;

-- name: GetAssetByCode :one
SELECT * FROM assets
WHERE code = $1;

-- name: SetAssetEnabled :exec
UPDATE assets SET is_enabled = $2
WHERE id = $1;
