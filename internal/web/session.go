package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"templ-app/blocks/auth"
	"templ-app/internal/identity"
	"templ-app/internal/org"
	"templ-app/internal/postgres"
	"templ-app/internal/store"
	"templ-app/pages"
)

const (
	sessionCookie = "pc_session"
	orgCookie     = "pc_org"
	rememberFor   = 30 * 24 * time.Hour
)

// viewer is the signed-in user in the context of one organization.
type viewer struct {
	identity.Principal
	Memberships []org.Membership
	Org         org.Membership
	Sandbox     bool
}

func (v *viewer) orgID() uuid.UUID { return v.Org.Organization.ID }

func (s *Server) client(r *http.Request) identity.Client {
	return identity.Client{IP: ClientIP(r), UserAgent: r.UserAgent()}
}

// principal resolves the session cookie; (nil, nil) when there is none.
func (s *Server) principal(r *http.Request) (*identity.Principal, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	p, err := s.app.Identity.Resolve(r.Context(), c.Value)
	if err != nil {
		if errors.Is(err, identity.ErrSessionInvalid) || errors.Is(err, identity.ErrUserSuspended) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// viewerFor loads memberships and picks the current organization.
func (s *Server) viewerFor(r *http.Request, p identity.Principal) (*viewer, error) {
	ms, err := s.app.Org.ForUser(r.Context(), p.User.ID)
	if err != nil {
		return nil, err
	}
	v := &viewer{Principal: p, Memberships: ms, Sandbox: IsSandbox(r)}
	if len(ms) == 0 {
		return v, nil
	}
	v.Org = ms[0]
	if c, err := r.Cookie(orgCookie); err == nil {
		if id, err := uuid.Parse(c.Value); err == nil {
			for _, m := range ms {
				if m.Organization.ID == id {
					v.Org = m
					break
				}
			}
		}
	}
	return v, nil
}

// requireViewer gates dashboard pages: session, second factor, membership.
func (s *Server) requireViewer(next func(http.ResponseWriter, *http.Request, *viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.principal(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		here := r.URL.RequestURI()
		if p == nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(here), http.StatusSeeOther)
			return
		}
		if !p.MFASatisfied() {
			http.Redirect(w, r, "/verify?next="+url.QueryEscape(here), http.StatusSeeOther)
			return
		}
		v, err := s.viewerFor(r, *p)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if len(v.Memberships) == 0 {
			s.message(w, r, "No organization", "You are not part of an organization yet",
				"Ask a teammate to invite "+p.User.Email+", or sign out and create a new organization.", "/logout", "Sign out")
			return
		}
		next(w, r, v)
	}
}

func (s *Server) setSession(w http.ResponseWriter, token string, remember bool) {
	c := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.production(), SameSite: http.SameSiteLaxMode}
	if remember {
		c.MaxAge = int(rememberFor.Seconds())
	}
	http.SetCookie(w, c)
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.production(), SameSite: http.SameSiteLaxMode})
}

// sameOrigin is the CSRF check for form posts: the browser's Origin (or
// Referer) must match the host that served the form.
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func redirectErr(w http.ResponseWriter, r *http.Request, path, msg string, extra url.Values) {
	q := url.Values{"error": {msg}}
	for k, vs := range extra {
		for _, v := range vs {
			if v != "" {
				q.Add(k, v)
			}
		}
	}
	http.Redirect(w, r, path+"?"+q.Encode(), http.StatusSeeOther)
}

// Login -------------------------------------------------------------------------------

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if p, _ := s.principal(r); p != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	q := r.URL.Query()
	s.render(w, r, pages.Login(q.Get("error"), q.Get("notice"), safeNext(q.Get("next"), "/dashboard")))
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		redirectErr(w, r, "/login", "The form expired, please try again.", nil)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectErr(w, r, "/login", "Invalid form.", nil)
		return
	}
	next := safeNext(r.PostForm.Get("next"), "/dashboard")
	extra := url.Values{"next": {next}}
	user, err := s.app.Identity.Authenticate(r.Context(), r.PostForm.Get("email"), r.PostForm.Get("password"), s.client(r))
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrTooManyAttempts):
			redirectErr(w, r, "/login", "Too many attempts. Wait a few minutes and try again.", extra)
		case errors.Is(err, identity.ErrUserSuspended):
			redirectErr(w, r, "/login", "This account is suspended.", extra)
		case errors.Is(err, identity.ErrNoPassword):
			redirectErr(w, r, "/login", "This account uses social sign-in.", extra)
		case errors.Is(err, identity.ErrInvalidCredentials):
			redirectErr(w, r, "/login", "Wrong email or password.", extra)
		default:
			s.fail(w, r, err)
		}
		return
	}
	token, _, err := s.app.Identity.CreateSession(r.Context(), user.ID, uuid.NullUUID{}, s.client(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSession(w, token, r.PostForm.Get("remember") != "")
	if user.MfaRequired {
		http.Redirect(w, r, "/verify?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// Signup -------------------------------------------------------------------------------

func (s *Server) signupPage(w http.ResponseWriter, r *http.Request) {
	if p, _ := s.principal(r); p != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	s.render(w, r, pages.Signup(r.URL.Query().Get("error")))
}

func (s *Server) signupPost(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		redirectErr(w, r, "/signup", "The form expired, please try again.", nil)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectErr(w, r, "/signup", "Invalid form.", nil)
		return
	}
	f := r.PostForm
	if f.Get("terms") == "" {
		redirectErr(w, r, "/signup", "Please accept the terms to continue.", nil)
		return
	}
	orgName := strings.TrimSpace(f.Get("org"))
	if orgName == "" {
		redirectErr(w, r, "/signup", "Please enter your company name.", nil)
		return
	}
	user, err := s.app.Identity.Register(r.Context(), f.Get("email"), f.Get("password"), f.Get("name"))
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrEmailTaken):
			redirectErr(w, r, "/signup", "An account with this email already exists.", nil)
		case errors.Is(err, identity.ErrWeakPassword):
			redirectErr(w, r, "/signup", "Use at least 10 characters for your password.", nil)
		default:
			redirectErr(w, r, "/signup", "Please check your details and try again.", nil)
		}
		return
	}
	var country *string
	if c := strings.ToUpper(f.Get("country")); len(c) == 2 {
		country = &c
	}
	var o store.Organization
	slug := slugify(orgName)
	for attempt := 0; attempt < 4; attempt++ {
		candidate := slug
		if attempt > 0 {
			candidate = slug + "-" + uuid.NewString()[:4]
		}
		o, err = s.app.Org.Create(r.Context(), org.CreateInput{Slug: candidate, Name: orgName, CountryCode: country, OwnerID: user.ID})
		if err == nil || !errors.Is(err, org.ErrSlugTaken) {
			break
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.production() {
		// KYB review does not exist in development; let the merchant use the API right away.
		_ = s.app.Org.SetStatus(r.Context(), o.ID, store.OrganizationStatusActive)
	}
	token, _, err := s.app.Identity.CreateSession(r.Context(), user.ID, uuid.NullUUID{}, s.client(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSession(w, token, true)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// Two-factor ----------------------------------------------------------------------------

func (s *Server) verifyPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	q := r.URL.Query()
	next := safeNext(q.Get("next"), "/dashboard")
	if p.MFASatisfied() {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	method := q.Get("method")
	if method != "recovery" {
		method = "app"
	}
	s.render(w, r, pages.Verify(auth.Verify2FA{Email: p.User.Email, Method: method, Error: q.Get("error"), Next: next}))
}

func (s *Server) verifyPost(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !sameOrigin(r) || r.ParseForm() != nil {
		redirectErr(w, r, "/verify", "The form expired, please try again.", nil)
		return
	}
	next := safeNext(r.PostForm.Get("next"), "/dashboard")
	code := strings.TrimSpace(r.PostForm.Get("code"))
	extra := url.Values{"next": {next}}
	if strings.Contains(code, "-") || len(strings.ReplaceAll(code, " ", "")) > 6 {
		err = s.app.Identity.UseRecoveryCode(r.Context(), p.User.ID, p.Session.ID, code)
		extra.Set("method", "recovery")
	} else {
		err = s.app.Identity.VerifyTOTP(r.Context(), p.User.ID, p.Session.ID, code)
	}
	if err != nil {
		if errors.Is(err, identity.ErrInvalidCode) {
			redirectErr(w, r, "/verify", "That code is not valid. Try again.", extra)
			return
		}
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		http.Redirect(w, r, "/login?next=/2fa/setup", http.StatusSeeOther)
		return
	}
	enrol, err := s.app.Identity.EnrolTOTP(r.Context(), p.User.ID, "Authenticator app")
	if err != nil {
		if errors.Is(err, identity.ErrEncryptionMissing) {
			s.message(w, r, "Two-factor unavailable", "Two-factor authentication is not configured",
				"Set APP_ENCRYPTION_KEY on the server to enable authenticator apps.", "/dashboard", "Back to dashboard")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.render(w, r, pages.Setup2FA(s.setupState(p.User.Email, enrol, "")))
}

func (s *Server) setupState(email string, e identity.TOTPEnrolment, errMsg string) auth.Setup2FA {
	return auth.Setup2FA{Email: email, Issuer: s.app.Config.Issuer, Secret: groupSecret(e.Secret), OTPAuth: e.URI, MethodID: e.Method.ID.String(), Error: errMsg}
}

func (s *Server) setupPost(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !sameOrigin(r) || r.ParseForm() != nil {
		http.Redirect(w, r, "/2fa/setup", http.StatusSeeOther)
		return
	}
	methodID, err := uuid.Parse(r.PostForm.Get("method_id"))
	if err != nil {
		http.Redirect(w, r, "/2fa/setup", http.StatusSeeOther)
		return
	}
	codes, err := s.app.Identity.ConfirmTOTP(r.Context(), p.User.ID, methodID, r.PostForm.Get("code"))
	if err != nil {
		if errors.Is(err, identity.ErrInvalidCode) || postgres.IsNotFound(err) {
			enrol, rerr := s.app.Identity.Enrolment(r.Context(), p.User.ID, methodID)
			if rerr != nil {
				http.Redirect(w, r, "/2fa/setup", http.StatusSeeOther)
				return
			}
			s.render(w, r, pages.Setup2FA(s.setupState(p.User.Email, enrol, "That code did not match. Check the time on your phone and try again.")))
			return
		}
		s.fail(w, r, err)
		return
	}
	_ = s.app.Identity.MarkSessionVerified(r.Context(), p.Session.ID)
	s.render(w, r, pages.RecoveryCodes(codes, "/dashboard/account?tab=security"))
}

// Password reset, logout, org switch --------------------------------------------------------

func (s *Server) forgotPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, pages.ForgotPassword())
}

// forgotPost acknowledges the request. Sending the reset email needs a mail
// provider, which is not wired; the response is the same either way so the
// form cannot be used to probe which emails exist.
func (s *Server) forgotPost(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?notice="+url.QueryEscape("If an account exists for that email, a reset link is on its way."), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if p, _ := s.principal(r); p != nil {
		_ = s.app.Identity.RevokeSession(r.Context(), p.Session.ID)
	}
	s.clearCookie(w, sessionCookie)
	s.clearCookie(w, orgCookie)
	http.Redirect(w, r, "/login?notice="+url.QueryEscape("You have been signed out."), http.StatusSeeOther)
}

func (s *Server) switchOrg(w http.ResponseWriter, r *http.Request) {
	p, err := s.principal(r)
	if err != nil || p == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	id, err := uuid.Parse(r.URL.Query().Get("id"))
	if err == nil {
		if _, err := s.app.Org.Role(r.Context(), id, p.User.ID); err == nil {
			http.SetCookie(w, &http.Cookie{Name: orgCookie, Value: id.String(), Path: "/", MaxAge: int(rememberFor.Seconds()), HttpOnly: true, Secure: s.production(), SameSite: http.SameSiteLaxMode})
		}
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next"), "/dashboard"), http.StatusSeeOther)
}

func slugify(name string) string {
	var b strings.Builder
	lastDash := true
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) < 2 {
		slug = "org-" + uuid.NewString()[:6]
	}
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	return slug
}

// groupSecret renders a base32 seed in blocks of four for manual entry.
func groupSecret(secret string) string {
	var b strings.Builder
	for i, c := range secret {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}
