// Package web serves the dashboard and public pages. It is a delivery layer:
// it maps HTTP to domain services and templ pages and holds no business
// rules. The dashboard pages still render fixtures (see blocks/); wiring
// each page to the services is done page by page in handlers here.
package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/a-h/templ"

	"templ-app/components"
	"templ-app/internal/app"
	"templ-app/pages"
)

// Server is the HTTP front end. app may be nil, in which case only the
// fixture-backed pages work and /healthz reports the database as absent.
type Server struct {
	app *app.App
}

// New builds the server.
func New(a *app.App) *Server { return &Server{app: a} }

// Handler returns the routed mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.assets(mux)
	mux.HandleFunc("GET /healthz", s.health)

	mux.Handle("GET /", templ.Handler(pages.Home()))
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
	// Settings pages take a ?tab= section, e.g. /dashboard/settings?tab=billing.
	tabbedPages := map[string]func(sandbox bool, tab string) templ.Component{
		"/dashboard/settings": pages.Settings,
		"/dashboard/account":  pages.Account,
	}
	for route, page := range tabbedPages {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(page(IsSandbox(r), r.URL.Query().Get("tab"))).ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /mode", setMode)
	// Auth screens are frontend-only mocks: forms navigate to the next step
	// client-side. Wire real handlers behind the same paths.
	mux.Handle("GET /login", templ.Handler(pages.Login()))
	mux.Handle("GET /signup", templ.Handler(pages.Signup()))
	mux.Handle("GET /forgot-password", templ.Handler(pages.ForgotPassword()))
	mux.Handle("GET /2fa/setup", templ.Handler(pages.Setup2FA()))
	mux.HandleFunc("GET /verify", func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(pages.Verify(r.URL.Query().Get("method"))).ServeHTTP(w, r)
	})
	return mux
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
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/dashboard"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
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
		return &ip
	}
	return nil
}
