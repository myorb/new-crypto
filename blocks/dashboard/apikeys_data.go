package dashboard

// KeyType is the class of an API key.
type KeyType string

const (
	KeyPublishable KeyType = "publishable"
	KeySecret      KeyType = "secret"
	KeyRestricted  KeyType = "restricted"
)

// APIKey is one row of the keys table.
type APIKey struct {
	ID       string
	Name     string
	Token    string // masked
	Type     KeyType
	Scopes   []string
	Created  string
	LastUsed string
	Requests string // last 24h
	Revoked  bool
}

// Scope is one permission a restricted key can hold.
type Scope struct {
	Key   string
	Label string
	Desc  string
}

// APIKeysData is everything the API keys page renders.
type APIKeysData struct {
	Sandbox    bool
	Mode       string // "live" or "test"
	Keys       []APIKey
	Scopes     []Scope
	APIVersion string
	Versions   []option
	Requests   []float64 // requests per day, 14d
	Requests24 string
	ErrorRate  string
	RateLimit  string
	RateUsed   int
}

// APIKeysFixture returns the live or sandbox keys.
func APIKeysFixture(sandbox bool) APIKeysData {
	mode := "live"
	if sandbox {
		mode = "test"
	}
	keys := []APIKey{
		{ID: "key_1", Name: "Default publishable", Token: "pk_" + mode + "_51HxQ9kZ2mN8vT4rL7pC3sD1f", Type: KeyPublishable, Scopes: []string{"checkout:create"}, Created: "Jan 12, 2025", LastUsed: "1 min ago", Requests: "12,480"},
		{ID: "key_2", Name: "Default secret", Token: "sk_" + mode + "_••••••••••••••••••••4f2a", Type: KeySecret, Scopes: []string{"all"}, Created: "Jan 12, 2025", LastUsed: "3 min ago", Requests: "48,102"},
		{ID: "key_3", Name: "Shopify connector", Token: "rk_" + mode + "_••••••••••••••••••••9c1d", Type: KeyRestricted, Scopes: []string{"payments:read", "payments:write", "webhooks:read"}, Created: "Mar 4, 2025", LastUsed: "12 min ago", Requests: "6,914"},
		{ID: "key_4", Name: "Finance reporting", Token: "rk_" + mode + "_••••••••••••••••••••e77b", Type: KeyRestricted, Scopes: []string{"payments:read", "payouts:read", "balances:read"}, Created: "Jun 18, 2025", LastUsed: "2 h ago", Requests: "312"},
		{ID: "key_5", Name: "Zapier (old)", Token: "rk_" + mode + "_••••••••••••••••••••02aa", Type: KeyRestricted, Scopes: []string{"payments:read"}, Created: "Sep 2, 2024", LastUsed: "Mar 30, 2026", Requests: "0", Revoked: true},
	}
	d := APIKeysData{
		Sandbox:    sandbox,
		Mode:       mode,
		Keys:       keys,
		APIVersion: "2026-09-01",
		Versions: []option{
			{Value: "2026-09-01", Label: "2026-09-01 · current"},
			{Value: "2026-03-15", Label: "2026-03-15"},
			{Value: "2025-11-01", Label: "2025-11-01"},
		},
		Scopes: []Scope{
			{Key: "payments:read", Label: "Payments · read", Desc: "List and retrieve payments"},
			{Key: "payments:write", Label: "Payments · write", Desc: "Create payments and refunds"},
			{Key: "links:write", Label: "Payment links · write", Desc: "Create and edit hosted links"},
			{Key: "payouts:read", Label: "Payouts · read", Desc: "List payouts and destinations"},
			{Key: "payouts:write", Label: "Payouts · write", Desc: "Request payouts"},
			{Key: "balances:read", Label: "Balances · read", Desc: "Read wallet balances"},
			{Key: "customers:write", Label: "Customers · write", Desc: "Create and update customers"},
			{Key: "webhooks:read", Label: "Webhooks · read", Desc: "Inspect endpoints and deliveries"},
		},
		Requests:   []float64{61, 64, 59, 70, 72, 68, 75, 79, 74, 81, 86, 83, 88, 92},
		Requests24: "67,808",
		ErrorRate:  "0.14%",
		RateLimit:  "1,000 req/min",
		RateUsed:   23,
	}
	if sandbox {
		d.Requests = []float64{3, 4, 2, 6, 5, 8, 7, 9, 6, 11, 10, 12, 9, 14}
		d.Requests24 = "1,284"
		d.ErrorRate = "2.10%"
		d.RateLimit = "100 req/min"
		d.RateUsed = 4
	}
	return d
}

func keyTypeClass(t KeyType) string {
	switch t {
	case KeyPublishable:
		return pillInfo
	case KeySecret:
		return pillWarn
	}
	return pillNeutral
}
