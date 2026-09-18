package web

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"templ-app/blocks/dashboard"
	"templ-app/components/chart"
	"templ-app/internal/events"
	"templ-app/internal/store"
)

func (s *Server) webhooksPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	orgID := v.orgID()
	eps, err := s.app.Events.Endpoints(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	endpoints := make([]dashboard.Endpoint, len(eps))
	active := 0
	for i, ep := range eps {
		st, err := s.app.Events.Stats(ctx, ep.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		status := dashboard.EndpointEnabled
		switch {
		case !ep.IsActive:
			status = dashboard.EndpointDisabled
		case st.Total >= 5 && st.SuccessRate() < 80:
			status = dashboard.EndpointFailing
		}
		if ep.IsActive {
			active++
		}
		desc := ""
		if ep.Description != nil {
			desc = *ep.Description
		}
		endpoints[i] = dashboard.Endpoint{
			ID: shortID("we_", ep.ID), URL: ep.Url, Description: desc, Events: ep.EventTypes, Status: status,
			SuccessRate: st.SuccessRate(), Last: agoPtr(st.LastAttempt), Version: apiVersion,
		}
	}
	dels, err := s.app.Events.OrganizationDeliveries(ctx, orgID, pageSize, pageOffset(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	deliveries := make([]dashboard.Delivery, len(dels))
	for i, d := range dels {
		wd := d.WebhookDelivery
		host := d.EndpointUrl
		if u, err := url.Parse(d.EndpointUrl); err == nil && u.Host != "" {
			host = u.Host
		}
		code := 0
		if wd.LastStatusCode.Valid {
			code = int(wd.LastStatusCode.Int16)
		}
		deliveries[i] = dashboard.Delivery{
			ID: shortID("evt_", wd.EventID), Event: d.EventType, Endpoint: host, Code: code, Attempts: int(wd.Attempts), Latency: "—",
			OK: wd.Status == store.WebhookDeliveryStatusSucceeded, Pending: wd.Status == store.WebhookDeliveryStatusPending || wd.Status == store.WebhookDeliveryStatusFailed,
			Time: ago(wd.CreatedAt),
		}
	}
	counts, err := s.app.Events.DeliveryCounts(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var total int64
	for _, c := range counts {
		total += c
	}
	ok := counts[store.WebhookDeliveryStatusSucceeded]
	failed := counts[store.WebhookDeliveryStatusFailed] + counts[store.WebhookDeliveryStatusExhausted]
	rate := 100.0
	if ok+failed > 0 {
		rate = float64(ok) / float64(ok+failed) * 100
	}

	byDay, err := s.app.Events.DeliveriesByDay(ctx, orgID, time.Now().AddDate(0, 0, -14))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	perDay := map[string][2]float64{}
	for _, d := range byDay {
		k := dayKey(d.Day)
		vals := perDay[k]
		if d.Status == store.WebhookDeliveryStatusSucceeded {
			vals[0] += float64(d.Count)
		} else if d.Status != store.WebhookDeliveryStatusPending {
			vals[1] += float64(d.Count)
		}
		perDay[k] = vals
	}
	groups := make([]chart.Group, 0, 14)
	for _, d := range days(14) {
		vals := perDay[dayKey(d)]
		groups = append(groups, chart.Group{Label: dayLabel(d), Values: []float64{vals[0], vals[1]}})
	}

	d := dashboard.WebhooksData{
		Sandbox: v.Sandbox, Endpoints: endpoints, Deliveries: deliveries, Total: int(total), ByDay: groups,
		Secret: "whsec_" + "•••••••••••••••••••• (rotate to reveal)", EventTypes: events.Types(),
		Stats: []dashboard.Stat{
			{Label: "Delivered", Value: chartInt(int(ok)), Hint: "all time"},
			{Label: "Failed", Value: chartInt(int(failed)), Hint: fmt.Sprintf("%d exhausted", counts[store.WebhookDeliveryStatusExhausted])},
			{Label: "Success rate", Value: fmt.Sprintf("%.1f%%", rate), Hint: fmt.Sprintf("%d pending", counts[store.WebhookDeliveryStatusPending])},
			{Label: "Endpoints", Value: chartInt(len(eps)), Hint: fmt.Sprintf("%d active", active)},
		},
	}
	s.renderDashboard(w, r, v, "Webhooks", "/dashboard/webhooks", dashboard.Webhooks(d))
}
