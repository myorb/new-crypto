-- Payments: an on-chain transfer matched to a merchant, moving
-- detected -> confirmed -> credited (or reverted). Owned by internal/payments.

-- name: CreatePayment :one
INSERT INTO payments (organization_id, invoice_id, option_id, address_id, asset_id, provider_id, transfer_id, amount,
                      fee_amount, fee_bps_applied, spread_amount, network_fee_amount, fee_schedule_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentForOrganization :one
SELECT * FROM payments WHERE id = $1 AND organization_id = $2;

-- name: GetPaymentByTransfer :one
SELECT * FROM payments WHERE transfer_id = $1;

-- name: ListPayments :many
SELECT sqlc.embed(p), sqlc.embed(a), tx.hash AS tx_hash, tx.confirmations, n.required_confirmations, n.code AS network_code, i.customer_email
FROM payments p
JOIN assets a         ON a.id = p.asset_id
JOIN networks n       ON n.id = a.network_id
JOIN transfers t      ON t.id = p.transfer_id
JOIN transactions tx  ON tx.id = t.transaction_id
LEFT JOIN invoices i  ON i.id = p.invoice_id
WHERE p.organization_id = $1
  AND (p.status = sqlc.narg('status') OR sqlc.narg('status') IS NULL)
ORDER BY p.created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListPaymentsForInvoice :many
SELECT * FROM payments WHERE invoice_id = $1 ORDER BY created_at;

-- name: CountPaymentsByStatus :many
SELECT status, count(*) AS count
FROM payments
WHERE organization_id = $1
GROUP BY status;

-- name: SumCreditedPayments :one
SELECT COALESCE(SUM(amount), 0)::crypto_amount AS gross,
       COALESCE(SUM(fee_amount + spread_amount + network_fee_amount), 0)::crypto_amount AS fees
FROM payments
WHERE organization_id = $1 AND asset_id = $2 AND status = 'credited' AND credited_at >= $3;

-- Detected payments whose transaction data may have moved on. The worker
-- compares confirmations with the network threshold and confirms or reverts.
-- name: ListDetectedPayments :many
SELECT sqlc.embed(p), tx.status AS tx_status, tx.confirmations, n.required_confirmations
FROM payments p
JOIN transfers t     ON t.id = p.transfer_id
JOIN transactions tx ON tx.id = t.transaction_id
JOIN assets a        ON a.id = p.asset_id
JOIN networks n      ON n.id = a.network_id
WHERE p.status = 'detected'
ORDER BY p.detected_at
LIMIT $1;

-- name: ConfirmPayment :one
UPDATE payments
SET status = 'confirmed', confirmed_at = COALESCE(confirmed_at, now())
WHERE id = $1 AND status = 'detected'
RETURNING *;

-- name: CreditPayment :one
UPDATE payments
SET status = 'credited', credited_at = COALESCE(credited_at, now())
WHERE id = $1 AND status IN ('detected', 'confirmed')
RETURNING *;

-- name: RevertPayment :one
UPDATE payments
SET status = 'reverted'
WHERE id = $1 AND status IN ('detected', 'confirmed')
RETURNING *;

-- Dashboard aggregates ----------------------------------------------------------

-- name: SumPaymentsByDay :many
SELECT (created_at AT TIME ZONE 'UTC')::date AS day, asset_id, count(*) AS count, COALESCE(SUM(amount), 0)::crypto_amount AS amount
FROM payments
WHERE organization_id = $1 AND status IN ('confirmed', 'credited') AND created_at >= $2
GROUP BY 1, 2
ORDER BY 1;

-- name: CountPaymentsByDayAndStatus :many
SELECT (created_at AT TIME ZONE 'UTC')::date AS day, status, count(*) AS count
FROM payments
WHERE organization_id = $1 AND created_at >= $2
GROUP BY 1, 2
ORDER BY 1;

-- name: SumPaymentsByAsset :many
SELECT p.asset_id, a.code AS asset_code, a.symbol, a.name AS asset_name, count(*) AS count,
       COALESCE(SUM(p.amount), 0)::crypto_amount AS amount,
       COALESCE(SUM(p.fee_amount + p.spread_amount + p.network_fee_amount), 0)::crypto_amount AS fees
FROM payments p
JOIN assets a ON a.id = p.asset_id
WHERE p.organization_id = $1 AND p.status IN ('confirmed', 'credited') AND p.created_at >= $2
GROUP BY p.asset_id, a.code, a.symbol, a.name
ORDER BY amount DESC;

-- name: CountPaymentsByNetwork :many
SELECT n.code AS network_code, n.name AS network_name, count(*) AS count
FROM payments p
JOIN assets a   ON a.id = p.asset_id
JOIN networks n ON n.id = a.network_id
WHERE p.organization_id = $1 AND p.created_at >= $2
GROUP BY n.code, n.name
ORDER BY count DESC;

-- name: CountPaymentsSince :one
SELECT count(*) FROM payments WHERE organization_id = $1 AND created_at >= $2;
