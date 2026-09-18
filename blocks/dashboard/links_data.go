package dashboard

// LinkStatus is the state of a payment link.
type LinkStatus string

const (
	LinkActive  LinkStatus = "active"
	LinkPaused  LinkStatus = "paused"
	LinkExpired LinkStatus = "expired"
)

// PaymentLink is one hosted checkout link.
type PaymentLink struct {
	ID       string
	Ref      string // real id, submitted by the row actions
	Name     string
	URL      string
	Amount   string // "" for customer-chosen amounts
	Assets   []string
	Views    int
	Paid     int
	Revenue  string
	Status   LinkStatus
	Created  string
	Expires  string
	Reusable bool
}

// Conversion is paid/views as a percentage.
func (l PaymentLink) Conversion() float64 {
	if l.Views == 0 {
		return 0
	}
	return float64(l.Paid) / float64(l.Views) * 100
}

// LinksData is everything the payment links page renders.
type LinksData struct {
	Sandbox    bool
	Stats      []Stat
	Links      []PaymentLink
	Total      int
	Host       string
	Status     string   // the status filter in force ("all", "active", ...)
	Currencies []Option // pricing currencies offered in the create dialog
	Assets     []Option // assets a link can accept; Value is the asset id
	Error      string   // result of the last form post
	Notice     string
}

// currencyChoices and assetChoices are the fallbacks the fixtures use when a
// handler passes no catalog of its own.
func currencyChoices() []Option { return currencyOptions }

func assetChoices() []Option {
	out := make([]Option, 0, len(assetOptions)-1)
	for _, a := range assetOptions[1:] {
		out = append(out, Option{Value: a.Value, Label: a.Label})
	}
	return out
}

// LinksFixture returns the live or sandbox payment links.
func LinksFixture(sandbox bool) LinksData {
	host := "pay.northwind.dev"
	if sandbox {
		host = "sandbox.pay.northwind.dev"
	}
	links := []PaymentLink{
		{ID: "plink_7Ha2sKq9", Name: "Pro plan · annual", Amount: "$1,188.00", Assets: []string{"USDT", "USDC", "BTC", "ETH"}, Views: 1240, Paid: 312, Revenue: "$370,656.00", Status: LinkActive, Created: "Aug 2", Expires: "Never", Reusable: true},
		{ID: "plink_2Qz8mLp4", Name: "Starter plan · monthly", Amount: "$49.00", Assets: []string{"USDT", "USDC", "SOL"}, Views: 3880, Paid: 1462, Revenue: "$71,638.00", Status: LinkActive, Created: "Jul 14", Expires: "Never", Reusable: true},
		{ID: "plink_9Vb3nRt1", Name: "Invoice #2026-0912 · Orbit Media", Amount: "$8,000.00", Assets: []string{"USDC"}, Views: 3, Paid: 1, Revenue: "$8,000.00", Status: LinkExpired, Created: "Sep 12", Expires: "Sep 15", Reusable: false},
		{ID: "plink_4Ck6wYe7", Name: "Donation · open amount", Amount: "", Assets: []string{"BTC", "ETH", "USDT", "USDC", "SOL", "TRX"}, Views: 920, Paid: 188, Revenue: "$12,404.10", Status: LinkActive, Created: "Jun 30", Expires: "Never", Reusable: true},
		{ID: "plink_1Xn5pDs3", Name: "Workshop ticket · Berlin", Amount: "$240.00", Assets: []string{"USDT", "USDC"}, Views: 615, Paid: 97, Revenue: "$23,280.00", Status: LinkPaused, Created: "Aug 21", Expires: "Oct 1", Reusable: true},
		{ID: "plink_6Ry0tGh2", Name: "Hardware wallet bundle", Amount: "$189.00", Assets: []string{"BTC", "USDT"}, Views: 2104, Paid: 411, Revenue: "$77,679.00", Status: LinkActive, Created: "May 9", Expires: "Never", Reusable: true},
		{ID: "plink_8Lm4qFv5", Name: "Invoice #2026-0904 · Bloom & Co.", Amount: "$12,300.00", Assets: []string{"USDT"}, Views: 2, Paid: 1, Revenue: "$12,300.00", Status: LinkExpired, Created: "Sep 4", Expires: "Sep 6", Reusable: false},
		{ID: "plink_3Tw9kJb8", Name: "Consulting retainer · Sep", Amount: "$4,500.00", Assets: []string{"USDC", "ETH"}, Views: 6, Paid: 0, Revenue: "$0.00", Status: LinkActive, Created: "Sep 15", Expires: "Sep 30", Reusable: false},
	}
	for i := range links {
		links[i].ID = tid(sandbox, links[i].ID)
		links[i].URL = "https://" + host + "/l/" + links[i].ID[len("plink_"):]
		if sandbox {
			links[i].Views /= 10
			links[i].Paid /= 10
		}
	}
	d := LinksData{
		Sandbox:    sandbox,
		Links:      links,
		Total:      38,
		Host:       host,
		Status:     "all",
		Currencies: currencyChoices(),
		Assets:     assetChoices(),
		Stats: []Stat{
			{Label: "Active links", Value: "23", Change: "+3", Trend: TrendUp, Hint: "5 expire this month"},
			{Label: "Paid via links", Value: "2,471", Change: "+14.2%", Trend: TrendUp, Hint: "Last 30 days"},
			{Label: "Conversion", Value: "27.6%", Change: "+1.9 pt", Trend: TrendUp, Hint: "Views → paid"},
			{Label: "Revenue via links", Value: "$575,957.10", Change: "+18.0%", Trend: TrendUp, Hint: "44.8% of gross volume"},
		},
	}
	if sandbox {
		d.Total = 8
		d.Stats = []Stat{
			{Label: "Active links", Value: "5", Change: "", Hint: "Test links only"},
			{Label: "Paid via links", Value: "247", Change: "+2.1%", Trend: TrendUp, Hint: "Simulated"},
			{Label: "Conversion", Value: "27.6%", Change: "0.0 pt", Trend: TrendFlat, Hint: "Views → paid"},
			{Label: "Revenue via links", Value: "$7,420.00", Change: "+1.0%", Trend: TrendUp, Hint: "No funds move"},
		}
	}
	return d
}

func linkStatusClass(s LinkStatus) string {
	switch s {
	case LinkActive:
		return pillOK
	case LinkPaused:
		return pillWarn
	}
	return pillNeutral
}
