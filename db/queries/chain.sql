-- Chain: wallets, addresses, observed transactions and transfers, scanner
-- cursors. Owned by internal/chain. Knows nothing about invoices or merchants
-- beyond the organization_id stamped on deposit addresses.

-- Wallets ---------------------------------------------------------------------

-- name: CreateWallet :one
INSERT INTO wallets (provider_id, network_id, kind, name, key_ref, xpub, derivation_path, external_wallet_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetWallet :one
SELECT * FROM wallets WHERE id = $1;

-- name: ListWallets :many
SELECT sqlc.embed(w), n.code AS network_code, p.code AS provider_code
FROM wallets w
JOIN networks n          ON n.id = w.network_id
JOIN payment_providers p ON p.id = w.provider_id
ORDER BY n.code, w.kind, w.name;

-- name: GetActiveWallet :one
SELECT * FROM wallets
WHERE network_id = $1 AND kind = $2 AND is_active
  AND (provider_id = sqlc.narg('provider_id') OR sqlc.narg('provider_id') IS NULL)
ORDER BY created_at
LIMIT 1;

-- name: ReserveWalletIndex :one
UPDATE wallets SET next_index = next_index + 1
WHERE id = $1
RETURNING (next_index - 1)::bigint AS derivation_index;

-- name: SetWalletActive :exec
UPDATE wallets SET is_active = $2 WHERE id = $1;

-- Addresses -------------------------------------------------------------------

-- name: CreateAddress :one
INSERT INTO addresses (network_id, provider_id, wallet_id, organization_id, address, memo, kind, derivation_index, external_address_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetAddress :one
SELECT * FROM addresses WHERE id = $1;

-- name: FindAddress :one
SELECT * FROM addresses
WHERE network_id = $1 AND address = $2 AND memo IS NOT DISTINCT FROM sqlc.narg('memo');

-- name: ListOrganizationAddresses :many
SELECT sqlc.embed(a), n.code AS network_code
FROM addresses a
JOIN networks n ON n.id = a.network_id
WHERE a.organization_id = $1
ORDER BY a.created_at DESC
LIMIT $2 OFFSET $3;

-- Newest first within (network, kind): several hot addresses can exist on one
-- network, and routing must pick the same one every time.
-- name: ListPlatformAddresses :many
SELECT sqlc.embed(a), n.code AS network_code
FROM addresses a
JOIN networks n ON n.id = a.network_id
WHERE a.kind IN ('hot', 'cold', 'fee') AND a.is_active
ORDER BY n.code, a.kind, a.created_at DESC, a.id DESC;

-- name: ListWatchedAddresses :many
SELECT address, memo FROM addresses
WHERE network_id = $1 AND is_active;

-- name: TouchAddress :exec
UPDATE addresses SET last_used_at = now() WHERE id = $1;

-- name: SetAddressActive :exec
UPDATE addresses SET is_active = $2 WHERE id = $1;

-- name: UpsertAddressBalance :exec
INSERT INTO address_balances (address_id, asset_id, balance, as_of_block)
VALUES ($1, $2, $3, $4)
ON CONFLICT (address_id, asset_id) DO UPDATE
    SET balance = EXCLUDED.balance, as_of_block = EXCLUDED.as_of_block, updated_at = now()
WHERE address_balances.as_of_block <= EXCLUDED.as_of_block;

-- name: ListAddressBalances :many
SELECT sqlc.embed(b), a.code AS asset_code, a.symbol
FROM address_balances b
JOIN assets a ON a.id = b.asset_id
WHERE b.address_id = $1
ORDER BY a.code;

-- Cursors ---------------------------------------------------------------------

-- name: GetChainCursor :one
SELECT * FROM chain_cursors WHERE provider_id = $1 AND network_id = $2;

-- name: UpsertChainCursor :exec
INSERT INTO chain_cursors (provider_id, network_id, last_scanned_block, last_scanned_hash, external_cursor)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (provider_id, network_id) DO UPDATE
    SET last_scanned_block = EXCLUDED.last_scanned_block,
        last_scanned_hash  = EXCLUDED.last_scanned_hash,
        external_cursor    = EXCLUDED.external_cursor,
        updated_at         = now();

-- Transactions ----------------------------------------------------------------

-- name: UpsertTransaction :one
INSERT INTO transactions (network_id, provider_id, hash, block_number, block_hash, block_timestamp, from_address, to_address, status, confirmations, fee_native, fee_details, external_tx_id, raw)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (network_id, hash) DO UPDATE
    SET block_number    = COALESCE(EXCLUDED.block_number, transactions.block_number),
        block_hash      = COALESCE(EXCLUDED.block_hash, transactions.block_hash),
        block_timestamp = COALESCE(EXCLUDED.block_timestamp, transactions.block_timestamp),
        status          = EXCLUDED.status,
        confirmations   = GREATEST(EXCLUDED.confirmations, transactions.confirmations),
        fee_native      = COALESCE(EXCLUDED.fee_native, transactions.fee_native),
        fee_details     = COALESCE(EXCLUDED.fee_details, transactions.fee_details),
        external_tx_id  = COALESCE(EXCLUDED.external_tx_id, transactions.external_tx_id),
        raw             = COALESCE(EXCLUDED.raw, transactions.raw),
        confirmed_at    = CASE WHEN EXCLUDED.status = 'confirmed' THEN COALESCE(transactions.confirmed_at, now()) ELSE transactions.confirmed_at END
RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1;

-- name: GetTransactionByHash :one
SELECT * FROM transactions WHERE network_id = $1 AND hash = $2;

-- name: UpdateTransactionConfirmations :one
UPDATE transactions
SET confirmations = $2,
    block_number  = COALESCE($3, block_number),
    status        = $4,
    confirmed_at  = CASE WHEN $4 = 'confirmed'::tx_status THEN COALESCE(confirmed_at, now()) ELSE confirmed_at END
WHERE id = $1
RETURNING *;

-- name: ListPendingTransactions :many
SELECT * FROM transactions
WHERE network_id = $1 AND status = 'pending'
ORDER BY first_seen_at
LIMIT $2;

-- Transfers -------------------------------------------------------------------

-- name: CreateTransfer :one
INSERT INTO transfers (transaction_id, network_id, asset_id, log_index, from_address, to_address, to_memo, amount, direction, address_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (transaction_id, log_index) DO NOTHING
RETURNING *;

-- name: GetTransfer :one
SELECT * FROM transfers WHERE id = $1;

-- name: GetTransferByTxLog :one
SELECT * FROM transfers WHERE transaction_id = $1 AND log_index = $2;

-- name: ListTransfersForTransaction :many
SELECT * FROM transfers WHERE transaction_id = $1 ORDER BY log_index;

-- name: ListTransfersForAddress :many
SELECT sqlc.embed(t), sqlc.embed(tx)
FROM transfers t
JOIN transactions tx ON tx.id = t.transaction_id
WHERE t.address_id = $1
ORDER BY t.created_at DESC
LIMIT $2 OFFSET $3;

-- Merchant activity: every transfer touching one of the merchant's addresses.
-- name: ListOrganizationTransfers :many
SELECT sqlc.embed(t), sqlc.embed(tx), a.code AS asset_code, a.symbol, a.decimals, n.code AS network_code, n.name AS network_name, n.required_confirmations
FROM transfers t
JOIN addresses ad    ON ad.id = t.address_id
JOIN transactions tx ON tx.id = t.transaction_id
JOIN assets a        ON a.id = t.asset_id
JOIN networks n      ON n.id = t.network_id
WHERE ad.organization_id = $1
ORDER BY t.created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountOrganizationTransfers :one
SELECT count(*)
FROM transfers t
JOIN addresses ad ON ad.id = t.address_id
WHERE ad.organization_id = $1;

-- name: CountOrganizationAddresses :one
SELECT count(*) FROM addresses WHERE organization_id = $1;
