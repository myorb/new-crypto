package web

import (
	"fmt"
	"net/http"
	"time"

	"templ-app/blocks/dashboard"
	"templ-app/internal/store"
)

func customerStatus(s store.CustomerStatus, paid int64, since time.Time, firstSeen time.Time) dashboard.CustomerStatus {
	if s == store.CustomerStatusBlocked {
		return dashboard.CustomerBlocked
	}
	if paid == 0 || firstSeen.After(since) {
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
		email, name := "", ""
		if c.Email != nil {
			email = *c.Email
			name = displayName(email)
		}
		if c.Name != nil && *c.Name != "" {
			name = *c.Name
		}
		if name == "" {
			name = shortID("cus_", c.ID)
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
		rows[i] = dashboard.Customer{
			ID: shortID("cus_", c.ID), Ref: c.ID.String(), Name: name, Email: email, Initials: initials(name), Country: country,
			Payments: int(c.Paid), Volume: formatFiat(vol, cur), Last: ago(last), Asset: "—",
			Status: customerStatus(c.Status, c.Paid, monthAgo, c.CreatedAt), Since: c.CreatedAt.Format("Jan 2006"),
		}
	}
	avg, _ := stats.AvgVolume.Float64()
	d := dashboard.CustomersData{
		Sandbox: v.Sandbox, Customers: rows, Total: int(total), Status: statusFilter,
		Countries: countryChoices(), Error: q.Get("error"), Notice: q.Get("notice"),
		Stats: []dashboard.Stat{
			{Label: "Customers", Value: chartInt(int(total)), Hint: fmt.Sprintf("%d have paid", stats.Paying)},
			{Label: "New this month", Value: chartInt(int(newThisMonth)), Hint: "first seen in the last 30 days"},
			{Label: "Repeat rate", Value: fmt.Sprintf("%.0f%%", stats.RepeatRate()), Hint: fmt.Sprintf("%d paid more than once", stats.Repeat)},
			{Label: "Average lifetime value", Value: formatFiat(avg, cur), Hint: "per paying customer, in " + cur},
		},
	}
	s.renderDashboard(w, r, v, "Customers", "/dashboard/customers", dashboard.Customers(d))
}
