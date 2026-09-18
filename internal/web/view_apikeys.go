package web

import (
	"net/http"
	"strings"

	"templ-app/blocks/dashboard"
)

// apiScopes are the permissions a restricted key may hold. They match the
// scope strings checked by org.APIPrincipal.HasScope.
var apiScopes = []dashboard.Scope{
	{Key: "invoices:read", Label: "Invoices · read", Desc: "List and retrieve invoices"},
	{Key: "invoices:write", Label: "Invoices · write", Desc: "Create and cancel invoices"},
	{Key: "payments:read", Label: "Payments · read", Desc: "List and retrieve payments"},
	{Key: "payouts:read", Label: "Payouts · read", Desc: "List withdrawals and destinations"},
	{Key: "payouts:write", Label: "Payouts · write", Desc: "Request withdrawals"},
	{Key: "balances:read", Label: "Balances · read", Desc: "Read ledger balances"},
	{Key: "webhooks:read", Label: "Webhooks · read", Desc: "Inspect endpoints and deliveries"},
	{Key: "webhooks:write", Label: "Webhooks · write", Desc: "Manage endpoints"},
}

func (s *Server) apiKeysPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	keys, err := s.app.Org.APIKeys(r.Context(), v.orgID())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	mode, prefix := "live", "sk_live_"
	if v.Sandbox {
		mode, prefix = "test", "sk_test_"
	}
	var rows []dashboard.APIKey
	for _, k := range keys {
		if !strings.HasPrefix(k.KeyPrefix, prefix) {
			continue
		}
		typ := dashboard.KeySecret
		if len(k.Scopes) > 0 {
			typ = dashboard.KeyRestricted
		}
		rows = append(rows, dashboard.APIKey{
			ID: shortID("key_", k.ID), Name: k.Name, Token: k.KeyPrefix + strings.Repeat("•", 24), Type: typ, Scopes: k.Scopes,
			Created: k.CreatedAt.Format("Jan 2, 2006"), LastUsed: agoPtr(k.LastUsedAt), Requests: "—", Revoked: k.RevokedAt != nil,
		})
	}
	d := dashboard.APIKeysData{
		Sandbox: v.Sandbox, Mode: mode, Keys: rows, Scopes: apiScopes, APIVersion: apiVersion,
		Versions:   []dashboard.Option{{Value: apiVersion, Label: apiVersion + " · current"}},
		Requests:   zeros(14),
		Requests24: "—", ErrorRate: "—", RateLimit: "1,000 req/min", RateUsed: 0,
	}
	s.renderDashboard(w, r, v, "API keys", "/dashboard/api-keys", dashboard.APIKeys(d))
}
