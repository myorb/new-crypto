package web

import (
	"fmt"
	"net/http"
	"time"

	"templ-app/blocks/dashboard"
	"templ-app/components/chart"
	"templ-app/internal/money"
	"templ-app/internal/store"
)

func chartInt(n int) string { return chart.Thousands(float64(n), 0) }

func (s *Server) walletsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
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
	addrs, err := s.app.Chain.OrganizationAddresses(ctx, orgID, 100, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	firstOnNet := map[string]string{}
	for _, a := range addrs {
		if _, ok := firstOnNet[a.NetworkCode]; !ok {
			firstOnNet[a.NetworkCode] = a.Address.Address
		}
	}
	daily, err := s.app.Payments.VolumeByDay(ctx, orgID, time.Now().AddDate(0, 0, -14))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	perAsset := map[int16]map[string]float64{}
	for _, dv := range daily {
		if perAsset[dv.AssetID] == nil {
			perAsset[dv.AssetID] = map[string]float64{}
		}
		amt, _ := dv.Amount.Float64()
		perAsset[dv.AssetID][dayKey(dv.Day)] += amt
	}

	var wallets []dashboard.Wallet
	total := 0.0
	for _, b := range balances {
		held := b.Available.Add(b.Pending).Add(b.Locked)
		if held.IsZero() {
			continue
		}
		fv, ok := f.fiat(b.AssetID, held)
		total += fv
		a := f.asset(b.AssetID)
		fiat := "—"
		if ok {
			fiat = f.fiatStr(fv)
		}
		addr := "—"
		if x, ok := firstOnNet[b.NetworkCode]; ok {
			addr = shortAddr(x)
		}
		spark := windowVals(perAsset[b.AssetID], 0, 14)
		if sum(spark) == 0 {
			spark = zeros(14)
		}
		wallets = append(wallets, dashboard.Wallet{
			Symbol: b.Symbol, Name: a.Name, Network: f.networkName(b.NetworkCode), Address: addr,
			Amount: f.amount(b.AssetID, held), Fiat: fiat, Value: fv, Change: "", Trend: dashboard.TrendFlat,
			Spark: spark, Color: assetColor(b.Symbol), Chart: chartColor(len(wallets)),
		})
	}

	assetsByNet := map[string][]string{}
	if all, err := s.app.Catalog.Assets(ctx); err == nil {
		for _, a := range all {
			assetsByNet[a.Network.Code] = append(assetsByNet[a.Network.Code], a.Symbol)
		}
	}
	addresses := make([]dashboard.DepositAddress, 0, len(addrs))
	for i, a := range addrs {
		if i == 20 {
			break
		}
		memo := ""
		if a.Address.Memo != nil {
			memo = *a.Address.Memo
		}
		label := "Deposit address · " + ago(a.Address.CreatedAt)
		if a.Address.LastUsedAt != nil {
			label = "Last used " + ago(*a.Address.LastUsedAt)
		}
		addresses = append(addresses, dashboard.DepositAddress{Network: f.networkName(a.NetworkCode), Assets: assetsByNet[a.NetworkCode], Address: a.Address.Address, Memo: memo, Label: label})
	}

	rows, err := s.app.Chain.OrganizationActivity(ctx, orgID, 25, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	activityAll, err := s.app.Chain.CountOrganizationActivity(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	activity := make([]dashboard.Activity, len(rows))
	for i, row := range rows {
		t, tx := row.Transfer, row.Transaction
		kind, counterparty := dashboard.ActivityIn, ""
		if t.FromAddress != nil {
			counterparty = shortAddr(*t.FromAddress)
		}
		switch t.Direction {
		case store.TransferDirectionOut:
			kind, counterparty = dashboard.ActivityOut, shortAddr(t.ToAddress)
		case store.TransferDirectionInternal:
			kind = dashboard.ActivitySwap
		}
		status := dashboard.StatusPending
		switch tx.Status {
		case store.TxStatusConfirmed:
			status = dashboard.StatusSucceeded
		case store.TxStatusFailed, store.TxStatusDropped:
			status = dashboard.StatusFailed
		}
		conf := tx.Confirmations
		if conf > row.RequiredConfirmations {
			conf = row.RequiredConfirmations
		}
		amount := money.FromNumeric(t.Amount)
		activity[i] = dashboard.Activity{
			Hash: shortAddr(tx.Hash), Kind: kind, Asset: row.Symbol, Network: row.NetworkName,
			Amount: amountStr(amount, row.Symbol, row.Decimals), Fiat: f.fiatOrDash(t.AssetID, amount), Counterparty: counterparty,
			Confirmations: fmt.Sprintf("%d/%d", conf, row.RequiredConfirmations), Status: status, Time: ago(t.CreatedAt),
		}
	}

	d := dashboard.WalletsData{Sandbox: v.Sandbox, Total: f.fiatStr(total), Change: "", Trend: dashboard.TrendFlat, Wallets: wallets, Addresses: addresses, Activity: activity, ActivityAll: int(activityAll)}
	s.renderDashboard(w, r, v, "Wallets", "/dashboard/wallets", dashboard.Wallets(d))
}
