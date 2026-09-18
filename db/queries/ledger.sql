-- Ledger. Postings (journals + entries) are written inside one transaction by
-- internal/ledger; the database checks that they balance at COMMIT and keeps
-- ledger_balances current through a trigger. Nothing here is ever updated.

-- name: GetMerchantBalances :many
SELECT * FROM merchant_balances
WHERE organization_id = $1
ORDER BY asset_code;

-- name: GetMerchantBalance :one
SELECT * FROM merchant_balances
WHERE organization_id = $1 AND asset_id = $2;

-- name: GetLedgerAccount :one
SELECT * FROM ledger_accounts
WHERE organization_id IS NOT DISTINCT FROM sqlc.narg('organization_id')
  AND asset_id = $1
  AND type = $2;

-- name: CreateLedgerAccount :execrows
INSERT INTO ledger_accounts (organization_id, asset_id, type)
VALUES (sqlc.narg('organization_id'), $1, $2)
ON CONFLICT DO NOTHING;

-- name: GetLedgerBalance :one
SELECT COALESCE(b.balance, 0)::crypto_amount AS balance
FROM ledger_accounts a
LEFT JOIN ledger_balances b ON b.account_id = a.id
WHERE a.id = $1;

-- name: CreateLedgerJournal :one
INSERT INTO ledger_journals (event_type, reference_type, reference_id, description)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateLedgerEntry :one
INSERT INTO ledger_entries (journal_id, account_id, amount)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetLedgerJournalByReference :one
SELECT * FROM ledger_journals
WHERE event_type = $1 AND reference_type = $2 AND reference_id = $3;

-- name: ListLedgerEntriesForJournal :many
SELECT sqlc.embed(e), sqlc.embed(a)
FROM ledger_entries e
JOIN ledger_accounts a ON a.id = e.account_id
WHERE e.journal_id = $1
ORDER BY e.id;

-- name: ListLedgerEntriesForAccount :many
SELECT sqlc.embed(e), sqlc.embed(j)
FROM ledger_entries e
JOIN ledger_journals j ON j.id = e.journal_id
WHERE e.account_id = $1
ORDER BY e.id DESC
LIMIT $2 OFFSET $3;

-- name: ListPlatformBalances :many
SELECT sqlc.embed(a), COALESCE(b.balance, 0)::crypto_amount AS balance
FROM ledger_accounts a
LEFT JOIN ledger_balances b ON b.account_id = a.id
WHERE a.organization_id IS NULL
ORDER BY a.asset_id, a.type;
