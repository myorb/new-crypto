package web

import (
	"fmt"
	"net/http"
	"strconv"

	"templ-app/blocks/dashboard"
	"templ-app/components/chart"
	"templ-app/internal/money"
	"templ-app/internal/payments"
	"templ-app/internal/store"
)

const pageSize = 50

func pageOffset(r *http.Request) int32 {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	return int32((p - 1) * pageSize)
}

func paymentStatus(s store.PaymentStatus) dashboard.Status {
	switch s {
	case store.PaymentStatusCredited, store.PaymentStatusConfirmed:
		return dashboard.StatusSucceeded
	case store.PaymentStatusDetected:
		return dashboard.StatusPending
	case store.PaymentStatusReverted:
		return dashboard.StatusFailed
	}
	return dashboard.StatusPending
}

// storeStatus maps a filter tab back to the single store status it lists.
func storeStatus(tab string) (*store.PaymentStatus, bool) {
	var st store.PaymentStatus
	switch tab {
	case "", "all":
		return nil, true
	case "succeeded":
		st = store.PaymentStatusCredited
	case "pending":
		st = store.PaymentStatusDetected
	case "failed":
		st = store.PaymentStatusReverted
	default:
		return nil, false // refunded: no such state yet
	}
	return &st, true
}

func (f *fx) paymentRow(r payments.Row) dashboard.Payment {
	p := r.Payment
	amount := money.FromNumeric(p.Amount)
	fees := money.FromNumeric(p.FeeAmount).Add(money.FromNumeric(p.SpreadAmount)).Add(money.FromNumeric(p.NetworkFeeAmount))
	customer, email := "—", ""
	if r.CustomerEmail != nil {
		email = *r.CustomerEmail
		customer = displayName(email)
	}
	conf := r.Confirmations
	if conf > r.RequiredConfirmations {
		conf = r.RequiredConfirmations
	}
	return dashboard.Payment{
		ID: shortID("pay_", p.ID), Customer: customer, Email: email, Asset: r.Asset.Symbol, Network: f.networkName(r.NetworkCode),
		Amount: amountStr(amount, r.Asset.Symbol, r.Asset.Decimals), Fiat: f.fiatOrDash(p.AssetID, amount),
		Status: paymentStatus(p.Status), Time: ago(p.DetectedAt), Color: assetColor(r.Asset.Symbol),
		Fee: f.fiatOrDash(p.AssetID, fees), Confirmations: fmt.Sprintf("%d/%d", conf, r.RequiredConfirmations),
	}
}

func (s *Server) paymentsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	orgID := v.orgID()
	f, err := s.loadFX(ctx, v.Org.Organization.DefaultCurrency)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	filter, known := storeStatus(r.URL.Query().Get("status"))
	var rows []payments.Row
	if known {
		if rows, err = s.app.Payments.List(ctx, orgID, filter, pageSize, pageOffset(r)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	counts, err := s.app.Payments.Counts(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ps, err := s.periodStats(ctx, orgID, f, 30)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := dashboard.PaymentsData{
		Sandbox: v.Sandbox, Period: "Last 30 days", Rows: make([]dashboard.Payment, len(rows)),
		Counts: map[dashboard.Status]int{
			dashboard.StatusSucceeded: int(counts[store.PaymentStatusCredited] + counts[store.PaymentStatusConfirmed]),
			dashboard.StatusPending:   int(counts[store.PaymentStatusDetected]),
			dashboard.StatusFailed:    int(counts[store.PaymentStatusReverted]),
		},
	}
	for _, c := range counts {
		d.Total += int(c)
	}
	for i, row := range rows {
		d.Rows[i] = f.paymentRow(row)
	}
	volChange, volTrend := change(ps.Volume, ps.PrevVolume)
	cntChange, cntTrend := change(float64(ps.Count), float64(ps.PrevCount))
	avg := 0.0
	if ps.Count > 0 {
		avg = ps.Volume / float64(ps.Count)
	}
	d.Stats = []dashboard.Stat{
		{Label: "Volume", Value: f.fiatStr(ps.Volume), Change: volChange, Trend: volTrend, Hint: "settled, last 30 days"},
		{Label: "Payments", Value: chart.Thousands(float64(ps.Count), 0), Change: cntChange, Trend: cntTrend, Hint: fmt.Sprintf("%d pending", ps.Pending)},
		{Label: "Average payment", Value: f.fiatStr(avg), Hint: "per settled payment"},
		{Label: "Success rate", Value: fmt.Sprintf("%.1f%%", ps.successRate()), Hint: fmt.Sprintf("%d reverted", ps.Failed)},
	}
	s.renderDashboard(w, r, v, "Payments", "/dashboard/payments", dashboard.Payments(d))
}
