-- Organizations (merchants), membership, invitations, API keys and per-merchant
-- asset settings. Owned by internal/org.

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

-- name: UpdateOrganization :one
UPDATE organizations
SET name = $2, legal_name = $3, country_code = $4, default_currency = $5, require_mfa = $6, settings = $7
WHERE id = $1
RETURNING *;

-- name: SetOrganizationStatus :exec
UPDATE organizations
SET status = $2,
    kyb_verified_at = CASE WHEN $2 = 'active'::organization_status THEN COALESCE(kyb_verified_at, now()) ELSE kyb_verified_at END
WHERE id = $1;

-- name: ListOrganizationsForUser :many
SELECT sqlc.embed(o), m.role
FROM organizations o
JOIN organization_members m ON m.organization_id = o.id
WHERE m.user_id = $1
ORDER BY o.name;

-- Membership -----------------------------------------------------------------

-- name: AddOrganizationMember :exec
INSERT INTO organization_members (organization_id, user_id, role, invited_by)
VALUES ($1, $2, $3, $4);

-- name: GetOrganizationMember :one
SELECT * FROM organization_members
WHERE organization_id = $1 AND user_id = $2;

-- name: ListOrganizationMembers :many
SELECT sqlc.embed(m), sqlc.embed(u)
FROM organization_members m
JOIN users u ON u.id = m.user_id
WHERE m.organization_id = $1
ORDER BY m.joined_at;

-- name: UpdateOrganizationMemberRole :execrows
UPDATE organization_members SET role = $3
WHERE organization_id = $1 AND user_id = $2;

-- name: RemoveOrganizationMember :execrows
DELETE FROM organization_members
WHERE organization_id = $1 AND user_id = $2;

-- name: CountOrganizationOwners :one
SELECT count(*) FROM organization_members
WHERE organization_id = $1 AND role = 'owner';

-- Invitations ----------------------------------------------------------------

-- name: CreateOrganizationInvitation :one
INSERT INTO organization_invitations (organization_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetOrganizationInvitationByTokenHash :one
SELECT * FROM organization_invitations
WHERE token_hash = $1
  AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: ListOpenOrganizationInvitations :many
SELECT * FROM organization_invitations
WHERE organization_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
ORDER BY created_at DESC;

-- name: AcceptOrganizationInvitation :exec
UPDATE organization_invitations SET accepted_at = now() WHERE id = $1;

-- name: RevokeOrganizationInvitation :execrows
UPDATE organization_invitations SET revoked_at = now()
WHERE id = $1 AND organization_id = $2 AND accepted_at IS NULL AND revoked_at IS NULL;

-- API keys -------------------------------------------------------------------

-- name: CreateAPIKey :one
INSERT INTO api_keys (organization_id, name, key_prefix, key_hash, scopes, ip_allowlist, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetAPIKeyByHash :one
SELECT sqlc.embed(k), sqlc.embed(o)
FROM api_keys k
JOIN organizations o ON o.id = k.organization_id
WHERE k.key_hash = $1;

-- name: GetAPIKey :one
SELECT * FROM api_keys WHERE id = $1 AND organization_id = $2;

-- name: ListAPIKeys :many
SELECT * FROM api_keys
WHERE organization_id = $1
ORDER BY (revoked_at IS NULL) DESC, created_at DESC;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;

-- name: RevokeAPIKey :execrows
UPDATE api_keys SET revoked_at = now()
WHERE id = $1 AND organization_id = $2 AND revoked_at IS NULL;

-- Per-merchant asset settings -------------------------------------------------

-- name: UpsertOrganizationAsset :one
INSERT INTO organization_assets (organization_id, asset_id, is_enabled, preferred_provider_id, auto_withdraw_to)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (organization_id, asset_id) DO UPDATE
    SET is_enabled            = EXCLUDED.is_enabled,
        preferred_provider_id = EXCLUDED.preferred_provider_id,
        auto_withdraw_to      = EXCLUDED.auto_withdraw_to
RETURNING *;

-- name: ListOrganizationAssets :many
SELECT sqlc.embed(oa), sqlc.embed(a), n.code AS network_code, n.name AS network_name
FROM organization_assets oa
JOIN assets a   ON a.id = oa.asset_id
JOIN networks n ON n.id = a.network_id
WHERE oa.organization_id = $1
ORDER BY n.code, a.code;

-- name: GetOrganizationAsset :one
SELECT * FROM organization_assets
WHERE organization_id = $1 AND asset_id = $2;

-- name: DeleteOrganizationAsset :execrows
DELETE FROM organization_assets
WHERE organization_id = $1 AND asset_id = $2;
