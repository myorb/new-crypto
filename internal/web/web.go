// Package web serves the dashboard and public pages. It is a delivery layer:
// it maps HTTP to domain services and templ components and holds no
// business rules. Each dashboard page has a view assembler (view_*.go) that
// turns service results into the block's view model; the templates in
// blocks/ are untouched.
//
// Without an *app.App (no DATABASE_URL) the server falls back to the fixture
// pages so the UI can still be developed on its own.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/a-h/templ"

	"templ-app/components"
	"templ-app/internal/app"
	"templ-app/internal/store"
	"templ-app/pages"
)

// Server is the HTTP front end.
type Server struct {
	app *app.App
	log *slog.Logger
}

// New builds the server. a may be nil (fixture mode).
func New(a *app.App) *Server {
	log := slog.Default()
	if a != nil {
		log = a.Log
	}
	return &Server{app: a, log: log}
}

func (s *Server) production() bool { return s.app != nil && s.app.Config.Production() }

// Handler returns the routed mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.assets(mux)
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /", templ.Handler(pages.Home()))
	mux.HandleFunc("GET /mode", setMode)

	if s.app == nil {
		s.fixtureRoutes(mux)
		return mux
	}

	// auth
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("GET /signup", s.signupPage)
	mux.HandleFunc("POST /signup", s.signupPost)
	mux.HandleFunc("GET /verify", s.verifyPage)
	mux.HandleFunc("POST /verify", s.verifyPost)
	mux.HandleFunc("GET /2fa/setup", s.setupPage)
	mux.HandleFunc("POST /2fa/setup", s.setupPost)
	mux.HandleFunc("GET /forgot-password", s.forgotPage)
	mux.HandleFunc("POST /forgot-password", s.forgotPost)
	mux.HandleFunc("GET /logout", s.logout)
	mux.HandleFunc("GET /org/switch", s.switchOrg)

	// dashboard
	mux.HandleFunc("GET /dashboard", s.requireViewer(s.overview))
	mux.HandleFunc("GET /dashboard/payments", s.requireViewer(s.paymentsPage))
	mux.HandleFunc("GET /dashboard/links", s.requireViewer(s.linksPage))
	mux.HandleFunc("GET /dashboard/payouts", s.requireViewer(s.payoutsPage))
	mux.HandleFunc("GET /dashboard/wallets", s.requireViewer(s.walletsPage))
	mux.HandleFunc("GET /dashboard/customers", s.requireViewer(s.customersPage))
	mux.HandleFunc("POST /dashboard/customers", s.requireRole(customersPath, store.OrganizationRoleFinance, s.createCustomer))
	mux.HandleFunc("POST /dashboard/customers/status", s.requireRole(customersPath, store.OrganizationRoleFinance, s.customerStatus))
	mux.HandleFunc("POST /dashboard/links", s.requireRole(linksPath, store.OrganizationRoleFinance, s.createLink))
	mux.HandleFunc("POST /dashboard/links/status", s.requireRole(linksPath, store.OrganizationRoleFinance, s.linkStatus))
	mux.HandleFunc("GET /dashboard/api-keys", s.requireViewer(s.apiKeysPage))
	mux.HandleFunc("GET /dashboard/webhooks", s.requireViewer(s.webhooksPage))
	mux.HandleFunc("GET /dashboard/settings", s.requireViewer(s.settingsPage))
	mux.HandleFunc("GET /dashboard/account", s.requireViewer(s.accountPage))

	if !s.production() {
		mux.HandleFunc("GET /dev/simulate", s.requireViewer(s.devSimulate))
		mux.HandleFunc("GET /dev/fixtures", s.requireViewer(s.devFixturesHandler))
		mux.HandleFunc("GET /dev/payout", s.requireViewer(s.devPayout))
	}
	return mux
}

// fixtureRoutes serve the design previews when there is no database.
func (s *Server) fixtureRoutes(mux *http.ServeMux) {
	dashboardPages := map[string]func(sandbox bool) templ.Component{
		"/dashboard":           pages.Dashboard,
		"/dashboard/payments":  pages.Payments,
		"/dashboard/links":     pages.Links,
		"/dashboard/payouts":   pages.Payouts,
		"/dashboard/wallets":   pages.Wallets,
		"/dashboard/customers": pages.Customers,
		"/dashboard/api-keys":  pages.APIKeys,
		"/dashboard/webhooks":  pages.Webhooks,
	}
	for route, page := range dashboardPages {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(page(IsSandbox(r))).ServeHTTP(w, r)
		})
	}
	tabbedPages := map[string]func(sandbox bool, tab string) templ.Component{
		"/dashboard/settings": pages.Settings,
		"/dashboard/account":  pages.Account,
	}
	for route, page := range tabbedPages {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(page(IsSandbox(r), r.URL.Query().Get("tab"))).ServeHTTP(w, r)
		})
	}
	mux.Handle("GET /login", templ.Handler(pages.Login("", "Fixture mode: set DATABASE_URL to sign in", "/dashboard")))
	mux.Handle("GET /signup", templ.Handler(pages.Signup("")))
	mux.Handle("GET /forgot-password", templ.Handler(pages.ForgotPassword()))
}

// health reports process liveness and database reachability.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status := map[string]string{"status": "ok", "database": "absent"}
	code := http.StatusOK
	if s.app != nil {
		if err := s.app.Ping(ctx); err != nil {
			status["status"], status["database"] = "degraded", "unreachable"
			code = http.StatusServiceUnavailable
		} else {
			status["database"] = "ok"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

// modeCookie stores whether the merchant is viewing live or sandbox data.
const modeCookie = "pc_mode"

// IsSandbox reports whether the request is in sandbox mode.
func IsSandbox(r *http.Request) bool {
	c, err := r.Cookie(modeCookie)
	return err == nil && c.Value == "sandbox"
}

// setMode switches between live and sandbox (GET /mode?set=sandbox&next=/dashboard)
// and redirects back to the page the switch was on.
func setMode(w http.ResponseWriter, r *http.Request) {
	mode := "live"
	if r.URL.Query().Get("set") == "sandbox" {
		mode = "sandbox"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     modeCookie,
		Value:    mode,
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 365,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next"), "/dashboard"), http.StatusSeeOther)
}

// safeNext only allows same-site relative redirects.
func safeNext(next, def string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return def
	}
	return next
}

func (s *Server) assets(mux *http.ServeMux) {
	assetHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("GO_ENV") != "production" {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000")
		}
		http.FileServer(http.Dir("./assets")).ServeHTTP(w, r)
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", assetHandler))

	// The component JS bundle (hashed name plus the shadcn-templ.js alias).
	mux.Handle("GET /components/{bundle}", components.ScriptsHandler())
}

// ClientIP extracts the caller's address for sessions, API keys and the
// login log. It trusts X-Forwarded-For only when TRUST_PROXY is set, since
// the app runs behind Render's proxy in production and directly in dev.
func ClientIP(r *http.Request) *netip.Addr {
	if os.Getenv("TRUST_PROXY") != "" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if ip, err := netip.ParseAddr(first); err == nil {
				return &ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return &ip
	}
	return nil
}
