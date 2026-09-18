package web

import (
	"net/http"
	"time"

	"templ-app/blocks/dashboard"
	"templ-app/internal/money"
	"templ-app/internal/store"
)

func payoutStatus(s store.WithdrawalStatus) dashboard.PayoutStatus {
	switch s {
	case store.WithdrawalStatusConfirmed:
		return dashboard.PayoutPaid
	case store.WithdrawalStatusApproved, store.WithdrawalStatusQueued, store.WithdrawalStatusBroadcast:
		return dashboard.PayoutInTransit
	case store.WithdrawalStatusPendingApproval:
		return dashboard.PayoutPending
	}
	return dashboard.PayoutFailed
}

func (s *Server) payoutsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	orgID := v.orgID()
	f, err := s.loadFX(ctx, v.Org.Organization.DefaultCurrency)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	balances, err := s.app.Ledger.MerchantBalances(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var available, pending, locked float64
	for _, b := range balances {
		av, _ := f.fiat(b.AssetID, b.Available)
		pe, _ := f.fiat(b.AssetID, b.Pending)
		lo, _ := f.fiat(b.AssetID, b.Locked)
		available, pending, locked = available+av, pending+pe, locked+lo
	}
	yearStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	ytd, err := s.app.Payouts.CompletedByAsset(ctx, orgID, yearStart)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	paidYTD := 0.0
	for _, a := range ytd {
		fv, _ := f.fiat(a.AssetID, a.Amount)
		paidYTD += fv
	}
	dests, err := s.app.Payouts.Addresses(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	destByAddr := map[string]string{}
	destinations := make([]dashboard.Destination, len(dests))
	for i, d := range dests {
		wa := d.WithdrawalAddress
		destByAddr[wa.Address] = wa.Label
		destinations[i] = dashboard.Destination{Name: wa.Label, Detail: d.NetworkName + " · " + shortAddr(wa.Address), Currency: d.NetworkCode, Kind: "wallet", Default: i == 0, Verified: wa.IsWhitelisted}
	}
	rows, err := s.app.Payouts.List(ctx, orgID, nil, pageSize, pageOffset(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts, err := s.app.Payouts.Counts(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	total := 0
	inFlight := 0
	for st, c := range counts {
		total += int(c)
		if payoutStatus(st) == dashboard.PayoutInTransit {
			inFlight += int(c)
		}
	}
	payouts := make([]dashboard.Payout, len(rows))
	for i, row := range rows {
		wd := row.Withdrawal
		dest := shortAddr(wd.ToAddress)
		if label, ok := destByAddr[wd.ToAddress]; ok {
			dest = label
		}
		arrival := "—"
		if wd.CompletedAt != nil {
			arrival = wd.CompletedAt.Format("Jan 2, 15:04")
		} else if wd.Status == store.WithdrawalStatusBroadcast {
			arrival = "Awaiting confirmations"
		}
		payouts[i] = dashboard.Payout{
			ID: shortID("po_", wd.ID), Destination: dest, DestHint: f.networkName(row.NetworkCode) + " · " + shortAddr(wd.ToAddress),
			Amount: amountStr(money.FromNumeric(wd.Amount), row.Asset.Symbol, row.Asset.Decimals),
			Fee:    amountStr(money.FromNumeric(wd.FeeAmount), row.Asset.Symbol, row.Asset.Decimals),
			Method: row.Asset.Symbol + " on-chain", Status: payoutStatus(wd.Status), Initiated: ago(wd.CreatedAt), Arrival: arrival, Auto: wd.RequestedByApiKey.Valid,
		}
	}
	inTransitHint := "Nothing in flight"
	if inFlight > 0 {
		inTransitHint = pluralize(inFlight, "payout") + " in progress"
	}
	d := dashboard.PayoutsData{
		Sandbox: v.Sandbox, Available: f.fiatStr(available), Pending: f.fiatStr(pending), PendingHint: "Awaiting on-chain confirmations",
		InTransit: f.fiatStr(locked), InTransitHint: inTransitHint, PaidYTD: f.fiatStr(paidYTD),
		Schedule:     dashboard.Schedule{Enabled: false, Frequency: "Manual", Threshold: "—", Progress: 0, Progress0: f.fiatStr(available), Next: "Request a payout from this page", MinAmount: "Per asset minimum"},
		Destinations: destinations, Payouts: payouts, Total: total,
	}
	s.renderDashboard(w, r, v, "Payouts", "/dashboard/payouts", dashboard.Payouts(d))
}

func pluralize(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return itoa(n) + " " + noun + "s"
}

func itoa(n int) string {
	return chartInt(n)
}
