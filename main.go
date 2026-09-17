package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"templ-app/components"
	"templ-app/pages"
)

func main() {
	mux := http.NewServeMux()
	setupAssetsRoutes(mux)
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
			templ.Handler(page(isSandbox(r))).ServeHTTP(w, r)
		})
	}
	// Settings pages take a ?tab= section, e.g. /dashboard/settings?tab=billing.
	tabbedPages := map[string]func(sandbox bool, tab string) templ.Component{
		"/dashboard/settings": pages.Settings,
		"/dashboard/account":  pages.Account,
	}
	for route, page := range tabbedPages {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(page(isSandbox(r), r.URL.Query().Get("tab"))).ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /mode", setMode)

	ln, err := listen()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Server is running on http://localhost:%d\n", ln.Addr().(*net.TCPAddr).Port)
	if err := http.Serve(ln, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// modeCookie stores whether the merchant is viewing live or sandbox data.
const modeCookie = "pc_mode"

func isSandbox(r *http.Request) bool {
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

// listen binds the port. An explicit PORT binds exactly (the templ proxy in
// the Taskfile points at it, so silently moving would break hot reload);
// without PORT it walks from 8090 to the next free port like node dev
// servers do.
func listen() (net.Listener, error) {
	if env := os.Getenv("PORT"); env != "" {
		port, err := strconv.Atoi(env)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT %q", env)
		}
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return nil, fmt.Errorf("port %d is busy - stop the other app or run with another port, e.g. 'task dev PORT=%d'", port, port+1)
		}
		return ln, nil
	}
	for port := 8090; port < 8100; port++ {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("no free port found from 8090 upwards")
}

func setupAssetsRoutes(mux *http.ServeMux) {
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
