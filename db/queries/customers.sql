-- Customers: the merchant's payers.
-- Owned by internal/customers.

-- name: CreateCustomer :one
INSERT INTO customers (organization_id, external_id, email, name, country_code, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- Find-or-create by email; used when an invoice arrives with a customer_email.
-- name: UpsertCustomerByEmail :one
INSERT INTO customers (organization_id, email)
VALUES ($1, $2)
ON CONFLICT (organization_id, email) DO UPDATE SET email = EXCLUDED.email
RETURNING *;

-- name: GetCustomer :one
SELECT * FROM customers WHERE id = $1 AND organization_id = $2;

-- name: GetCustomerByID :one
SELECT * FROM customers WHERE id = $1;

-- name: GetCustomerByEmail :one
SELECT * FROM customers WHERE organization_id = $1 AND email = $2;

-- name: GetCustomerByExternalID :one
SELECT * FROM customers WHERE organization_id = $1 AND external_id = $2;

-- name: UpdateCustomer :one
UPDATE customers
SET external_id  = $3,
    email        = $4,
    name         = $5,
    country_code = $6,
    metadata     = $7
WHERE id = $1 AND organization_id = $2
RETURNING *;

-- name: SetCustomerStatus :one
UPDATE customers
SET status         = sqlc.arg('status')::customer_status,
    blocked_at     = CASE WHEN sqlc.arg('status')::customer_status = 'blocked' THEN COALESCE(blocked_at, now()) ELSE NULL END,
    blocked_reason = CASE WHEN sqlc.arg('status')::customer_status = 'blocked' THEN sqlc.narg('reason')::text  ELSE NULL END
WHERE id = $1 AND organization_id = $2
RETURNING *;

-- Dashboard list: one row per customer with activity summed from their
-- invoices. currency selects which invoices count towards volume.
-- name: ListCustomers :many
SELECT sqlc.embed(c),
       count(i.id)                                                                   AS invoices,
       count(i.id) FILTER (WHERE i.status IN ('confirmed', 'completed'))             AS paid,
       COALESCE(sum(i.price_amount) FILTER (WHERE i.status IN ('confirmed', 'completed') AND i.price_currency = sqlc.arg('currency')), 0)::numeric AS volume,
       COALESCE(max(i.created_at), c.created_at)::timestamptz                        AS last_activity_at
FROM customers c
LEFT JOIN invoices i ON i.customer_id = c.id
WHERE c.organization_id = $1
  AND (c.status = sqlc.narg('status') OR sqlc.narg('status') IS NULL)
GROUP BY c.id
ORDER BY last_activity_at DESC, c.id DESC
LIMIT $2 OFFSET $3;

-- name: CountCustomers :one
SELECT count(*) FROM customers
WHERE organization_id = $1
  AND (status = sqlc.narg('status') OR sqlc.narg('status') IS NULL);

-- name: CountCustomersSince :one
SELECT count(*) FROM customers WHERE organization_id = $1 AND created_at >= $2;

-- Repeat rate and lifetime value over customers that paid at least once.
-- name: CustomerStats :one
SELECT count(*) FILTER (WHERE s.paid > 0)                                                      AS paying,
       count(*) FILTER (WHERE s.paid > 1)                                                      AS repeat,
       COALESCE(avg(s.volume) FILTER (WHERE s.paid > 0), 0)::numeric                           AS avg_volume,
       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY s.volume) FILTER (WHERE s.paid > 0), 0)::numeric AS median_volume
FROM (
    SELECT c.id,
           count(i.id) FILTER (WHERE i.status IN ('confirmed', 'completed')) AS paid,
           COALESCE(sum(i.price_amount) FILTER (WHERE i.status IN ('confirmed', 'completed') AND i.price_currency = $2), 0)::numeric AS volume
    FROM customers c
    LEFT JOIN invoices i ON i.customer_id = c.id
    WHERE c.organization_id = $1
    GROUP BY c.id
) s;

-- name: ListCustomerInvoices :many
SELECT * FROM invoices
WHERE customer_id = $1 AND organization_id = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;
