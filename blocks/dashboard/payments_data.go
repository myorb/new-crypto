package dashboard

// PaymentsData is everything the full payments page renders.
type PaymentsData struct {
	Sandbox bool
	Period  string
	Stats   []Stat
	Rows    []Payment
	Total   int
	// Counts per status for the filter tabs.
	Counts map[Status]int
}

// PaymentsFixture returns the live or sandbox payments list.
func PaymentsFixture(sandbox bool) PaymentsData {
	rows := []Payment{
		{ID: "pay_3Kq9fT2mL8", Customer: "Acme Retail GmbH", Email: "billing@acme-retail.de", Asset: "USDT", Network: "Tron", Amount: "2,450.00 USDT", Fiat: "$2,449.76", Fee: "$6.12", Confirmations: "19/19", Status: StatusSucceeded, Time: "2 min ago"},
		{ID: "pay_8Hs2vN1pQ4", Customer: "Lena Hoffmann", Email: "lena.h@proton.me", Asset: "BTC", Network: "Bitcoin", Amount: "0.0182 BTC", Fiat: "$1,213.58", Fee: "$3.03", Confirmations: "1/3", Status: StatusPending, Time: "9 min ago"},
		{ID: "pay_5Tz7bR4kW2", Customer: "Orbit Media Ltd", Email: "ap@orbitmedia.co", Asset: "USDC", Network: "Arc", Amount: "8,000.00 USDC", Fiat: "$8,000.00", Fee: "$20.00", Confirmations: "1/1", Status: StatusSucceeded, Time: "26 min ago"},
		{ID: "pay_1Mv4cX9sD7", Customer: "Kenji Watanabe", Email: "kenji@wtnb.jp", Asset: "ETH", Network: "Ethereum", Amount: "0.7400 ETH", Fiat: "$1,920.60", Fee: "—", Confirmations: "0/12", Status: StatusFailed, Time: "41 min ago"},
		{ID: "pay_9Rd6nJ3hF5", Customer: "Bloom & Co.", Email: "finance@bloomco.io", Asset: "USDT", Network: "Ethereum", Amount: "12,300.00 USDT", Fiat: "$12,298.77", Fee: "$30.75", Confirmations: "12/12", Status: StatusSucceeded, Time: "1 h ago"},
		{ID: "pay_2Lw8gK5tB9", Customer: "Sofia Almeida", Email: "sofia.almeida@gmail.com", Asset: "TRX", Network: "Tron", Amount: "1,480.00 TRX", Fiat: "$495.06", Fee: "$1.24", Confirmations: "19/19", Status: StatusRefunded, Time: "2 h ago"},
		{ID: "pay_7Pn3eY8qZ1", Customer: "Helix Games", Email: "pay@helixgames.gg", Asset: "SOL", Network: "Solana", Amount: "18.20 SOL", Fiat: "$3,028.84", Fee: "$7.57", Confirmations: "32/32", Status: StatusSucceeded, Time: "3 h ago"},
		{ID: "pay_4Fj1uV6rC3", Customer: "Nordic Supply AB", Email: "invoices@nordicsupply.se", Asset: "USDC", Network: "Solana", Amount: "5,600.00 USDC", Fiat: "$5,600.00", Fee: "$14.00", Confirmations: "32/32", Status: StatusSucceeded, Time: "4 h ago"},
		{ID: "pay_6Gx2wM8nA5", Customer: "Marcus Lindqvist", Email: "marcus@lindqvist.io", Asset: "USDT", Network: "Tron", Amount: "640.00 USDT", Fiat: "$639.94", Fee: "$1.60", Confirmations: "6/19", Status: StatusPending, Time: "5 h ago"},
		{ID: "pay_0Qa5zP7rE2", Customer: "Pixel Foundry", Email: "ar@pixelfoundry.studio", Asset: "ETH", Network: "Ethereum", Amount: "2.1000 ETH", Fiat: "$5,450.34", Fee: "$13.63", Confirmations: "12/12", Status: StatusSucceeded, Time: "6 h ago"},
		{ID: "pay_5Yb8kL2vT6", Customer: "Amara Okafor", Email: "amara.o@outlook.com", Asset: "BTC", Network: "Bitcoin", Amount: "0.0050 BTC", Fiat: "$333.40", Fee: "—", Confirmations: "0/3", Status: StatusFailed, Time: "8 h ago"},
		{ID: "pay_3Hd1sN9wK4", Customer: "Greenfield Organics", Email: "pay@greenfield.farm", Asset: "USDC", Network: "Arc", Amount: "1,150.00 USDC", Fiat: "$1,150.00", Fee: "$2.88", Confirmations: "1/1", Status: StatusSucceeded, Time: "Yesterday"},
	}
	for i := range rows {
		rows[i].ID = tid(sandbox, rows[i].ID)
		rows[i].Color = assetColor(rows[i].Asset)
		if sandbox {
			rows[i].Customer = "Test customer " + string(rune('A'+i))
			rows[i].Email = "test+" + string(rune('a'+i)) + "@example.com"
		}
	}
	counts := map[Status]int{}
	for _, r := range rows {
		counts[r.Status]++
	}
	d := PaymentsData{
		Sandbox: sandbox,
		Period:  "Last 30 days",
		Rows:    rows,
		Counts:  counts,
		Total:   4812,
		Stats: []Stat{
			{Label: "Gross volume", Value: "$1,284,320.55", Change: "+12.4%", Trend: TrendUp, Hint: "Last 30 days"},
			{Label: "Payments", Value: "4,812", Change: "+8.1%", Trend: TrendUp, Hint: "312 today"},
			{Label: "Average payment", Value: "$266.90", Change: "+3.9%", Trend: TrendUp, Hint: "Median $84.20"},
			{Label: "Refunded", Value: "$18,410.00", Change: "-1.2%", Trend: TrendDown, Hint: "61 refunds · 1.4% of volume"},
		},
	}
	if sandbox {
		d.Total = 612
		d.Stats = []Stat{
			{Label: "Gross volume", Value: "$18,420.00", Change: "+3.2%", Trend: TrendUp, Hint: "Simulated · last 30 days"},
			{Label: "Payments", Value: "612", Change: "+1.8%", Trend: TrendUp, Hint: "48 today"},
			{Label: "Average payment", Value: "$30.10", Change: "0.0%", Trend: TrendFlat, Hint: "Median $25.00"},
			{Label: "Refunded", Value: "$240.00", Change: "0.0%", Trend: TrendFlat, Hint: "8 refunds"},
		}
	}
	return d
}

// filterPayments returns the rows matching status, or all rows when status
// is empty.
func filterPayments(rows []Payment, status Status) []Payment {
	if status == "" {
		return rows
	}
	out := make([]Payment, 0, len(rows))
	for _, r := range rows {
		if r.Status == status {
			out = append(out, r)
		}
	}
	return out
}

var paymentTabs = []struct {
	Key    string
	Label  string
	Status Status
}{
	{Key: "all", Label: "All"},
	{Key: "succeeded", Label: "Succeeded", Status: StatusSucceeded},
	{Key: "pending", Label: "Pending", Status: StatusPending},
	{Key: "failed", Label: "Failed", Status: StatusFailed},
	{Key: "refunded", Label: "Refunded", Status: StatusRefunded},
}

func (d PaymentsData) tabCount(s Status) int {
	if s == "" {
		return len(d.Rows)
	}
	return d.Counts[s]
}
