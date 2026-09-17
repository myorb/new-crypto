package dashboard

// CustomerStatus is the state of a customer record.
type CustomerStatus string

const (
	CustomerActive  CustomerStatus = "active"
	CustomerNew     CustomerStatus = "new"
	CustomerBlocked CustomerStatus = "blocked"
)

// Customer is one row of the customers table.
type Customer struct {
	ID       string
	Name     string
	Email    string
	Initials string
	Country  string
	Payments int
	Volume   string
	Last     string
	Asset    string
	Status   CustomerStatus
	Since    string
}

// CustomersData is everything the customers page renders.
type CustomersData struct {
	Sandbox   bool
	Stats     []Stat
	Customers []Customer
	Total     int
}

// CustomersFixture returns the live or sandbox customers.
func CustomersFixture(sandbox bool) CustomersData {
	rows := []Customer{
		{ID: "cus_8Ka2sLq9", Name: "Acme Retail GmbH", Email: "billing@acme-retail.de", Initials: "AR", Country: "Germany", Payments: 148, Volume: "$212,480.00", Last: "2 min ago", Asset: "USDT", Status: CustomerActive, Since: "Jan 2025"},
		{ID: "cus_3Nz7bTr4", Name: "Bloom & Co.", Email: "finance@bloomco.io", Initials: "BC", Country: "United States", Payments: 96, Volume: "$188,120.40", Last: "1 h ago", Asset: "USDT", Status: CustomerActive, Since: "Mar 2025"},
		{ID: "cus_5Qw1mPk8", Name: "Orbit Media Ltd", Email: "ap@orbitmedia.co", Initials: "OM", Country: "United Kingdom", Payments: 41, Volume: "$96,300.00", Last: "26 min ago", Asset: "USDC", Status: CustomerActive, Since: "Jun 2025"},
		{ID: "cus_9Ht4vCx2", Name: "Helix Games", Email: "pay@helixgames.gg", Initials: "HG", Country: "Singapore", Payments: 63, Volume: "$74,910.22", Last: "3 h ago", Asset: "SOL", Status: CustomerActive, Since: "Sep 2025"},
		{ID: "cus_1Rd6nJy5", Name: "Nordic Supply AB", Email: "invoices@nordicsupply.se", Initials: "NS", Country: "Sweden", Payments: 28, Volume: "$61,200.00", Last: "4 h ago", Asset: "USDC", Status: CustomerActive, Since: "Nov 2025"},
		{ID: "cus_6Lw8gKb3", Name: "Lena Hoffmann", Email: "lena.h@proton.me", Initials: "LH", Country: "Austria", Payments: 12, Volume: "$9,840.10", Last: "9 min ago", Asset: "BTC", Status: CustomerActive, Since: "Feb 2026"},
		{ID: "cus_2Pn3eYz7", Name: "Kenji Watanabe", Email: "kenji@wtnb.jp", Initials: "KW", Country: "Japan", Payments: 4, Volume: "$3,105.90", Last: "41 min ago", Asset: "ETH", Status: CustomerNew, Since: "Sep 2026"},
		{ID: "cus_7Fj1uVw6", Name: "Pixel Foundry", Email: "ar@pixelfoundry.studio", Initials: "PF", Country: "Canada", Payments: 19, Volume: "$44,720.00", Last: "6 h ago", Asset: "ETH", Status: CustomerActive, Since: "Apr 2025"},
		{ID: "cus_4Gx2wMa9", Name: "Sofia Almeida", Email: "sofia.almeida@gmail.com", Initials: "SA", Country: "Portugal", Payments: 7, Volume: "$2,140.50", Last: "2 h ago", Asset: "TRX", Status: CustomerActive, Since: "Jul 2026"},
		{ID: "cus_0Qa5zPd1", Name: "Amara Okafor", Email: "amara.o@outlook.com", Initials: "AO", Country: "Nigeria", Payments: 2, Volume: "$180.00", Last: "8 h ago", Asset: "BTC", Status: CustomerBlocked, Since: "Sep 2026"},
		{ID: "cus_5Yb8kLh4", Name: "Greenfield Organics", Email: "pay@greenfield.farm", Initials: "GO", Country: "Netherlands", Payments: 33, Volume: "$38,950.00", Last: "Yesterday", Asset: "USDC", Status: CustomerActive, Since: "Aug 2025"},
		{ID: "cus_3Hd1sNe2", Name: "Marcus Lindqvist", Email: "marcus@lindqvist.io", Initials: "ML", Country: "Sweden", Payments: 1, Volume: "$639.94", Last: "5 h ago", Asset: "USDT", Status: CustomerNew, Since: "Sep 2026"},
	}
	for i := range rows {
		rows[i].ID = tid(sandbox, rows[i].ID)
		if sandbox {
			rows[i].Name = "Test customer " + string(rune('A'+i))
			rows[i].Email = "test+" + string(rune('a'+i)) + "@example.com"
			rows[i].Initials = "T" + string(rune('A'+i))
		}
	}
	d := CustomersData{
		Sandbox:   sandbox,
		Customers: rows,
		Total:     1_864,
		Stats: []Stat{
			{Label: "Customers", Value: "1,864", Change: "+6.2%", Trend: TrendUp, Hint: "Paid at least once"},
			{Label: "New this month", Value: "142", Change: "+11.0%", Trend: TrendUp, Hint: "vs. 128 in August"},
			{Label: "Repeat rate", Value: "38.4%", Change: "+2.3 pt", Trend: TrendUp, Hint: "Paid 2+ times"},
			{Label: "Avg. lifetime value", Value: "$689.00", Change: "-0.8%", Trend: TrendDown, Hint: "Median $142.00"},
		},
	}
	if sandbox {
		d.Total = 12
		d.Stats = []Stat{
			{Label: "Customers", Value: "12", Hint: "Test customers"},
			{Label: "New this month", Value: "3", Hint: "Simulated"},
			{Label: "Repeat rate", Value: "41.7%", Hint: "Paid 2+ times"},
			{Label: "Avg. lifetime value", Value: "$1,525.00", Hint: "Simulated"},
		}
	}
	return d
}

func customerStatusClass(s CustomerStatus) string {
	switch s {
	case CustomerActive:
		return pillOK
	case CustomerNew:
		return pillInfo
	case CustomerBlocked:
		return pillBad
	}
	return pillNeutral
}
