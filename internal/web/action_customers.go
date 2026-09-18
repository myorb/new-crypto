package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"templ-app/blocks/dashboard"
	"templ-app/internal/customers"
	"templ-app/internal/store"
)

const customersPath = "/dashboard/customers"

// requireRole gates a form post on the viewer's role and on the request
// coming from our own form. It answers with a redirect, never a 403 page,
// so the merchant lands back on the page with the reason.
func (s *Server) requireRole(path string, least store.OrganizationRole, next func(http.ResponseWriter, *http.Request, *viewer)) http.HandlerFunc {
	return s.requireViewer(func(w http.ResponseWriter, r *http.Request, v *viewer) {
		if !sameOrigin(r) {
			redirectErr(w, r, path, "The form expired, please try again.", nil)
			return
		}
		if err := r.ParseForm(); err != nil {
			redirectErr(w, r, path, "Invalid form.", nil)
			return
		}
		if err := s.app.Org.RequireRole(r.Context(), v.orgID(), v.User.ID, least); err != nil {
			redirectErr(w, r, path, "Your role cannot change this.", nil)
			return
		}
		next(w, r, v)
	})
}

// redirectNotice sends the merchant back to a page with a confirmation.
func redirectNotice(w http.ResponseWriter, r *http.Request, path, msg string) {
	http.Redirect(w, r, path+"?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

// createCustomer handles the "Add customer" dialog.
func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := r.PostForm
	country := f.Get("country")
	if country == "none" { // the dialog's "Not set" entry
		country = ""
	}
	in := customers.Input{
		Email:       optional(f.Get("email")),
		Name:        optional(f.Get("name")),
		CountryCode: optional(country),
		ExternalID:  optional(f.Get("external_id")),
	}
	c, err := s.app.Customers.Create(r.Context(), v.orgID(), in)
	if err != nil {
		switch {
		case errors.Is(err, customers.ErrDuplicate):
			redirectErr(w, r, customersPath, "A customer with that email or reference already exists.", nil)
		case errors.Is(err, customers.ErrNoIdentity):
			redirectErr(w, r, customersPath, "Enter an email address or your own reference.", nil)
		case errors.Is(err, customers.ErrBadEmail):
			redirectErr(w, r, customersPath, "That email address is not valid.", nil)
		case errors.Is(err, customers.ErrBadCountry):
			redirectErr(w, r, customersPath, "Pick a country from the list.", nil)
		default:
			s.fail(w, r, err)
		}
		return
	}
	name := "Customer"
	if c.Name != nil && *c.Name != "" {
		name = *c.Name
	} else if c.Email != nil {
		name = *c.Email
	}
	redirectNotice(w, r, customersPath, name+" was added.")
}

// customerStatus blocks or unblocks a customer from the row menu.
func (s *Server) customerStatus(w http.ResponseWriter, r *http.Request, v *viewer) {
	id, err := uuid.Parse(r.PostForm.Get("id"))
	if err != nil {
		redirectErr(w, r, customersPath, "That customer no longer exists.", nil)
		return
	}
	block := r.PostForm.Get("status") == string(store.CustomerStatusBlocked)
	var c store.Customer
	if block {
		reason := optional(r.PostForm.Get("reason"))
		c, err = s.app.Customers.Block(r.Context(), v.orgID(), id, reason)
	} else {
		c, err = s.app.Customers.Unblock(r.Context(), v.orgID(), id)
	}
	if err != nil {
		if errors.Is(err, customers.ErrNotFound) {
			redirectErr(w, r, customersPath, "That customer no longer exists.", nil)
			return
		}
		s.fail(w, r, err)
		return
	}
	who := "The customer"
	if c.Email != nil {
		who = *c.Email
	}
	if block {
		redirectNotice(w, r, customersPath, who+" is blocked and cannot open new invoices.")
		return
	}
	redirectNotice(w, r, customersPath, who+" can pay again.")
}

// optional trims a form value and returns nil when it is empty, which is what
// the services expect for "not given".
func optional(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

// countryChoices lists the countries the add-customer dialog offers. ISO
// 3166-1 alpha-2, the same set the organization profile uses.
func countryChoices() []dashboard.Option {
	return []dashboard.Option{
		{Value: "DE", Label: "Germany"}, {Value: "AT", Label: "Austria"}, {Value: "CH", Label: "Switzerland"},
		{Value: "FR", Label: "France"}, {Value: "ES", Label: "Spain"}, {Value: "IT", Label: "Italy"},
		{Value: "NL", Label: "Netherlands"}, {Value: "SE", Label: "Sweden"}, {Value: "PL", Label: "Poland"},
		{Value: "GB", Label: "United Kingdom"}, {Value: "IE", Label: "Ireland"}, {Value: "US", Label: "United States"},
		{Value: "CA", Label: "Canada"}, {Value: "BR", Label: "Brazil"}, {Value: "NG", Label: "Nigeria"},
		{Value: "AE", Label: "United Arab Emirates"}, {Value: "IN", Label: "India"}, {Value: "SG", Label: "Singapore"},
		{Value: "JP", Label: "Japan"}, {Value: "AU", Label: "Australia"},
	}
}
