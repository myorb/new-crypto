package web

import (
	"fmt"
	"net/http"
	"time"

	"templ-app/blocks/dashboard"
	"templ-app/internal/store"
)

// customerStatus maps a payer to the badge the directory shows: blocked,
// new (on the books but yet to pay) or active.
func customerStatus(s store.CustomerStatus, paid int64) dashboard.CustomerStatus {
	if s == store.CustomerStatusBlocked {
		return dashboard.CustomerBlocked
	}
	if paid == 0 {
		return dashboard.CustomerNew
	}
	return dashboard.CustomerActive
}

func (s *Server) customersPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	orgID, cur := v.orgID(), v.Org.Organization.DefaultCurrency

	q := r.URL.Query()
	statusFilter := q.Get("status")
	var filter *store.CustomerStatus
	if statusFilter == "blocked" || statusFilter == "active" {
		st := store.CustomerStatus(statusFilter)
		filter = &st
	}
	list, err := s.app.Customers.List(ctx, orgID, filter, cur, pageSize, pageOffset(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	total, err := s.app.Customers.Count(ctx, orgID, filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The tile counts every customer; the table footer counts the filter.
	all := total
	if filter != nil {
		if all, err = s.app.Customers.Count(ctx, orgID, nil); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	monthAgo := time.Now().AddDate(0, 0, -30)
	newThisMonth, err := s.app.Customers.CountSince(ctx, orgID, monthAgo)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	stats, err := s.app.Customers.Stats(ctx, orgID, cur)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	rows := make([]dashboard.Customer, len(list))
	for i, c := range list {
		// Show the stored name when there is one. Otherwise the email is the
		// only thing we know, so it is the title and the line below is the
		// merchant's own reference instead of a repeat.
		email, name, second := "", "", ""
		if c.Email != nil {
			email = *c.Email
		}
		switch {
		case c.Name != nil && *c.Name != "":
			name, second = *c.Name, email
		case email != "":
			name = email
		case c.ExternalID != nil:
			name = *c.ExternalID
		default:
			name = shortID("cus_", c.ID)
		}
		if second == "" && c.ExternalID != nil {
			second = *c.ExternalID
		}
		country := "—"
		if c.CountryCode.Valid && c.CountryCode.String != "" {
			country = c.CountryCode.String
		}
		vol, _ := c.Volume.Float64()
		last := c.LastActivityAt
		if last.IsZero() {
			last = c.CreatedAt
		}
		asset := c.TopAsset
		if asset == "" {
			asset = "—"
		}
		rows[i] = dashboard.Customer{
			ID: shortID("cus_", c.ID), Ref: c.ID.String(), Name: name, Email: second, Initials: initials(name), Country: country,
			Payments: int(c.Paid), Volume: formatFiat(vol, cur), Last: ago(last), Asset: asset,
			Status: customerStatus(c.Status, c.Paid), Since: c.CreatedAt.Format("Jan 2006"),
		}
	}
	avg, _ := stats.AvgVolume.Float64()
	d := dashboard.CustomersData{
		Sandbox: v.Sandbox, Customers: rows, Total: int(total), Status: statusFilter,
		Countries: countryChoices(), Error: q.Get("error"), Notice: q.Get("notice"),
		Stats: []dashboard.Stat{
			{Label: "Customers", Value: chartInt(int(all)), Hint: fmt.Sprintf("%d have paid", stats.Paying)},
			{Label: "New this month", Value: chartInt(int(newThisMonth)), Hint: "first seen in the last 30 days"},
			{Label: "Repeat rate", Value: fmt.Sprintf("%.0f%%", stats.RepeatRate()), Hint: fmt.Sprintf("%d paid more than once", stats.Repeat)},
			{Label: "Average lifetime value", Value: formatFiat(avg, cur), Hint: "per paying customer, in " + cur},
		},
	}
	s.renderDashboard(w, r, v, "Customers", "/dashboard/customers", dashboard.Customers(d))
}
