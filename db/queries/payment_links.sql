-- Payment links: reusable checkout templates that open into invoices.
-- Owned by internal/checkout (links.go).

-- name: CreatePaymentLink :one
INSERT INTO payment_links (
    organization_id, slug, name, description, price_currency, price_amount, min_amount, max_amount,
    max_uses, invoice_ttl_seconds, collect_email, customer_id, callback_url, return_url, metadata, expires_at,
    created_by, created_by_api_key
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
RETURNING *;

-- name: GetPaymentLink :one
SELECT * FROM payment_links WHERE id = $1 AND organization_id = $2;

-- name: GetPaymentLinkByID :one
SELECT * FROM payment_links WHERE id = $1;

-- Public lookup for the hosted page.
-- name: GetPaymentLinkBySlug :one
SELECT * FROM payment_links WHERE slug = $1;

-- Dashboard list: each link with the invoices it produced. Invoices inherit
-- the link's currency, so revenue needs no currency filter.
-- name: ListPaymentLinks :many
SELECT sqlc.embed(l),
       count(i.id)                                                                             AS invoices,
       count(i.id) FILTER (WHERE i.status IN ('confirmed', 'completed'))                       AS paid,
       COALESCE(sum(i.price_amount) FILTER (WHERE i.status IN ('confirmed', 'completed')), 0)::numeric AS revenue
FROM payment_links l
LEFT JOIN invoices i ON i.payment_link_id = l.id
WHERE l.organization_id = $1
  AND (l.status = sqlc.narg('status') OR sqlc.narg('status') IS NULL)
GROUP BY l.id
ORDER BY l.created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountPaymentLinks :one
SELECT count(*) FROM payment_links
WHERE organization_id = $1
  AND (status = sqlc.narg('status') OR sqlc.narg('status') IS NULL);

-- name: PaymentLinkStats :one
SELECT count(*) FILTER (WHERE status = 'active')                                                       AS active,
       count(*) FILTER (WHERE status = 'active' AND expires_at IS NOT NULL AND expires_at < sqlc.arg('before')) AS expiring,
       COALESCE(sum(view_count), 0)::bigint                                                            AS views
FROM payment_links
WHERE organization_id = $1;

-- Invoices paid through any link since a point in time; revenue counts the
-- merchant's reporting currency only.
-- name: SumPaymentLinkInvoices :one
SELECT count(*)                                                                    AS paid,
       count(DISTINCT payment_link_id)                                             AS links,
       COALESCE(sum(price_amount) FILTER (WHERE price_currency = sqlc.arg('currency')), 0)::numeric AS revenue
FROM invoices
WHERE organization_id = $1
  AND payment_link_id IS NOT NULL
  AND status IN ('confirmed', 'completed')
  AND created_at >= sqlc.arg('since');

-- name: UpdatePaymentLink :one
UPDATE payment_links
SET name                = $3,
    description         = $4,
    price_amount        = $5,
    min_amount          = $6,
    max_amount          = $7,
    max_uses            = $8,
    invoice_ttl_seconds = $9,
    collect_email       = $10,
    callback_url        = $11,
    return_url          = $12,
    metadata            = $13,
    expires_at          = $14
WHERE id = $1 AND organization_id = $2 AND status <> 'archived'
RETURNING *;

-- name: SetPaymentLinkStatus :one
UPDATE payment_links
SET status = $3
WHERE id = $1 AND organization_id = $2
RETURNING *;

-- name: RecordPaymentLinkView :exec
UPDATE payment_links
SET view_count = view_count + 1, last_viewed_at = now()
WHERE id = $1;

-- Called when an invoice opened from the link is paid. A link that reaches
-- max_uses completes and can no longer be opened.
-- name: AddPaymentLinkUse :one
UPDATE payment_links
SET uses_count = uses_count + 1,
    status     = CASE WHEN status = 'active' AND max_uses IS NOT NULL AND uses_count + 1 >= max_uses
                      THEN 'completed'::payment_link_status ELSE status END
WHERE id = $1
RETURNING *;

-- name: ExpirePaymentLinks :many
UPDATE payment_links
SET status = 'expired'
WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL AND expires_at <= now()
RETURNING *;

-- Accepted assets -----------------------------------------------------------------

-- name: AddPaymentLinkAsset :exec
INSERT INTO payment_link_assets (payment_link_id, asset_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ClearPaymentLinkAssets :exec
DELETE FROM payment_link_assets WHERE payment_link_id = $1;

-- name: ListPaymentLinkAssets :many
SELECT a.*
FROM payment_link_assets la
JOIN assets a ON a.id = la.asset_id
WHERE la.payment_link_id = $1
ORDER BY a.code;

-- name: ListPaymentLinkAssetCodes :many
SELECT la.payment_link_id, a.id AS asset_id, a.code, a.symbol
FROM payment_link_assets la
JOIN assets a ON a.id = la.asset_id
WHERE la.payment_link_id = ANY(sqlc.arg('link_ids')::uuid[])
ORDER BY la.payment_link_id, a.code;
