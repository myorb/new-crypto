// Package identity owns who a user is: accounts, passwords, sessions, MFA and
// the login audit. It knows nothing about merchants; org membership lives in
// internal/org.
package identity

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"templ-app/internal/postgres"
	"templ-app/internal/secrets"
	"templ-app/internal/store"
)

var (
	ErrEmailTaken         = errors.New("identity: email already registered")
	ErrInvalidCredentials = errors.New("identity: invalid email or password")
	ErrUserSuspended      = errors.New("identity: account is suspended")
	ErrTooManyAttempts    = errors.New("identity: too many failed attempts, try again later")
	ErrWeakPassword       = errors.New("identity: password must be at least 10 characters")
	ErrSessionInvalid     = errors.New("identity: session is invalid or expired")
	ErrMFARequired        = errors.New("identity: second factor required")
	ErrInvalidCode        = errors.New("identity: invalid code")
	ErrNoPassword         = errors.New("identity: account has no password (social login only)")
	ErrEncryptionMissing  = errors.New("identity: encryption key required for MFA")
)

// Options tune the service.
type Options struct {
	SessionTTL      time.Duration // default 30 days
	LockoutWindow   time.Duration // default 15 min
	LockoutAttempts int64         // default 10
	BcryptCost      int           // default bcrypt.DefaultCost
	Issuer          string        // shown in authenticator apps; default "Payments"
}

// Service is the identity domain.
type Service struct {
	q    *store.Queries
	pool *pgxpool.Pool
	keys *secrets.Keyring // may be nil: MFA enrolment then returns ErrEncryptionMissing
	opts Options
}

// New builds the service.
func New(pool *pgxpool.Pool, keys *secrets.Keyring, opts Options) *Service {
	if opts.SessionTTL == 0 {
		opts.SessionTTL = 30 * 24 * time.Hour
	}
	if opts.LockoutWindow == 0 {
		opts.LockoutWindow = 15 * time.Minute
	}
	if opts.LockoutAttempts == 0 {
		opts.LockoutAttempts = 10
	}
	if opts.BcryptCost == 0 {
		opts.BcryptCost = bcrypt.DefaultCost
	}
	if opts.Issuer == "" {
		opts.Issuer = "Payments"
	}
	return &Service{q: store.New(pool), pool: pool, keys: keys, opts: opts}
}

// WithTx binds the service to a transaction.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{q: s.q.WithTx(tx), keys: s.keys, opts: s.opts}
}

// Client describes where a request came from, for sessions and the login log.
type Client struct {
	IP        *netip.Addr
	UserAgent string
}

func (c Client) ua() *string {
	if c.UserAgent == "" {
		return nil
	}
	ua := c.UserAgent
	if len(ua) > 512 {
		ua = ua[:512]
	}
	return &ua
}

// Accounts --------------------------------------------------------------------

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Register creates a password account.
func (s *Service) Register(ctx context.Context, email, password, fullName string) (store.User, error) {
	email = normalizeEmail(email)
	if !strings.Contains(email, "@") {
		return store.User{}, errors.New("identity: invalid email")
	}
	if len(password) < 10 {
		return store.User{}, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.opts.BcryptCost)
	if err != nil {
		return store.User{}, err
	}
	h := string(hash)
	u, err := s.q.CreateUser(ctx, store.CreateUserParams{Email: email, PasswordHash: &h, FullName: strings.TrimSpace(fullName)})
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return store.User{}, ErrEmailTaken
		}
		return store.User{}, err
	}
	return u, nil
}

// User returns a user by id.
func (s *Service) User(ctx context.Context, id uuid.UUID) (store.User, error) {
	u, err := s.q.GetUserByID(ctx, id)
	return u, postgres.MapNotFound(err)
}

// UserByEmail returns a user by email.
func (s *Service) UserByEmail(ctx context.Context, email string) (store.User, error) {
	u, err := s.q.GetUserByEmail(ctx, normalizeEmail(email))
	return u, postgres.MapNotFound(err)
}

// UpdateProfile changes display fields.
func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, fullName string, avatarURL *string) (store.User, error) {
	return s.q.UpdateUserProfile(ctx, store.UpdateUserProfileParams{ID: id, FullName: strings.TrimSpace(fullName), AvatarUrl: avatarURL})
}

// Authenticate checks a password and records the attempt. It enforces a
// sliding lockout per email / IP. A successful result still needs a session
// (CreateSession) and, when the user requires MFA, a verified challenge.
func (s *Service) Authenticate(ctx context.Context, email, password string, client Client) (store.User, error) {
	email = normalizeEmail(email)
	ip := client.IP
	if ip == nil {
		z := netip.MustParseAddr("0.0.0.0")
		ip = &z
	}
	failed, err := s.q.CountRecentFailedLogins(ctx, store.CountRecentFailedLoginsParams{
		WindowSeconds: s.opts.LockoutWindow.Seconds(), Email: &email, IpAddress: ip,
	})
	if err != nil {
		return store.User{}, err
	}
	if failed >= s.opts.LockoutAttempts {
		return store.User{}, ErrTooManyAttempts
	}

	u, err := s.q.GetUserByEmail(ctx, email)
	record := func(ok bool, reason string, userID uuid.NullUUID) {
		var r *string
		if reason != "" {
			r = &reason
		}
		_ = s.q.RecordLoginAttempt(ctx, store.RecordLoginAttemptParams{
			Email: &email, UserID: userID, IpAddress: *ip, UserAgent: client.ua(), Success: ok, FailureReason: r,
		})
	}
	if err != nil {
		if postgres.IsNotFound(err) {
			// Burn the same time as a real check so enumeration is harder.
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOa4Yl4K7cfjTLVn0G2bqA6YQQxJ4Bl4W"), []byte(password))
			record(false, "unknown_email", uuid.NullUUID{})
			return store.User{}, ErrInvalidCredentials
		}
		return store.User{}, err
	}
	uid := uuid.NullUUID{UUID: u.ID, Valid: true}
	if u.PasswordHash == nil {
		record(false, "no_password", uid)
		return store.User{}, ErrNoPassword
	}
	if bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(password)) != nil {
		record(false, "bad_password", uid)
		return store.User{}, ErrInvalidCredentials
	}
	if u.Status != store.UserStatusActive {
		record(false, "status_"+string(u.Status), uid)
		return store.User{}, ErrUserSuspended
	}
	record(true, "", uid)
	_ = s.q.SetUserLastLogin(ctx, u.ID)
	return u, nil
}

// ChangePassword verifies the current password and sets a new one, revoking
// every other session.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, next string, keepSession uuid.UUID) error {
	u, err := s.User(ctx, userID)
	if err != nil {
		return err
	}
	if u.PasswordHash != nil && bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(current)) != nil {
		return ErrInvalidCredentials
	}
	if len(next) < 10 {
		return ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), s.opts.BcryptCost)
	if err != nil {
		return err
	}
	h := string(hash)
	if err := s.q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: userID, PasswordHash: &h}); err != nil {
		return err
	}
	_, err = s.q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: userID, KeepSessionID: uuid.NullUUID{UUID: keepSession, Valid: keepSession != uuid.Nil}})
	return err
}

// Sessions --------------------------------------------------------------------

// Principal is the resolved bearer of a session token.
type Principal struct {
	Session store.Session
	User    store.User
}

// MFASatisfied reports whether the session may access MFA-gated resources.
func (p Principal) MFASatisfied() bool {
	return !p.User.MfaRequired || p.Session.MfaVerifiedAt != nil
}

// CreateSession issues a session token. The raw token is returned once and
// only its hash is stored. identityID is set for social logins.
func (s *Service) CreateSession(ctx context.Context, userID uuid.UUID, identityID uuid.NullUUID, client Client) (string, store.Session, error) {
	token, err := secrets.Token("sess_")
	if err != nil {
		return "", store.Session{}, err
	}
	sess, err := s.q.CreateSession(ctx, store.CreateSessionParams{
		UserID: userID, TokenHash: secrets.Hash(token), IdentityID: identityID,
		IpAddress: client.IP, UserAgent: client.ua(), ExpiresAt: time.Now().Add(s.opts.SessionTTL),
	})
	if err != nil {
		return "", store.Session{}, err
	}
	return token, sess, nil
}

// Resolve turns a bearer token into a Principal and bumps last_seen_at.
func (s *Service) Resolve(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrSessionInvalid
	}
	row, err := s.q.GetSessionByTokenHash(ctx, secrets.Hash(token))
	if err != nil {
		if postgres.IsNotFound(err) {
			return Principal{}, ErrSessionInvalid
		}
		return Principal{}, err
	}
	if row.User.Status != store.UserStatusActive {
		return Principal{}, ErrUserSuspended
	}
	if time.Since(row.Session.LastSeenAt) > time.Minute {
		_ = s.q.TouchSession(ctx, row.Session.ID)
	}
	return Principal{Session: row.Session, User: row.User}, nil
}

// RevokeSession logs one session out.
func (s *Service) RevokeSession(ctx context.Context, sessionID uuid.UUID) error {
	return s.q.RevokeSession(ctx, sessionID)
}

// RevokeOtherSessions logs the user out everywhere except the current session.
func (s *Service) RevokeOtherSessions(ctx context.Context, userID, keep uuid.UUID) (int64, error) {
	return s.q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: userID, KeepSessionID: uuid.NullUUID{UUID: keep, Valid: true}})
}

// Sessions lists the user's live sessions.
func (s *Service) Sessions(ctx context.Context, userID uuid.UUID) ([]store.Session, error) {
	return s.q.ListUserSessions(ctx, userID)
}

// PurgeExpiredSessions is the retention job.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	return s.q.DeleteExpiredSessions(ctx)
}

// MFA -------------------------------------------------------------------------

// TOTPEnrolment is what the UI needs to show a QR code.
type TOTPEnrolment struct {
	Method store.UserMfaMethod
	Secret string // base32, shown once
	URI    string // otpauth://
}

// EnrolTOTP creates an unverified authenticator method. Call ConfirmTOTP with
// a code from the app to activate it.
func (s *Service) EnrolTOTP(ctx context.Context, userID uuid.UUID, label string) (TOTPEnrolment, error) {
	if s.keys == nil {
		return TOTPEnrolment{}, ErrEncryptionMissing
	}
	u, err := s.User(ctx, userID)
	if err != nil {
		return TOTPEnrolment{}, err
	}
	secret, err := secrets.TOTPSecret()
	if err != nil {
		return TOTPEnrolment{}, err
	}
	enc, err := s.keys.EncryptString(secret)
	if err != nil {
		return TOTPEnrolment{}, err
	}
	if label == "" {
		label = "Authenticator app"
	}
	m, err := s.q.CreateTOTPMethod(ctx, store.CreateTOTPMethodParams{UserID: userID, Label: label, TotpSecretEnc: enc})
	if err != nil {
		return TOTPEnrolment{}, err
	}
	return TOTPEnrolment{Method: m, Secret: secret, URI: TOTPURI(s.opts.Issuer, u.Email, secret)}, nil
}

// ConfirmTOTP verifies the first code, activates the method as primary and
// returns fresh recovery codes (shown once).
func (s *Service) ConfirmTOTP(ctx context.Context, userID, methodID uuid.UUID, code string) ([]string, error) {
	if err := s.checkTOTP(ctx, userID, methodID, code); err != nil {
		return nil, err
	}
	var codes []string
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.MarkMFAMethodVerified(ctx, store.MarkMFAMethodVerifiedParams{ID: methodID, UserID: userID, IsPrimary: true}); err != nil {
			return err
		}
		if err := q.SetUserMFARequired(ctx, store.SetUserMFARequiredParams{ID: userID, MfaRequired: true}); err != nil {
			return err
		}
		var err error
		codes, err = s.regenerateRecoveryCodes(ctx, q, userID)
		return err
	})
	return codes, err
}

// VerifyTOTP checks a login code and marks the session as MFA-verified.
func (s *Service) VerifyTOTP(ctx context.Context, userID, sessionID uuid.UUID, code string) error {
	methods, err := s.q.ListUserMFAMethods(ctx, userID)
	if err != nil {
		return err
	}
	for _, m := range methods {
		if m.Type != store.MfaMethodTypeTotp || m.VerifiedAt == nil {
			continue
		}
		if err := s.checkTOTP(ctx, userID, m.ID, code); err == nil {
			_ = s.q.TouchMFAMethod(ctx, m.ID)
			return s.q.SetSessionMFAVerified(ctx, sessionID)
		}
	}
	return ErrInvalidCode
}

func (s *Service) checkTOTP(ctx context.Context, userID, methodID uuid.UUID, code string) error {
	if s.keys == nil {
		return ErrEncryptionMissing
	}
	m, err := s.q.GetMFAMethod(ctx, store.GetMFAMethodParams{ID: methodID, UserID: userID})
	if err != nil {
		return postgres.MapNotFound(err)
	}
	if m.Type != store.MfaMethodTypeTotp || m.TotpSecretEnc == nil {
		return ErrInvalidCode
	}
	secret, err := s.keys.DecryptString(m.TotpSecretEnc)
	if err != nil {
		return fmt.Errorf("identity: decrypt totp seed: %w", err)
	}
	if !totpValid(secret, code, time.Now()) {
		return ErrInvalidCode
	}
	return nil
}

// UseRecoveryCode consumes a recovery code and marks the session verified.
func (s *Service) UseRecoveryCode(ctx context.Context, userID, sessionID uuid.UUID, code string) error {
	n, err := s.q.UseRecoveryCode(ctx, store.UseRecoveryCodeParams{UserID: userID, CodeHash: secrets.Hash(secrets.NormalizeRecoveryCode(code))})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvalidCode
	}
	return s.q.SetSessionMFAVerified(ctx, sessionID)
}

// RegenerateRecoveryCodes replaces the whole set and returns the new codes.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	return postgres.InTxRet(ctx, s.pool, func(tx pgx.Tx) ([]string, error) {
		return s.regenerateRecoveryCodes(ctx, s.q.WithTx(tx), userID)
	})
}

func (s *Service) regenerateRecoveryCodes(ctx context.Context, q *store.Queries, userID uuid.UUID) ([]string, error) {
	if _, err := q.DeleteUserRecoveryCodes(ctx, userID); err != nil {
		return nil, err
	}
	codes := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		c, err := secrets.RecoveryCode()
		if err != nil {
			return nil, err
		}
		if err := q.CreateRecoveryCode(ctx, store.CreateRecoveryCodeParams{UserID: userID, CodeHash: secrets.Hash(secrets.NormalizeRecoveryCode(c))}); err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, nil
}

// MFAMethods lists a user's second factors.
func (s *Service) MFAMethods(ctx context.Context, userID uuid.UUID) ([]store.UserMfaMethod, error) {
	return s.q.ListUserMFAMethods(ctx, userID)
}

// RemoveMFAMethod deletes a method; when none verified remain, MFA is no
// longer required for the user.
func (s *Service) RemoveMFAMethod(ctx context.Context, userID, methodID uuid.UUID) error {
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.DeleteMFAMethod(ctx, store.DeleteMFAMethodParams{ID: methodID, UserID: userID}); err != nil {
			return err
		}
		methods, err := q.ListUserMFAMethods(ctx, userID)
		if err != nil {
			return err
		}
		for _, m := range methods {
			if m.VerifiedAt != nil {
				return nil
			}
		}
		if _, err := q.DeleteUserRecoveryCodes(ctx, userID); err != nil {
			return err
		}
		return q.SetUserMFARequired(ctx, store.SetUserMFARequiredParams{ID: userID, MfaRequired: false})
	})
}

// Social logins ----------------------------------------------------------------

// SocialProfile is what an OAuth callback yields after token exchange.
type SocialProfile struct {
	Provider       store.AuthProvider
	ProviderUserID string
	Email          string
	EmailVerified  bool
	DisplayName    string
	AvatarURL      string
	Raw            []byte
}

// LoginSocial finds or creates the user behind a social profile and returns
// the linked identity. A verified email that matches an existing account
// links to it; otherwise a passwordless account is created.
func (s *Service) LoginSocial(ctx context.Context, p SocialProfile) (store.User, store.UserIdentity, error) {
	return postgres.InTxRet2(ctx, s.pool, func(tx pgx.Tx) (store.User, store.UserIdentity, error) {
		q := s.q.WithTx(tx)
		var user store.User
		if id, err := q.GetUserIdentity(ctx, store.GetUserIdentityParams{Provider: p.Provider, ProviderUserID: p.ProviderUserID}); err == nil {
			user, err = q.GetUserByID(ctx, id.UserID)
			if err != nil {
				return store.User{}, store.UserIdentity{}, err
			}
		} else if !postgres.IsNotFound(err) {
			return store.User{}, store.UserIdentity{}, err
		} else {
			email := normalizeEmail(p.Email)
			if email != "" && p.EmailVerified {
				if u, err := q.GetUserByEmail(ctx, email); err == nil {
					user = u
				} else if !postgres.IsNotFound(err) {
					return store.User{}, store.UserIdentity{}, err
				}
			}
			if user.ID == uuid.Nil {
				if email == "" {
					return store.User{}, store.UserIdentity{}, errors.New("identity: social profile has no email")
				}
				u, err := q.CreateUser(ctx, store.CreateUserParams{Email: email, FullName: p.DisplayName})
				if err != nil {
					if postgres.IsUniqueViolation(err) {
						return store.User{}, store.UserIdentity{}, errors.New("identity: email belongs to an account whose email is not verified by this provider")
					}
					return store.User{}, store.UserIdentity{}, err
				}
				if p.EmailVerified {
					_ = q.MarkUserEmailVerified(ctx, u.ID)
				}
				user = u
			}
		}
		if user.Status != store.UserStatusActive {
			return store.User{}, store.UserIdentity{}, ErrUserSuspended
		}
		raw := p.Raw
		if raw == nil {
			raw = []byte("{}")
		}
		var email, name, avatar *string
		if p.Email != "" {
			e := normalizeEmail(p.Email)
			email = &e
		}
		if p.DisplayName != "" {
			name = &p.DisplayName
		}
		if p.AvatarURL != "" {
			avatar = &p.AvatarURL
		}
		ident, err := q.UpsertUserIdentity(ctx, store.UpsertUserIdentityParams{
			UserID: user.ID, Provider: p.Provider, ProviderUserID: p.ProviderUserID, ProviderEmail: email,
			EmailVerified: p.EmailVerified, DisplayName: name, AvatarUrl: avatar, RawProfile: raw,
		})
		if err != nil {
			return store.User{}, store.UserIdentity{}, err
		}
		_ = q.SetUserLastLogin(ctx, user.ID)
		return user, ident, nil
	})
}

// Identities lists a user's linked social logins.
func (s *Service) Identities(ctx context.Context, userID uuid.UUID) ([]store.UserIdentity, error) {
	return s.q.ListUserIdentities(ctx, userID)
}

// UnlinkIdentity removes a social login; refused when it would leave the
// account with no way to sign in.
func (s *Service) UnlinkIdentity(ctx context.Context, userID, identityID uuid.UUID) error {
	u, err := s.User(ctx, userID)
	if err != nil {
		return err
	}
	if u.PasswordHash == nil {
		ids, err := s.q.ListUserIdentities(ctx, userID)
		if err != nil {
			return err
		}
		if len(ids) <= 1 {
			return errors.New("identity: set a password before unlinking the last social login")
		}
	}
	_, err = s.q.DeleteUserIdentity(ctx, store.DeleteUserIdentityParams{ID: identityID, UserID: userID})
	return err
}
