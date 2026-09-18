package web

import (
	"context"
	"html"
	"io"
	"net/http"
	"time"

	"github.com/a-h/templ"

	"templ-app/layouts"
)

// compose renders body as the children of layout, the Go equivalent of
// `@layout { @body }` in a template.
func compose(layout, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return layout.Render(templ.WithChildren(ctx, body), w)
	})
}

// render writes a component as HTML.
func (s *Server) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		s.log.ErrorContext(r.Context(), "render failed", "path", r.URL.Path, "err", err)
	}
}

// renderDashboard wraps a block in the sidebar shell for the signed-in viewer.
func (s *Server) renderDashboard(w http.ResponseWriter, r *http.Request, v *viewer, title, active string, body templ.Component) {
	s.render(w, r, compose(layouts.Dashboard(s.shell(v, title, active)), body))
}

// shell builds the sidebar props: current organization, the switcher list
// and the signed-in user.
func (s *Server) shell(v *viewer, title, active string) layouts.DashboardProps {
	o := v.Org.Organization
	orgs := make([]layouts.Org, len(v.Memberships))
	for i, m := range v.Memberships {
		orgs[i] = layouts.Org{
			Name: m.Organization.Name, Plan: planLabel(m.Organization.Status), Initials: initials(m.Organization.Name),
			Href: "/org/switch?id=" + m.Organization.ID.String() + "&next=" + active, Current: m.Organization.ID == o.ID,
		}
	}
	name := v.User.FullName
	if name == "" {
		name = v.User.Email
	}
	return layouts.DashboardProps{
		Title:      title,
		Subtitle:   time.Now().Format("Mon, Jan 2"),
		ActivePath: active,
		Merchant:   o.Name,
		Initials:   initials(o.Name),
		Plan:       planLabel(o.Status),
		Orgs:       orgs,
		User: layouts.User{
			Name: name, Email: v.User.Email, Initials: initials(name), Role: roleLabel(v.Org.Role),
		},
		Sandbox: v.Sandbox,
	}
}

// fail logs the error and shows a generic 500. Details never reach the browser.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
}

// message renders a small notice inside the auth layout (used when a
// signed-in user has no organization yet).
func (s *Server) message(w http.ResponseWriter, r *http.Request, title, heading, body, linkHref, linkText string) {
	inner := templ.Raw(`<div class="mb-6 flex flex-col gap-1.5"><h2 class="text-2xl font-semibold tracking-tight">` + html.EscapeString(heading) +
		`</h2><p class="text-muted-foreground text-sm">` + html.EscapeString(body) + `</p></div>` +
		`<a href="` + html.EscapeString(linkHref) + `" class="bg-primary text-primary-foreground inline-flex h-9 w-full items-center justify-center rounded-md px-4 text-sm font-medium">` + html.EscapeString(linkText) + `</a>`)
	s.render(w, r, compose(layouts.Auth(title), inner))
}
