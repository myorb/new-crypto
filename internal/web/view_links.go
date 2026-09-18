package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"templ-app/blocks/dashboard"
	"templ-app/internal/money"
	"templ-app/internal/store"
)

func linkStatus(s store.PaymentLinkStatus) dashboard.LinkStatus {
	switch s {
	case store.PaymentLinkStatusActive:
		return dashboard.LinkActive
	case store.PaymentLinkStatusPaused:
		return dashboard.LinkPaused
	}
	return dashboard.LinkExpired
}

// linkHost is where hosted checkout pages live. Links are served by this app
// at /l/<slug>, so the host is whatever the dashboard is reached on.
func (s *Server) linkHost(r *http.Request) string { return r.Host }

func (s *Server) linksPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	orgID, cur := v.orgID(), v.Org.Organization.DefaultCurrency

	q := r.URL.Query()
	statusFilter := q.Get("status")
	var filter *store.PaymentLinkStatus
	switch statusFilter {
	case "active", "paused", "completed", "expired", "archived":
		st := store.PaymentLinkStatus(statusFilter)
		filter = &st
	}
	list, err := s.app.Checkout.Links(ctx, orgID, filter, pageSize, pageOffset(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	total, err := s.app.Checkout.CountLinks(ctx, orgID, filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := time.Now()
	stats, err := s.app.Checkout.LinkStats(ctx, orgID, cur, now.AddDate(0, 0, -30), now.AddDate(0, 0, 7))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ids := make([]uuid.UUID, len(list))
	for i, l := range list {
		ids[i] = l.ID
	}
	assets := map[uuid.UUID][]store.ListPaymentLinkAssetCodesRow{}
	if len(ids) > 0 {
		if assets, err = s.app.Checkout.LinkAssets(ctx, ids); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	host := s.linkHost(r)
	scheme := "https://"
	if !s.production() {
		scheme = "http://"
	}
	rows := make([]dashboard.PaymentLink, len(list))
	for i, l := range list {
		amount := ""
		if l.PriceAmount.Valid {
			amount = formatFiat(f64(money.FromNumeric(l.PriceAmount)), l.PriceCurrency)
		}
		var symbols []string
		for _, a := range assets[l.ID] {
			symbols = append(symbols, a.Symbol)
		}
		if len(symbols) == 0 {
			symbols = []string{"All enabled assets"}
		}
		expires := "Never"
		if l.ExpiresAt != nil {
			expires = l.ExpiresAt.Format("Jan 2, 2006")
		}
		rev, _ := l.Revenue.Float64()
		rows[i] = dashboard.PaymentLink{
			ID: shortID("plink_", l.ID), Ref: l.ID.String(), Name: l.Name, URL: scheme + host + "/l/" + l.Slug, Amount: amount, Assets: symbols,
			Views: int(l.ViewCount), Paid: int(l.Paid), Revenue: formatFiat(rev, cur), Status: linkStatus(l.Status),
			Created: l.CreatedAt.Format("Jan 2, 2006"), Expires: expires, Reusable: !l.MaxUses.Valid || l.MaxUses.Int32 > 1,
		}
	}
	revenue, _ := stats.Revenue.Float64()
	assetChoices, err := s.assetChoices(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := dashboard.LinksData{
		Sandbox: v.Sandbox, Links: rows, Total: int(total), Host: host, Status: statusFilter,
		Currencies: currencyChoices(cur), Assets: assetChoices, Error: q.Get("error"), Notice: q.Get("notice"),
		Stats: []dashboard.Stat{
			{Label: "Active links", Value: chartInt(int(stats.Active)), Hint: fmt.Sprintf("%d expiring within 7 days", stats.Expiring)},
			{Label: "Views", Value: chartInt(int(stats.Views)), Hint: "hosted page opens, all time"},
			{Label: "Paid", Value: chartInt(int(stats.Paid)), Hint: fmt.Sprintf("across %d links, last 30 days", stats.UsedLink)},
			{Label: "Revenue", Value: formatFiat(revenue, cur), Hint: fmt.Sprintf("%.1f%% conversion", stats.Conversion())},
		},
	}
	s.renderDashboard(w, r, v, "Payment links", "/dashboard/links", dashboard.Links(d))
}

// assetChoices lists what a new link can accept: the merchant's enabled
// assets, or the whole catalog when they have enabled none yet.
func (s *Server) assetChoices(ctx context.Context, orgID uuid.UUID) ([]dashboard.Option, error) {
	settings, err := s.app.Org.Assets(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := []dashboard.Option{}
	for _, a := range settings {
		if a.IsEnabled {
			out = append(out, dashboard.Option{Value: strconv.Itoa(int(a.Asset.ID)), Label: a.Asset.Symbol + " · " + a.NetworkName})
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	assets, err := s.app.Catalog.Assets(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		if a.IsEnabled {
			out = append(out, dashboard.Option{Value: strconv.Itoa(int(a.ID)), Label: a.Symbol + " · " + a.Network.Name})
		}
	}
	return out, nil
}

// currencyChoices offers the merchant's reporting currency first.
func currencyChoices(preferred string) []dashboard.Option {
	out := []dashboard.Option{{Value: preferred, Label: preferred}}
	for _, c := range []string{"USD", "EUR", "GBP", "CHF"} {
		if c != preferred {
			out = append(out, dashboard.Option{Value: c, Label: c})
		}
	}
	return out
}
