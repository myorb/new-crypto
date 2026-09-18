// Package org owns merchants: organizations, who belongs to them and with
// what role, invitations, API keys and per-merchant asset settings.
package org

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"templ-app/internal/postgres"
	"templ-app/internal/secrets"
	"templ-app/internal/store"
)

var (
	ErrSlugTaken        = errors.New("org: slug already in use")
	ErrBadSlug          = errors.New("org: slug must be 2-63 lower-case letters, digits or hyphens")
	ErrNotMember        = errors.New("org: user is not a member of this organization")
	ErrLastOwner        = errors.New("org: an organization must keep at least one owner")
	ErrForbidden        = errors.New("org: role does not allow this action")
	ErrInviteInvalid    = errors.New("org: invitation is invalid, expired or already used")
	ErrInviteEmail      = errors.New("org: invitation was issued to a different email")
	ErrAPIKeyInvalid    = errors.New("org: API key is invalid, expired or revoked")
	ErrAPIKeyIP         = errors.New("org: API key may not be used from this address")
	ErrOrgInactive      = errors.New("org: organization is not active")
	ErrOrgAlreadyExists = errors.New("org: organization already exists")
)

var slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Service is the organizations domain.
type Service struct {
	q    *store.Queries
	pool *pgxpool.Pool
}

// New builds the service.
func New(pool *pgxpool.Pool) *Service { return &Service{q: store.New(pool), pool: pool} }

// WithTx binds the service to a transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service { return &Service{q: s.q.WithTx(tx)} }

// Organizations ----------------------------------------------------------------

// CreateInput describes a new merchant.
type CreateInput struct {
	Slug            string
	Name            string
	LegalName       *string
	CountryCode     *string // ISO 3166-1 alpha-2
	DefaultCurrency string  // ISO 4217; default USD
	OwnerID         uuid.UUID
}

// Create makes the organization and its first owner in one transaction.
func (s *Service) Create(ctx context.Context, in CreateInput) (store.Organization, error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !slugRe.MatchString(in.Slug) || len(in.Slug) < 2 {
		return store.Organization{}, ErrBadSlug
	}
	if in.DefaultCurrency == "" {
		in.DefaultCurrency = "USD"
	}
	in.DefaultCurrency = strings.ToUpper(in.DefaultCurrency)
	if in.CountryCode != nil {
		cc := strings.ToUpper(*in.CountryCode)
		in.CountryCode = &cc
	}
	return postgres.InTxRet(ctx, s.pool, func(tx pgx.Tx) (store.Organization, error) {
		q := s.q.WithTx(tx)
		o, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{
			Slug: in.Slug, Name: strings.TrimSpace(in.Name), LegalName: in.LegalName, CountryCode: text(in.CountryCode), DefaultCurrency: in.DefaultCurrency,
		})
		if err != nil {
			if postgres.IsUniqueViolation(err) {
				return store.Organization{}, ErrSlugTaken
			}
			return store.Organization{}, err
		}
		if err := q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{
			OrganizationID: o.ID, UserID: in.OwnerID, Role: store.OrganizationRoleOwner,
		}); err != nil {
			return store.Organization{}, err
		}
		return o, nil
	})
}

// Get returns an organization by id.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (store.Organization, error) {
	o, err := s.q.GetOrganization(ctx, id)
	return o, postgres.MapNotFound(err)
}

// GetBySlug returns an organization by slug.
func (s *Service) GetBySlug(ctx context.Context, slug string) (store.Organization, error) {
	o, err := s.q.GetOrganizationBySlug(ctx, strings.ToLower(slug))
	return o, postgres.MapNotFound(err)
}

// UpdateInput is the merchant-editable profile.
type UpdateInput struct {
	Name            string
	LegalName       *string
	CountryCode     *string
	DefaultCurrency string
	RequireMFA      bool
	Settings        []byte // JSON; nil keeps {}
}

// Update changes the profile fields.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (store.Organization, error) {
	if in.Settings == nil {
		in.Settings = []byte("{}")
	}
	if in.DefaultCurrency == "" {
		in.DefaultCurrency = "USD"
	}
	return s.q.UpdateOrganization(ctx, store.UpdateOrganizationParams{
		ID: id, Name: strings.TrimSpace(in.Name), LegalName: in.LegalName, CountryCode: text(in.CountryCode),
		DefaultCurrency: strings.ToUpper(in.DefaultCurrency), RequireMfa: in.RequireMFA, Settings: in.Settings,
	})
}

// SetStatus moves an organization through its lifecycle (KYB review etc).
func (s *Service) SetStatus(ctx context.Context, id uuid.UUID, status store.OrganizationStatus) error {
	return s.q.SetOrganizationStatus(ctx, store.SetOrganizationStatusParams{ID: id, Status: status})
}

// Membership is an organization seen from one user's point of view.
type Membership struct {
	Organization store.Organization
	Role         store.OrganizationRole
}

// ForUser lists the organizations a user belongs to.
func (s *Service) ForUser(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	rows, err := s.q.ListOrganizationsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, len(rows))
	for i, r := range rows {
		out[i] = Membership{Organization: r.Organization, Role: r.Role}
	}
	return out, nil
}

// Members -----------------------------------------------------------------------

// Member is a user with their role in an organization.
type Member struct {
	store.OrganizationMember
	User store.User
}

// Members lists everyone in an organization.
func (s *Service) Members(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	rows, err := s.q.ListOrganizationMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Member, len(rows))
	for i, r := range rows {
		out[i] = Member{OrganizationMember: r.OrganizationMember, User: r.User}
	}
	return out, nil
}

// Role returns the caller's role, or ErrNotMember.
func (s *Service) Role(ctx context.Context, orgID, userID uuid.UUID) (store.OrganizationRole, error) {
	m, err := s.q.GetOrganizationMember(ctx, store.GetOrganizationMemberParams{OrganizationID: orgID, UserID: userID})
	if err != nil {
		if postgres.IsNotFound(err) {
			return "", ErrNotMember
		}
		return "", err
	}
	return m.Role, nil
}

// roleRank orders roles for "at least" checks.
func roleRank(r store.OrganizationRole) int {
	switch r {
	case store.OrganizationRoleOwner:
		return 5
	case store.OrganizationRoleAdmin:
		return 4
	case store.OrganizationRoleFinance:
		return 3
	case store.OrganizationRoleDeveloper:
		return 2
	case store.OrganizationRoleViewer:
		return 1
	}
	return 0
}

// RequireRole checks that the user holds at least the given role.
func (s *Service) RequireRole(ctx context.Context, orgID, userID uuid.UUID, atLeast store.OrganizationRole) error {
	r, err := s.Role(ctx, orgID, userID)
	if err != nil {
		return err
	}
	if roleRank(r) < roleRank(atLeast) {
		return ErrForbidden
	}
	return nil
}

// SetMemberRole changes a member's role, never demoting the last owner.
func (s *Service) SetMemberRole(ctx context.Context, orgID, userID uuid.UUID, role store.OrganizationRole) error {
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if role != store.OrganizationRoleOwner {
			if err := s.guardLastOwner(ctx, q, orgID, userID); err != nil {
				return err
			}
		}
		n, err := q.UpdateOrganizationMemberRole(ctx, store.UpdateOrganizationMemberRoleParams{OrganizationID: orgID, UserID: userID, Role: role})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotMember
		}
		return nil
	})
}

// RemoveMember drops a member, never the last owner.
func (s *Service) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := s.guardLastOwner(ctx, q, orgID, userID); err != nil {
			return err
		}
		n, err := q.RemoveOrganizationMember(ctx, store.RemoveOrganizationMemberParams{OrganizationID: orgID, UserID: userID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotMember
		}
		return nil
	})
}

func (s *Service) guardLastOwner(ctx context.Context, q *store.Queries, orgID, userID uuid.UUID) error {
	m, err := q.GetOrganizationMember(ctx, store.GetOrganizationMemberParams{OrganizationID: orgID, UserID: userID})
	if err != nil {
		return postgres.MapNotFound(err)
	}
	if m.Role != store.OrganizationRoleOwner {
		return nil
	}
	owners, err := q.CountOrganizationOwners(ctx, orgID)
	if err != nil {
		return err
	}
	if owners <= 1 {
		return ErrLastOwner
	}
	return nil
}

// Invitations -------------------------------------------------------------------

// Invite issues an invitation token (returned once) for an email.
func (s *Service) Invite(ctx context.Context, orgID, inviterID uuid.UUID, email string, role store.OrganizationRole, ttl time.Duration) (string, store.OrganizationInvitation, error) {
	if ttl == 0 {
		ttl = 7 * 24 * time.Hour
	}
	token, err := secrets.Token("inv_")
	if err != nil {
		return "", store.OrganizationInvitation{}, err
	}
	inv, err := s.q.CreateOrganizationInvitation(ctx, store.CreateOrganizationInvitationParams{
		OrganizationID: orgID, Email: strings.ToLower(strings.TrimSpace(email)), Role: role,
		TokenHash: secrets.Hash(token), InvitedBy: inviterID, ExpiresAt: time.Now().Add(ttl),
	})
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return "", store.OrganizationInvitation{}, errors.New("org: an open invitation for this email already exists")
		}
		return "", store.OrganizationInvitation{}, err
	}
	return token, inv, nil
}

// InvitationByToken resolves a token so the accept page can show the org.
func (s *Service) InvitationByToken(ctx context.Context, token string) (store.OrganizationInvitation, error) {
	inv, err := s.q.GetOrganizationInvitationByTokenHash(ctx, secrets.Hash(token))
	if err != nil {
		if postgres.IsNotFound(err) {
			return store.OrganizationInvitation{}, ErrInviteInvalid
		}
		return store.OrganizationInvitation{}, err
	}
	return inv, nil
}

// AcceptInvitation adds the user as a member. The user's email must match the
// invitation's.
func (s *Service) AcceptInvitation(ctx context.Context, token string, user store.User) (store.Organization, error) {
	return postgres.InTxRet(ctx, s.pool, func(tx pgx.Tx) (store.Organization, error) {
		q := s.q.WithTx(tx)
		inv, err := q.GetOrganizationInvitationByTokenHash(ctx, secrets.Hash(token))
		if err != nil {
			if postgres.IsNotFound(err) {
				return store.Organization{}, ErrInviteInvalid
			}
			return store.Organization{}, err
		}
		if !strings.EqualFold(inv.Email, user.Email) {
			return store.Organization{}, ErrInviteEmail
		}
		if _, err := q.GetOrganizationMember(ctx, store.GetOrganizationMemberParams{OrganizationID: inv.OrganizationID, UserID: user.ID}); err == nil {
			// already a member: just consume the invitation
		} else if !postgres.IsNotFound(err) {
			return store.Organization{}, err
		} else if err := q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{
			OrganizationID: inv.OrganizationID, UserID: user.ID, Role: inv.Role, InvitedBy: uuid.NullUUID{UUID: inv.InvitedBy, Valid: true},
		}); err != nil {
			return store.Organization{}, err
		}
		if err := q.AcceptOrganizationInvitation(ctx, inv.ID); err != nil {
			return store.Organization{}, err
		}
		o, err := q.GetOrganization(ctx, inv.OrganizationID)
		return o, postgres.MapNotFound(err)
	})
}

// OpenInvitations lists pending invitations.
func (s *Service) OpenInvitations(ctx context.Context, orgID uuid.UUID) ([]store.OrganizationInvitation, error) {
	return s.q.ListOpenOrganizationInvitations(ctx, orgID)
}

// RevokeInvitation cancels a pending invitation.
func (s *Service) RevokeInvitation(ctx context.Context, orgID, invitationID uuid.UUID) error {
	n, err := s.q.RevokeOrganizationInvitation(ctx, store.RevokeOrganizationInvitationParams{ID: invitationID, OrganizationID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInviteInvalid
	}
	return nil
}

// API keys ------------------------------------------------------------------------

// KeyInput describes a new API key.
type KeyInput struct {
	Name        string
	Scopes      []string       // e.g. "invoices:write", "payouts:write"; empty = all
	IPAllowlist []netip.Prefix // empty = any
	ExpiresAt   *time.Time
	CreatedBy   uuid.NullUUID
	Sandbox     bool // sk_test_ vs sk_live_
}

// CreateAPIKey issues a key. The raw key is returned once; only its hash and
// display prefix are stored.
func (s *Service) CreateAPIKey(ctx context.Context, orgID uuid.UUID, in KeyInput) (string, store.ApiKey, error) {
	prefix := "sk_live_"
	if in.Sandbox {
		prefix = "sk_test_"
	}
	raw, err := secrets.Token(prefix)
	if err != nil {
		return "", store.ApiKey{}, err
	}
	if in.Scopes == nil {
		in.Scopes = []string{}
	}
	k, err := s.q.CreateAPIKey(ctx, store.CreateAPIKeyParams{
		OrganizationID: orgID, Name: strings.TrimSpace(in.Name), KeyPrefix: secrets.DisplayPrefix(raw), KeyHash: secrets.Hash(raw),
		Scopes: in.Scopes, IpAllowlist: in.IPAllowlist, CreatedBy: in.CreatedBy, ExpiresAt: in.ExpiresAt,
	})
	if err != nil {
		return "", store.ApiKey{}, err
	}
	return raw, k, nil
}

// APIPrincipal is the resolved bearer of an API key.
type APIPrincipal struct {
	Key          store.ApiKey
	Organization store.Organization
}

// Sandbox reports whether the key is a test key.
func (p APIPrincipal) Sandbox() bool { return strings.HasPrefix(p.Key.KeyPrefix, "sk_test_") }

// HasScope reports whether the key may perform an action. Keys with no scopes
// are unrestricted.
func (p APIPrincipal) HasScope(scope string) bool {
	if len(p.Key.Scopes) == 0 {
		return true
	}
	for _, s := range p.Key.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
}

// AuthenticateAPIKey resolves a raw key, enforcing revocation, expiry, the IP
// allowlist and the organization's status.
func (s *Service) AuthenticateAPIKey(ctx context.Context, raw string, from *netip.Addr) (APIPrincipal, error) {
	if !strings.HasPrefix(raw, "sk_") {
		return APIPrincipal{}, ErrAPIKeyInvalid
	}
	row, err := s.q.GetAPIKeyByHash(ctx, secrets.Hash(raw))
	if err != nil {
		if postgres.IsNotFound(err) {
			return APIPrincipal{}, ErrAPIKeyInvalid
		}
		return APIPrincipal{}, err
	}
	k := row.ApiKey
	if k.RevokedAt != nil || (k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now())) {
		return APIPrincipal{}, ErrAPIKeyInvalid
	}
	if len(k.IpAllowlist) > 0 {
		if from == nil {
			return APIPrincipal{}, ErrAPIKeyIP
		}
		allowed := false
		for _, p := range k.IpAllowlist {
			if p.Contains(*from) {
				allowed = true
				break
			}
		}
		if !allowed {
			return APIPrincipal{}, ErrAPIKeyIP
		}
	}
	if row.Organization.Status != store.OrganizationStatusActive {
		return APIPrincipal{}, ErrOrgInactive
	}
	if k.LastUsedAt == nil || time.Since(*k.LastUsedAt) > time.Minute {
		_ = s.q.TouchAPIKey(ctx, k.ID)
	}
	return APIPrincipal{Key: k, Organization: row.Organization}, nil
}

// APIKeys lists an organization's keys, live ones first.
func (s *Service) APIKeys(ctx context.Context, orgID uuid.UUID) ([]store.ApiKey, error) {
	return s.q.ListAPIKeys(ctx, orgID)
}

// RevokeAPIKey disables a key permanently.
func (s *Service) RevokeAPIKey(ctx context.Context, orgID, keyID uuid.UUID) error {
	n, err := s.q.RevokeAPIKey(ctx, store.RevokeAPIKeyParams{ID: keyID, OrganizationID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAPIKeyInvalid
	}
	return nil
}

// Asset settings --------------------------------------------------------------------

// AssetSetting is what a merchant configured for one asset.
type AssetSetting struct {
	store.OrganizationAsset
	Asset       store.Asset
	NetworkCode string
	NetworkName string
}

// Assets lists the merchant's asset settings.
func (s *Service) Assets(ctx context.Context, orgID uuid.UUID) ([]AssetSetting, error) {
	rows, err := s.q.ListOrganizationAssets(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]AssetSetting, len(rows))
	for i, r := range rows {
		out[i] = AssetSetting{OrganizationAsset: r.OrganizationAsset, Asset: r.Asset, NetworkCode: r.NetworkCode, NetworkName: r.NetworkName}
	}
	return out, nil
}

// EnabledAssetIDs returns the ids of assets the merchant accepts. An empty
// result means "no explicit settings" and callers fall back to the catalog.
func (s *Service) EnabledAssetIDs(ctx context.Context, orgID uuid.UUID) ([]int16, error) {
	rows, err := s.q.ListOrganizationAssets(ctx, orgID)
	if err != nil {
		return nil, err
	}
	ids := make([]int16, 0, len(rows))
	for _, r := range rows {
		if r.OrganizationAsset.IsEnabled && r.Asset.IsEnabled {
			ids = append(ids, r.Asset.ID)
		}
	}
	return ids, nil
}

// SetAsset enables or configures an asset for a merchant.
func (s *Service) SetAsset(ctx context.Context, orgID uuid.UUID, assetID int16, enabled bool, preferredProvider *int16, autoWithdrawTo *string) (store.OrganizationAsset, error) {
	var pref pgtype.Int2
	if preferredProvider != nil {
		pref = pgtype.Int2{Int16: *preferredProvider, Valid: true}
	}
	return s.q.UpsertOrganizationAsset(ctx, store.UpsertOrganizationAssetParams{
		OrganizationID: orgID, AssetID: assetID, IsEnabled: enabled, PreferredProviderID: pref, AutoWithdrawTo: autoWithdrawTo,
	})
}

// AssetSetting returns one asset's settings or ErrNotFound.
func (s *Service) AssetSetting(ctx context.Context, orgID uuid.UUID, assetID int16) (store.OrganizationAsset, error) {
	oa, err := s.q.GetOrganizationAsset(ctx, store.GetOrganizationAssetParams{OrganizationID: orgID, AssetID: assetID})
	return oa, postgres.MapNotFound(err)
}

// RemoveAsset deletes a merchant's setting for an asset.
func (s *Service) RemoveAsset(ctx context.Context, orgID uuid.UUID, assetID int16) error {
	_, err := s.q.DeleteOrganizationAsset(ctx, store.DeleteOrganizationAssetParams{OrganizationID: orgID, AssetID: assetID})
	return err
}

// String helpers for handlers.
func (m Membership) String() string {
	return fmt.Sprintf("%s (%s)", m.Organization.Name, m.Role)
}

// text converts an optional string to the pgtype CHAR(n) columns want.
func text(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}
