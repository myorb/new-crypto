package dashboard

// Profile is the business profile form.
type Profile struct {
	BusinessName string
	LegalName    string
	SupportEmail string
	Website      string
	Country      string
	Timezone     string
	Descriptor   string
	Initials     string
}

// PaymentPrefs is the payment preferences form.
type PaymentPrefs struct {
	SettlementCurrency string
	AutoConvertPct     string
	Expiry             string
	Tolerance          string
	Assets             []AcceptedAsset
	Networks           []NetworkPref
}

// AcceptedAsset is one asset toggle.
type AcceptedAsset struct {
	Symbol   string
	Name     string
	Networks string
	Enabled  bool
}

// NetworkPref is the confirmations rule for one network.
type NetworkPref struct {
	Name          string
	Confirmations string
	Typical       string
}

// TeamMember is one row of the team table.
type TeamMember struct {
	Name     string
	Email    string
	Initials string
	Role     string
	Status   string // "active" or "invited"
	Last     string
	You      bool
}

// Invoice is one row of the billing history.
type Invoice struct {
	Number string
	Period string
	Amount string
	Status string // "paid", "open"
	Date   string
}

// Billing is the plan, usage and payment method of the organization.
type Billing struct {
	Plan         string
	Price        string
	Renews       string
	FeeRate      string
	VolumeUsed   string
	VolumeIncl   string
	VolumePct    int
	SeatsUsed    int
	SeatsIncl    int
	CardBrand    string
	CardLast4    string
	CardExpiry   string
	BillingEmail string
	Invoices     []Invoice
}

// OrgSecurity is the organization-wide security policy.
type OrgSecurity struct {
	Require2FA      bool
	SSOEnabled      bool
	SSOProvider     string
	SessionTimeout  string
	IPAllowlist     []string
	PayoutApprovals bool
	AuditLog        []AuditEntry
}

// AuditEntry is one row of the audit log preview.
type AuditEntry struct {
	Actor  string
	Action string
	Target string
	Time   string
}

// SettingsData is everything the organization settings page renders.
type SettingsData struct {
	Sandbox  bool
	Profile  Profile
	Payments PaymentPrefs
	Team     []TeamMember
	Billing  Billing
	Security OrgSecurity
}

// SettingsFixture returns the settings fixture.
func SettingsFixture(sandbox bool) SettingsData {
	return SettingsData{
		Sandbox: sandbox,
		Profile: Profile{
			BusinessName: "Northwind Labs",
			LegalName:    "Northwind Labs GmbH",
			SupportEmail: "support@northwind.dev",
			Website:      "https://northwind.dev",
			Country:      "DE",
			Timezone:     "Europe/Berlin",
			Descriptor:   "NORTHWIND LABS",
			Initials:     "NL",
		},
		Payments: PaymentPrefs{
			SettlementCurrency: "USD",
			AutoConvertPct:     "65",
			Expiry:             "30m",
			Tolerance:          "1",
			Assets: []AcceptedAsset{
				{Symbol: "USDT", Name: "Tether", Networks: "Tron, Ethereum", Enabled: true},
				{Symbol: "USDC", Name: "USD Coin", Networks: "Arc, Solana, Ethereum", Enabled: true},
				{Symbol: "BTC", Name: "Bitcoin", Networks: "Bitcoin", Enabled: true},
				{Symbol: "ETH", Name: "Ethereum", Networks: "Ethereum, Arc", Enabled: true},
				{Symbol: "SOL", Name: "Solana", Networks: "Solana", Enabled: true},
				{Symbol: "TRX", Name: "Tron", Networks: "Tron", Enabled: false},
			},
			Networks: []NetworkPref{
				{Name: "Bitcoin", Confirmations: "3", Typical: "~30 min"},
				{Name: "Ethereum", Confirmations: "12", Typical: "~3 min"},
				{Name: "Tron", Confirmations: "19", Typical: "~1 min"},
				{Name: "Solana", Confirmations: "32", Typical: "~15 s"},
				{Name: "Arc", Confirmations: "1", Typical: "~2 s"},
			},
		},
		Team: []TeamMember{
			{Name: "Alex Shalaiev", Email: "alex@northwind.dev", Initials: "AS", Role: "Owner", Status: "active", Last: "Now", You: true},
			{Name: "Priya Raman", Email: "priya@northwind.dev", Initials: "PR", Role: "Admin", Status: "active", Last: "2 h ago"},
			{Name: "Tomás Ferreira", Email: "tomas@northwind.dev", Initials: "TF", Role: "Developer", Status: "active", Last: "Yesterday"},
			{Name: "Hannah Weiss", Email: "hannah@northwind.dev", Initials: "HW", Role: "Finance", Status: "active", Last: "3 days ago"},
			{Name: "jules@northwind.dev", Email: "jules@northwind.dev", Initials: "J", Role: "Support", Status: "invited", Last: "Invited Sep 15"},
		},
		Billing: Billing{
			Plan:         "Business",
			Price:        "$249 / month",
			Renews:       "Oct 1, 2026",
			FeeRate:      "0.25% + network fee",
			VolumeUsed:   "$1,284,320",
			VolumeIncl:   "$2,000,000",
			VolumePct:    64,
			SeatsUsed:    5,
			SeatsIncl:    10,
			CardBrand:    "Visa",
			CardLast4:    "4242",
			CardExpiry:   "08/28",
			BillingEmail: "finance@northwind.dev",
			Invoices: []Invoice{
				{Number: "INV-2026-0009", Period: "Sep 2026", Amount: "$249.00", Status: "open", Date: "Due Oct 1"},
				{Number: "INV-2026-0008", Period: "Aug 2026", Amount: "$249.00", Status: "paid", Date: "Sep 1"},
				{Number: "INV-2026-0007", Period: "Jul 2026", Amount: "$249.00", Status: "paid", Date: "Aug 1"},
				{Number: "INV-2026-0006", Period: "Jun 2026", Amount: "$249.00", Status: "paid", Date: "Jul 1"},
				{Number: "INV-2026-0005", Period: "May 2026", Amount: "$99.00", Status: "paid", Date: "Jun 1"},
			},
		},
		Security: OrgSecurity{
			Require2FA:      true,
			SSOEnabled:      false,
			SSOProvider:     "",
			SessionTimeout:  "12h",
			IPAllowlist:     []string{"84.132.10.0/24", "2a02:8109::/32"},
			PayoutApprovals: true,
			AuditLog: []AuditEntry{
				{Actor: "Priya Raman", Action: "Created API key", Target: "Finance reporting", Time: "2 h ago"},
				{Actor: "Alex Shalaiev", Action: "Changed payout schedule", Target: "Weekly → Fridays", Time: "Yesterday"},
				{Actor: "Tomás Ferreira", Action: "Added webhook endpoint", Target: "api.northwind.dev/webhooks/paychain", Time: "Sep 12"},
				{Actor: "Alex Shalaiev", Action: "Invited member", Target: "jules@northwind.dev", Time: "Sep 15"},
				{Actor: "Hannah Weiss", Action: "Requested payout", Target: "$25,000 USDC → Treasury wallet", Time: "Sep 10"},
			},
		},
	}
}
