-- Organizations (merchants) and membership.

-- name: GetOrganization :one
SELECT * FROM organizations
WHERE id = $1;

-- name: GetOrganizationBySlug :one
SELECT * FROM organizations
WHERE slug = $1;

-- name: CreateOrganization :one
INSERT INTO organizations (slug, name, legal_name, country_code, default_currency)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListOrganizationsForUser :many
SELECT o.*, m.role
FROM organizations o
JOIN organization_members m ON m.organization_id = o.id
WHERE m.user_id = $1
ORDER BY o.name;

-- name: AddOrganizationMember :exec
INSERT INTO organization_members (organization_id, user_id, role, invited_by)
VALUES ($1, $2, $3, $4);
