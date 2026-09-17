package dashboard

// UserProfile is the signed-in person.
type UserProfile struct {
	Name     string
	Email    string
	Initials string
	Role     string
	Language string
	Timezone string
	Phone    string
}

// Organization is one workspace the user belongs to.
type Organization struct {
	Name     string
	Plan     string
	Initials string
	Role     string
	Current  bool
}

// NotificationPref is one notification row.
type NotificationPref struct {
	Group string
	Label string
	Desc  string
	Email bool
	Push  bool
	Slack bool
}

// Session is one signed-in device.
type Session struct {
	Device   string
	Browser  string
	Location string
	IP       string
	Last     string
	Current  bool
}

// Passkey is one registered WebAuthn credential.
type Passkey struct {
	Name    string
	Created string
	Last    string
}

// AccountData is everything the personal account page renders.
type AccountData struct {
	Sandbox       bool
	User          UserProfile
	Orgs          []Organization
	Notifications []NotificationPref
	Sessions      []Session
	Passkeys      []Passkey
	TwoFactor     bool
	PasswordAge   string
}

// AccountFixture returns the signed-in user, their organizations and
// personal preferences.
func AccountFixture(sandbox bool) AccountData {
	return AccountData{
		Sandbox: sandbox,
		User: UserProfile{
			Name:     "Alex Shalaiev",
			Email:    "alex@northwind.dev",
			Initials: "AS",
			Role:     "Owner",
			Language: "en",
			Timezone: "Europe/Berlin",
			Phone:    "+49 •••• 4471",
		},
		Orgs: []Organization{
			{Name: "Northwind Labs", Plan: "Business plan", Initials: "NL", Role: "Owner", Current: true},
			{Name: "Northwind Staging", Plan: "Developer plan", Initials: "NS", Role: "Admin"},
			{Name: "Orbit Media Ltd", Plan: "Starter plan", Initials: "OM", Role: "Finance"},
		},
		Notifications: []NotificationPref{
			{Group: "Payments", Label: "Payment received", Desc: "Every successful payment", Email: false, Push: true, Slack: true},
			{Group: "Payments", Label: "Payment failed or expired", Desc: "Failures, underpayments and expired invoices", Email: true, Push: true, Slack: true},
			{Group: "Payments", Label: "Large payment", Desc: "Payments above $10,000", Email: true, Push: true, Slack: false},
			{Group: "Payouts", Label: "Payout sent", Desc: "When a payout leaves your balance", Email: true, Push: false, Slack: true},
			{Group: "Payouts", Label: "Payout failed", Desc: "Bank rejections and returned transfers", Email: true, Push: true, Slack: true},
			{Group: "Developers", Label: "Webhook endpoint failing", Desc: "After 3 consecutive failed deliveries", Email: true, Push: false, Slack: true},
			{Group: "Developers", Label: "API key used from a new IP", Desc: "First request from an unseen address", Email: true, Push: true, Slack: false},
			{Group: "Account", Label: "New sign-in", Desc: "Sign-ins from a new device or location", Email: true, Push: true, Slack: false},
			{Group: "Account", Label: "Product updates", Desc: "New features and API changes, monthly", Email: true, Push: false, Slack: false},
		},
		Sessions: []Session{
			{Device: "MacBook Pro", Browser: "Safari 26", Location: "Berlin, DE", IP: "84.132.•••.12", Last: "Now", Current: true},
			{Device: "iPhone 17", Browser: "PayChain app", Location: "Berlin, DE", IP: "31.18.•••.201", Last: "1 h ago"},
			{Device: "Windows PC", Browser: "Chrome 141", Location: "Munich, DE", IP: "62.216.•••.77", Last: "Sep 14"},
		},
		Passkeys: []Passkey{
			{Name: "MacBook Pro · Touch ID", Created: "Feb 3, 2026", Last: "Now"},
			{Name: "iPhone 17 · Face ID", Created: "Feb 3, 2026", Last: "1 h ago"},
		},
		TwoFactor:   true,
		PasswordAge: "4 months ago",
	}
}
