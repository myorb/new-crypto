package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"templ-app/blocks/dashboard"
	"templ-app/components/chart"
	"templ-app/internal/store"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request, v *viewer) {
	d, err := s.overviewData(r.Context(), v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderDashboard(w, r, v, "Overview", "/dashboard", dashboard.Overview(d))
}

// periodStats summarises payments over the last `days` days and the period before.
type periodStats struct {
	Volume, PrevVolume  float64
	Fees                float64
	Count, PrevCount    int64
	Ok, Pending, Failed int64
}

func (ps periodStats) successRate() float64 {
	settled := ps.Ok + ps.Failed
	if settled == 0 {
		return 100
	}
	return float64(ps.Ok) / float64(settled) * 100
}

func (s *Server) periodStats(ctx context.Context, orgID uuid.UUID, f *fx, days int) (periodStats, error) {
	var ps periodStats
	now := time.Now()
	since, before := now.AddDate(0, 0, -days), now.AddDate(0, 0, -2*days)
	cur, err := s.app.Payments.ByAsset(ctx, orgID, since)
	if err != nil {
		return ps, err
	}
	both, err := s.app.Payments.ByAsset(ctx, orgID, before)
	if err != nil {
		return ps, err
	}
	for _, a := range cur {
		fv, _ := f.fiat(a.AssetID, a.Amount)
		fee, _ := f.fiat(a.AssetID, a.Fees)
		ps.Volume += fv
		ps.Fees += fee
	}
	for _, a := range both {
		fv, _ := f.fiat(a.AssetID, a.Amount)
		ps.PrevVolume += fv
	}
	ps.PrevVolume -= ps.Volume
	if ps.Count, err = s.app.Payments.CountSince(ctx, orgID, since); err != nil {
		return ps, err
	}
	if ps.PrevCount, err = s.app.Payments.CountSince(ctx, orgID, before); err != nil {
		return ps, err
	}
	ps.PrevCount -= ps.Count
	statuses, err := s.app.Payments.StatusByDay(ctx, orgID, since)
	if err != nil {
		return ps, err
	}
	for _, st := range statuses {
		switch st.Status {
		case store.PaymentStatusCredited, store.PaymentStatusConfirmed:
			ps.Ok += st.Count
		case store.PaymentStatusDetected:
			ps.Pending += st.Count
		case store.PaymentStatusReverted:
			ps.Failed += st.Count
		}
	}
	return ps, nil
}

// windowVals returns n daily values ending `end` days ago, oldest first.
func windowVals(m map[string]float64, end, n int) []float64 {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	out := make([]float64, n)
	for i := range out {
		out[i] = m[dayKey(today.AddDate(0, 0, -(end+n-1-i)))]
	}
	return out
}

func (s *Server) overviewData(ctx context.Context, v *viewer) (dashboard.Data, error) {
	orgID, o := v.orgID(), v.Org.Organization
	f, err := s.loadFX(ctx, o.DefaultCurrency)
	if err != nil {
		return dashboard.Data{}, err
	}
	now := time.Now()

	daily, err := s.app.Payments.VolumeByDay(ctx, orgID, now.AddDate(0, 0, -181))
	if err != nil {
		return dashboard.Data{}, err
	}
	fiatByDay, countByDay := map[string]float64{}, map[string]float64{}
	for _, dv := range daily {
		k := dayKey(dv.Day)
		fv, _ := f.fiat(dv.AssetID, dv.Amount)
		fiatByDay[k] += fv
		countByDay[k] += float64(dv.Count)
	}
	ps, err := s.periodStats(ctx, orgID, f, 30)
	if err != nil {
		return dashboard.Data{}, err
	}
	settled, err := s.app.Payouts.CompletedByAsset(ctx, orgID, now.AddDate(0, 0, -30))
	if err != nil {
		return dashboard.Data{}, err
	}
	settledFiat := 0.0
	for _, a := range settled {
		fv, _ := f.fiat(a.AssetID, a.Amount)
		settledFiat += fv
	}

	volChange, volTrend := change(ps.Volume, ps.PrevVolume)
	cntChange, cntTrend := change(float64(ps.Count), float64(ps.PrevCount))
	todayCount := int(countByDay[dayKey(now)])
	avg := 0.0
	if ps.Count > 0 {
		avg = ps.Volume / float64(ps.Count)
	}
	kpis := []dashboard.KPI{
		{Label: "Volume processed", Value: f.fiatStr(ps.Volume), Change: volChange, Trend: volTrend, Hint: "vs. " + f.fiatStr(ps.PrevVolume) + " previous 30 days", Spark: windowVals(fiatByDay, 0, 30)},
		{Label: "Payments", Value: chart.Thousands(float64(ps.Count), 0), Change: cntChange, Trend: cntTrend, Hint: fmt.Sprintf("%d today · avg. %s", todayCount, f.fiatStr(avg)), Spark: windowVals(countByDay, 0, 30)},
		{Label: "Paid out", Value: f.fiatStr(settledFiat), Change: "", Trend: dashboard.TrendFlat, Hint: "Completed withdrawals, last 30 days", Spark: zeros(14)},
		{Label: "Success rate", Value: fmt.Sprintf("%.1f%%", ps.successRate()), Change: "", Trend: dashboard.TrendFlat, Hint: fmt.Sprintf("%d reverted · %d pending", ps.Failed, ps.Pending), Spark: zeros(14), Invert: true},
	}

	rng := func(key, label string, n int) dashboard.Range {
		cur, prev := windowVals(fiatByDay, 0, n), windowVals(fiatByDay, n, n)
		ds := days(n)
		curPts, prevPts := make([]chart.Point, n), make([]chart.Point, n)
		for i := range ds {
			curPts[i] = chart.Point{Label: dayLabel(ds[i]), Value: cur[i]}
			prevPts[i] = chart.Point{Label: dayLabel(ds[i]), Value: prev[i]}
		}
		ch, tr := change(sum(cur), sum(prev))
		return dashboard.Range{Key: key, Label: label, Total: f.fiatStr(sum(cur)), Change: ch, Trend: tr, Series: []chart.Series{
			{Name: "This period", Color: "var(--chart-1)", Points: curPts},
			{Name: "Previous period", Color: "var(--muted-foreground)", Points: prevPts, Dashed: true, NoFill: true},
		}}
	}

	byAsset, err := s.app.Payments.ByAsset(ctx, orgID, now.AddDate(0, 0, -30))
	if err != nil {
		return dashboard.Data{}, err
	}
	var assets []dashboard.AssetShare
	for i, a := range byAsset {
		if i == 5 {
			break
		}
		fv, _ := f.fiat(a.AssetID, a.Amount)
		assets = append(assets, dashboard.AssetShare{Symbol: a.Symbol, Name: a.Name, Volume: fv, Fiat: f.fiatStr(fv), Color: chartColor(i)})
	}
	byNet, err := s.app.Payments.ByNetwork(ctx, orgID, now.AddDate(0, 0, -30))
	if err != nil {
		return dashboard.Data{}, err
	}
	var netTotal int64
	for _, n := range byNet {
		netTotal += n.Count
	}
	var networks []dashboard.NetworkShare
	for i, n := range byNet {
		if i == 5 {
			break
		}
		networks = append(networks, dashboard.NetworkShare{Name: n.Name, Payments: int(n.Count), Share: pct(float64(n.Count), float64(netTotal)), Color: chartColor(i)})
	}

	statuses, err := s.app.Payments.StatusByDay(ctx, orgID, now.AddDate(0, 0, -14))
	if err != nil {
		return dashboard.Data{}, err
	}
	perDay := map[string][3]float64{}
	for _, st := range statuses {
		k := dayKey(st.Day)
		vals := perDay[k]
		switch st.Status {
		case store.PaymentStatusCredited, store.PaymentStatusConfirmed:
			vals[0] += float64(st.Count)
		case store.PaymentStatusDetected:
			vals[1] += float64(st.Count)
		case store.PaymentStatusReverted:
			vals[2] += float64(st.Count)
		}
		perDay[k] = vals
	}
	statusByDay := make([]chart.Group, 0, 14)
	for _, d := range days(14) {
		vals := perDay[dayKey(d)]
		statusByDay = append(statusByDay, chart.Group{Label: dayLabel(d), Values: []float64{vals[0], vals[1], vals[2]}})
	}

	balances, err := s.app.Ledger.MerchantBalances(ctx, orgID)
	if err != nil {
		return dashboard.Data{}, err
	}
	var available, pending, locked, totalHeld float64
	var rows []dashboard.Balance
	for _, b := range balances {
		if b.Available.IsZero() && b.Pending.IsZero() && b.Locked.IsZero() {
			continue
		}
		av, _ := f.fiat(b.AssetID, b.Available)
		pe, _ := f.fiat(b.AssetID, b.Pending)
		lo, _ := f.fiat(b.AssetID, b.Locked)
		available, pending, locked = available+av, pending+pe, locked+lo
		totalHeld += av + pe + lo
		a := f.asset(b.AssetID)
		rows = append(rows, dashboard.Balance{
			Symbol: b.Symbol, Name: a.Name, Amount: f.amount(b.AssetID, b.Available), Fiat: f.fiatOrDash(b.AssetID, b.Available),
			Change: "", Trend: dashboard.TrendFlat, Share: av + pe + lo, Color: assetColor(b.Symbol),
		})
	}
	for i := range rows {
		rows[i].Share = pct(rows[i].Share, totalHeld)
	}
	dests, err := s.app.Payouts.Addresses(ctx, orgID)
	if err != nil {
		return dashboard.Data{}, err
	}
	nextTo := "No payout destination saved"
	if len(dests) > 0 {
		nextTo = dests[0].WithdrawalAddress.Label + " · " + shortAddr(dests[0].WithdrawalAddress.Address)
	}
	feeRate := "no volume yet"
	if ps.Volume > 0 {
		feeRate = fmt.Sprintf("%.2f%% effective rate", ps.Fees/ps.Volume*100)
	}
	settlement := dashboard.Settlement{
		Available: f.fiatStr(available), Pending: f.fiatStr(pending), PendingHint: "Awaiting confirmations",
		InTransit: f.fiatStr(locked), Fees: f.fiatStr(ps.Fees), FeesHint: feeRate,
		NextPayoutDate: "On request", NextPayoutTo: nextTo, ThresholdValue: 0, ThresholdLabel: "Automatic payouts are not configured",
		AutoConvertNote: "Balances are held in the assets your customers paid with",
	}

	recent, err := s.app.Payments.List(ctx, orgID, nil, 8, 0)
	if err != nil {
		return dashboard.Data{}, err
	}
	payments := make([]dashboard.Payment, len(recent))
	for i, row := range recent {
		payments[i] = f.paymentRow(row)
	}
	counts, err := s.app.Payments.Counts(ctx, orgID)
	if err != nil {
		return dashboard.Data{}, err
	}
	var total int64
	for _, c := range counts {
		total += c
	}

	d := dashboard.Data{
		MerchantName: o.Name, Initials: initials(o.Name), Period: "Last 30 days", Today: now.Format("Mon, Jan 2"), Sandbox: v.Sandbox,
		KPIs: kpis, Ranges: []dashboard.Range{rng("7d", "7 days", 7), rng("30d", "30 days", 30), rng("90d", "90 days", 90)},
		Assets: assets, Networks: networks, StatusByDay: statusByDay, Settlement: settlement, Balances: rows, Payments: payments, TotalCount: int(total),
	}
	if v.Sandbox {
		if d.Developer, err = s.developerCard(ctx, orgID); err != nil {
			return dashboard.Data{}, err
		}
	}
	return d, nil
}

// developerCard fills the sandbox tooling card with the org's test keys and
// webhook health.
func (s *Server) developerCard(ctx context.Context, orgID uuid.UUID) (dashboard.Developer, error) {
	dev := dashboard.Developer{PublishableKey: "—", SecretKey: "No test key yet", APIVersion: apiVersion, WebhookURL: "No endpoint configured"}
	keys, err := s.app.Org.APIKeys(ctx, orgID)
	if err != nil {
		return dev, err
	}
	for _, k := range keys {
		if k.RevokedAt == nil && strings.HasPrefix(k.KeyPrefix, "sk_test_") {
			dev.SecretKey = k.KeyPrefix + strings.Repeat("•", 20)
			dev.PublishableKey = k.Name
			break
		}
	}
	eps, err := s.app.Events.Endpoints(ctx, orgID)
	if err != nil {
		return dev, err
	}
	if len(eps) > 0 {
		dev.WebhookURL = eps[0].Url
	}
	counts, err := s.app.Events.DeliveryCounts(ctx, orgID)
	if err != nil {
		return dev, err
	}
	dev.Delivered = int(counts[store.WebhookDeliveryStatusSucceeded])
	dev.Failed = int(counts[store.WebhookDeliveryStatusFailed] + counts[store.WebhookDeliveryStatusExhausted])
	dels, err := s.app.Events.OrganizationDeliveries(ctx, orgID, 5, 0)
	if err != nil {
		return dev, err
	}
	for _, d := range dels {
		dev.Events = append(dev.Events, dashboard.WebhookEvent{Type: d.EventType, ID: shortID("evt_", d.WebhookDelivery.EventID), Time: ago(d.WebhookDelivery.CreatedAt), OK: d.WebhookDelivery.Status == store.WebhookDeliveryStatusSucceeded})
	}
	return dev, nil
}

// apiVersion is the public API version label shown to developers.
const apiVersion = "2026-09-01"
