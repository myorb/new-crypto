-- Checkout: invoices and the payment options quoted on them.
-- Owned by internal/checkout.

-- name: CreateInvoice :one
INSERT INTO invoices (organization_id, external_id, price_currency, price_amount, description, customer_email, callback_url, return_url, metadata, expires_at, created_by_api_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetInvoice :one
SELECT * FROM invoices WHERE id = $1;

-- name: GetInvoiceForOrganization :one
SELECT * FROM invoices WHERE id = $1 AND organization_id = $2;

-- name: GetInvoiceByExternalID :one
SELECT * FROM invoices WHERE organization_id = $1 AND external_id = $2;

-- name: ListInvoices :many
SELECT * FROM invoices
WHERE organization_id = $1
  AND (status = sqlc.narg('status') OR sqlc.narg('status') IS NULL)
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountInvoicesByStatus :many
SELECT status, count(*) AS count
FROM invoices
WHERE organization_id = $1
GROUP BY status;

-- name: SetInvoiceStatus :one
UPDATE invoices
SET status       = sqlc.arg('status')::invoice_status,
    paid_at      = CASE WHEN sqlc.arg('status')::invoice_status IN ('paid', 'confirmed', 'completed') THEN COALESCE(paid_at, now())      ELSE paid_at      END,
    confirmed_at = CASE WHEN sqlc.arg('status')::invoice_status IN ('confirmed', 'completed')         THEN COALESCE(confirmed_at, now()) ELSE confirmed_at END,
    completed_at = CASE WHEN sqlc.arg('status')::invoice_status = 'completed'                          THEN COALESCE(completed_at, now()) ELSE completed_at END
WHERE id = $1
RETURNING *;

-- name: CancelInvoice :execrows
UPDATE invoices SET status = 'cancelled'
WHERE id = $1 AND organization_id = $2 AND status IN ('new', 'expired');

-- name: ExpireInvoices :many
UPDATE invoices SET status = 'expired'
WHERE status = 'new' AND expires_at <= now()
RETURNING *;

-- name: ExtendInvoiceExpiry :exec
UPDATE invoices SET expires_at = $2 WHERE id = $1;

-- Payment options ---------------------------------------------------------------

-- name: CreateInvoicePaymentOption :one
INSERT INTO invoice_payment_options (invoice_id, asset_id, provider_id, amount_due, exchange_rate, source_rate, spread_bps, rate_id, rate_locked_until)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetInvoicePaymentOption :one
SELECT * FROM invoice_payment_options WHERE id = $1;

-- name: GetInvoicePaymentOptionByAsset :one
SELECT * FROM invoice_payment_options WHERE invoice_id = $1 AND asset_id = $2;

-- name: ListInvoicePaymentOptions :many
SELECT sqlc.embed(o), sqlc.embed(a), n.code AS network_code, n.name AS network_name, addr.address, addr.memo AS address_memo
FROM invoice_payment_options o
JOIN assets a        ON a.id = o.asset_id
JOIN networks n      ON n.id = a.network_id
LEFT JOIN addresses addr ON addr.id = o.address_id
WHERE o.invoice_id = $1
ORDER BY n.code, a.code;

-- name: GetSelectedInvoicePaymentOption :one
SELECT * FROM invoice_payment_options WHERE invoice_id = $1 AND is_selected;

-- name: SelectInvoicePaymentOption :one
UPDATE invoice_payment_options
SET address_id        = $2,
    memo              = $3,
    is_selected       = TRUE,
    selected_at       = now(),
    rate_locked_until = $4
WHERE id = $1
RETURNING *;

-- name: RequoteInvoicePaymentOption :one
UPDATE invoice_payment_options
SET amount_due = $2, exchange_rate = $3, source_rate = $4, spread_bps = $5, rate_id = $6, rate_locked_until = $7
WHERE id = $1
RETURNING *;

-- name: AddInvoicePaymentOptionPaid :one
UPDATE invoice_payment_options
SET amount_paid = amount_paid + $2
WHERE id = $1
RETURNING *;

-- Deposit matching: which open invoice option owns this address?
-- name: FindOpenInvoiceOptionByAddress :one
SELECT sqlc.embed(o), sqlc.embed(i)
FROM invoice_payment_options o
JOIN invoices i ON i.id = o.invoice_id
WHERE o.address_id = $1
  AND o.asset_id = $2
  AND i.status IN ('new', 'partial', 'paid', 'confirmed', 'expired')
ORDER BY i.created_at DESC
LIMIT 1;
