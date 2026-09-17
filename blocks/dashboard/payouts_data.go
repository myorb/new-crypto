package dashboard

// PayoutStatus is the state of one payout.
type PayoutStatus string

const (
	PayoutPaid      PayoutStatus = "paid"
	PayoutInTransit PayoutStatus = "in transit"
	PayoutPending   PayoutStatus = "pending"
	PayoutFailed    PayoutStatus = "failed"
)

// Payout is one row of the payouts history.
type Payout struct {
	ID          string
	Destination string
	DestHint    string
	Amount      string
	Fee         string
	Method      string // e.g. "SEPA", "Wire", "USDC on-chain"
	Status      PayoutStatus
	Initiated   string
	Arrival     string
	Auto        bool
}

// Destination is a bank account or external wallet payouts go to.
type Destination struct {
	Name     string
	Detail   string // bank + last4 or truncated address
	Currency string
	Kind     string // "bank" or "wallet"
	Default  bool
	Verified bool
}

// Schedule describes the auto-payout rule.
type Schedule struct {
	Enabled   bool
	Frequency string
	Threshold string
	Progress  int
	Progress0 string
	Next      string
	MinAmount string
}

// PayoutsData is everything the payouts page renders.
type PayoutsData struct {
	Sandbox       bool
	Available     string
	Pending       string
	PendingHint   string
	InTransit     string
	InTransitHint string
	PaidYTD       string
	Schedule      Schedule
	Destinations  []Destination
	Payouts       []Payout
	Total         int
}

// PayoutsFixture returns the live or sandbox payouts data.
func PayoutsFixture(sandbox bool) PayoutsData {
	rows := []Payout{
		{ID: "po_7Kd2sLm9", Destination: "Chase Business", DestHint: "•••• 4821 · USD", Amount: "$61,150.30", Fee: "$0.00", Method: "ACH", Status: PayoutInTransit, Initiated: "Sep 17, 09:00", Arrival: "Sep 19", Auto: true},
		{ID: "po_3Fq8nPr4", Destination: "Chase Business", DestHint: "•••• 4821 · USD", Amount: "$74,880.00", Fee: "$0.00", Method: "ACH", Status: PayoutPaid, Initiated: "Sep 12, 09:00", Arrival: "Sep 15", Auto: true},
		{ID: "po_9Wz1cTb6", Destination: "Treasury wallet", DestHint: "0x7a3f…c21e · USDC", Amount: "25,000.00 USDC", Fee: "$1.20", Method: "On-chain · Arc", Status: PayoutPaid, Initiated: "Sep 10, 14:32", Arrival: "Sep 10", Auto: false},
		{ID: "po_2Hs5vNk3", Destination: "Chase Business", DestHint: "•••• 4821 · USD", Amount: "$75,010.55", Fee: "$0.00", Method: "ACH", Status: PayoutPaid, Initiated: "Sep 5, 09:00", Arrival: "Sep 8", Auto: true},
		{ID: "po_6Ly4mQd8", Destination: "Revolut Business", DestHint: "DE89 •••• 3000 · EUR", Amount: "€18,400.00", Fee: "$12.50", Method: "SEPA", Status: PayoutFailed, Initiated: "Sep 3, 11:15", Arrival: "—", Auto: false},
		{ID: "po_8Pn0kRx2", Destination: "Chase Business", DestHint: "•••• 4821 · USD", Amount: "$72,300.00", Fee: "$0.00", Method: "ACH", Status: PayoutPaid, Initiated: "Aug 29, 09:00", Arrival: "Sep 1", Auto: true},
		{ID: "po_1Vb7tGw5", Destination: "Revolut Business", DestHint: "DE89 •••• 3000 · EUR", Amount: "€18,400.00", Fee: "$12.50", Method: "SEPA", Status: PayoutPaid, Initiated: "Aug 26, 16:40", Arrival: "Aug 27", Auto: false},
		{ID: "po_4Jc9dHe1", Destination: "Chase Business", DestHint: "•••• 4821 · USD", Amount: "$68,915.20", Fee: "$0.00", Method: "ACH", Status: PayoutPaid, Initiated: "Aug 22, 09:00", Arrival: "Aug 25", Auto: true},
	}
	for i := range rows {
		rows[i].ID = tid(sandbox, rows[i].ID)
	}
	d := PayoutsData{
		Sandbox:       sandbox,
		Available:     "$48,210.30",
		Pending:       "$12,940.00",
		PendingHint:   "Settles in ~2h",
		InTransit:     "$61,150.30",
		InTransitHint: "Arrives Fri, Sep 19",
		PaidYTD:       "$2,418,300.00",
		Schedule: Schedule{
			Enabled:   true,
			Frequency: "Weekly · Fridays",
			Threshold: "$75,000",
			Progress:  81,
			Progress0: "$61,150 of $75,000",
			Next:      "Fri, Sep 19",
			MinAmount: "$1,000",
		},
		Destinations: []Destination{
			{Name: "Chase Business", Detail: "Checking •••• 4821", Currency: "USD", Kind: "bank", Default: true, Verified: true},
			{Name: "Revolut Business", Detail: "DE89 3704 •••• 3000", Currency: "EUR", Kind: "bank", Verified: true},
			{Name: "Treasury wallet", Detail: "0x7a3f…c21e · Arc", Currency: "USDC", Kind: "wallet", Verified: true},
			{Name: "Cold storage", Detail: "bc1q…x8k2 · Bitcoin", Currency: "BTC", Kind: "wallet", Verified: false},
		},
		Payouts: rows,
		Total:   142,
	}
	if sandbox {
		d.Available = "$1,000.00"
		d.Pending = "$240.00"
		d.PendingHint = "Simulated settlement"
		d.InTransit = "$0.00"
		d.InTransitHint = "Payouts are simulated"
		d.PaidYTD = "$0.00"
		d.Schedule = Schedule{Enabled: true, Frequency: "Daily", Threshold: "$10,000", Progress: 12, Progress0: "$1,240 of $10,000", Next: "Simulated", MinAmount: "$100"}
		d.Destinations = []Destination{
			{Name: "Test bank", Detail: "Checking •••• 0000", Currency: "USD", Kind: "bank", Default: true, Verified: true},
			{Name: "Test wallet", Detail: "0x0000…0000 · Arc", Currency: "USDC", Kind: "wallet", Verified: true},
		}
		d.Total = 8
	}
	return d
}

func payoutStatusClass(s PayoutStatus) string {
	switch s {
	case PayoutPaid:
		return pillOK
	case PayoutInTransit:
		return pillInfo
	case PayoutPending:
		return pillWarn
	case PayoutFailed:
		return pillBad
	}
	return pillNeutral
}
