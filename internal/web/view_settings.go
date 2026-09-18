package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"templ-app/blocks/dashboard"
	"templ-app/internal/store"
)

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	orgID, o := v.orgID(), v.Org.Organization
	tab := dashboard.SettingsTab(r.URL.Query().Get("tab"))
	active := "/dashboard/settings"
	if tab == "billing" {
		active += "?tab=billing"
	}

	country := "—"
	if o.CountryCode.Valid {
		country = o.CountryCode.String
	}
	legal := o.Name
	if o.LegalName != nil {
		legal = *o.LegalName
	}
	profile := dashboard.Profile{
		BusinessName: o.Name, LegalName: legal, SupportEmail: v.User.Email, Website: "", Country: country, Timezone: "UTC",
		Descriptor: strings.ToUpper(o.Name), Initials: initials(o.Name),
	}

	settings, err := s.app.Org.Assets(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	enabled := map[int16]bool{}
	for _, st := range settings {
		enabled[st.Asset.ID] = st.IsEnabled
	}
	all, err := s.app.Catalog.Assets(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	bySymbol := map[string]*dashboard.AcceptedAsset{}
	var accepted []dashboard.AcceptedAsset
	order := []string{}
	for _, a := range all {
		on := true
		if len(settings) > 0 {
			on = enabled[a.ID]
		}
		if x, ok := bySymbol[a.Symbol]; ok {
			x.Networks += " · " + a.Network.Name
			x.Enabled = x.Enabled || on
			continue
		}
		bySymbol[a.Symbol] = &dashboard.AcceptedAsset{Symbol: a.Symbol, Name: a.Name, Networks: a.Network.Name, Enabled: on}
		order = append(order, a.Symbol)
	}
	for _, sym := range order {
		accepted = append(accepted, *bySymbol[sym])
	}
	nets, err := s.app.Catalog.Networks(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	prefs := make([]dashboard.NetworkPref, len(nets))
	for i, n := range nets {
		typical := time.Duration(int64(n.RequiredConfirmations)*int64(n.AvgBlockTimeMs)) * time.Millisecond
		prefs[i] = dashboard.NetworkPref{Name: n.Name, Confirmations: fmt.Sprint(n.RequiredConfirmations), Typical: "~" + roundDuration(typical)}
	}
	payments := dashboard.PaymentPrefs{
		SettlementCurrency: o.DefaultCurrency, AutoConvertPct: "Off", Expiry: roundDuration(s.app.Config.InvoiceTTL), Tolerance: "Exact amount",
		Assets: accepted, Networks: prefs,
	}

	members, err := s.app.Org.Members(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	team := make([]dashboard.TeamMember, 0, len(members))
	userNames := map[uuid.UUID]string{}
	for _, m := range members {
		name := m.User.FullName
		if name == "" {
			name = displayName(m.User.Email)
		}
		userNames[m.User.ID] = name
		team = append(team, dashboard.TeamMember{Name: name, Email: m.User.Email, Initials: initials(name), Role: roleLabel(m.Role), Status: "active", Last: agoPtr(m.User.LastLoginAt), You: m.User.ID == v.User.ID})
	}
	invites, err := s.app.Org.OpenInvitations(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, inv := range invites {
		team = append(team, dashboard.TeamMember{Name: displayName(inv.Email), Email: inv.Email, Initials: initials(inv.Email), Role: roleLabel(inv.Role), Status: "invited", Last: "Invited " + ago(inv.CreatedAt)})
	}

	logs, err := s.app.Audit.List(ctx, orgID, 10, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	audit := make([]dashboard.AuditEntry, len(logs))
	for i, l := range logs {
		actor := "System"
		switch {
		case l.ActorUserID.Valid:
			if n, ok := userNames[l.ActorUserID.UUID]; ok {
				actor = n
			} else if u, err := s.app.Identity.User(ctx, l.ActorUserID.UUID); err == nil {
				actor = u.Email
			}
		case l.ActorApiKeyID.Valid:
			actor = "API key"
		}
		target := ""
		if l.EntityType != nil {
			target = *l.EntityType
			if l.EntityID != nil {
				target += " " + shortAddr(*l.EntityID)
			}
		}
		audit[i] = dashboard.AuditEntry{Actor: actor, Action: l.Action, Target: target, Time: ago(l.CreatedAt)}
	}

	d := dashboard.SettingsData{
		Sandbox: v.Sandbox, Profile: profile, Payments: payments, Team: team,
		Billing: dashboard.Billing{
			Plan: planLabel(o.Status), Price: "Usage based", Renews: "—", FeeRate: "Per fee schedule", VolumeUsed: "—", VolumeIncl: "—", VolumePct: 0,
			SeatsUsed: len(members), SeatsIncl: 0, CardBrand: "", CardLast4: "", CardExpiry: "", BillingEmail: v.User.Email, Invoices: []dashboard.Invoice{},
		},
		Security: dashboard.OrgSecurity{
			Require2FA: o.RequireMfa, SSOEnabled: false, SSOProvider: "", SessionTimeout: roundDuration(s.app.Config.SessionTTL),
			IPAllowlist: []string{}, PayoutApprovals: true, AuditLog: audit,
		},
	}
	s.renderDashboard(w, r, v, "Organization settings", active, dashboard.Settings(d, tab))
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	tab := dashboard.AccountTab(r.URL.Query().Get("tab"))
	name := v.User.FullName
	if name == "" {
		name = displayName(v.User.Email)
	}
	orgs := make([]dashboard.Organization, len(v.Memberships))
	for i, m := range v.Memberships {
		orgs[i] = dashboard.Organization{Name: m.Organization.Name, Plan: planLabel(m.Organization.Status), Initials: initials(m.Organization.Name), Role: roleLabel(m.Role), Current: m.Organization.ID == v.orgID()}
	}
	sessions, err := s.app.Identity.Sessions(ctx, v.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sess := make([]dashboard.Session, len(sessions))
	for i, se := range sessions {
		ua := ""
		if se.UserAgent != nil {
			ua = *se.UserAgent
		}
		ip := "—"
		if se.IpAddress != nil {
			ip = se.IpAddress.String()
		}
		device, browser := parseUserAgent(ua)
		sess[i] = dashboard.Session{Device: device, Browser: browser, Location: "—", IP: ip, Last: ago(se.LastSeenAt), Current: se.ID == v.Session.ID}
	}
	methods, err := s.app.Identity.MFAMethods(ctx, v.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var passkeys []dashboard.Passkey
	twoFactor := false
	for _, m := range methods {
		if m.VerifiedAt == nil {
			continue
		}
		twoFactor = true
		if m.Type == store.MfaMethodTypeWebauthn {
			passkeys = append(passkeys, dashboard.Passkey{Name: m.Label, Created: m.CreatedAt.Format("Jan 2, 2006"), Last: agoPtr(m.LastUsedAt)})
		}
	}
	d := dashboard.AccountData{
		Sandbox: v.Sandbox,
		User:    dashboard.UserProfile{Name: name, Email: v.User.Email, Initials: initials(name), Role: roleLabel(v.Org.Role), Language: "English", Timezone: "UTC", Phone: ""},
		Orgs:    orgs, Notifications: defaultNotifications(), Sessions: sess, Passkeys: passkeys, TwoFactor: twoFactor,
		PasswordAge: "Set " + ago(v.User.UpdatedAt),
	}
	s.renderDashboard(w, r, v, "Your account", "/dashboard/account", dashboard.Account(d, tab))
}

// defaultNotifications are shown until preferences get a table of their own.
func defaultNotifications() []dashboard.NotificationPref {
	return []dashboard.NotificationPref{
		{Group: "Payments", Label: "Payment confirmed", Desc: "A deposit reached the required confirmations", Email: true},
		{Group: "Payments", Label: "Payment reverted", Desc: "A detected deposit was dropped by the network", Email: true},
		{Group: "Payouts", Label: "Approval needed", Desc: "A teammate requested a withdrawal", Email: true},
		{Group: "Payouts", Label: "Payout completed", Desc: "A withdrawal was confirmed on chain", Email: true},
		{Group: "Security", Label: "New sign-in", Desc: "Your account signed in from a new device", Email: true},
		{Group: "Developers", Label: "Webhook failing", Desc: "An endpoint stopped accepting deliveries", Email: true},
	}
}

func parseUserAgent(ua string) (device, browser string) {
	device, browser = "Unknown device", "Unknown browser"
	switch {
	case strings.Contains(ua, "iPhone"):
		device = "iPhone"
	case strings.Contains(ua, "Android"):
		device = "Android"
	case strings.Contains(ua, "Macintosh"):
		device = "Mac"
	case strings.Contains(ua, "Windows"):
		device = "Windows PC"
	case strings.Contains(ua, "Linux"):
		device = "Linux"
	}
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.HasPrefix(ua, "curl"):
		browser = "curl"
	}
	return device, browser
}

func roundDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}
