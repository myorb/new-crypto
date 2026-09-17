// Package dashboard is the merchant overview of a crypto payment platform:
// KPIs, volume charts, asset and network mix, settlement, balances and the
// latest payments. Everything renders from Data so a handler can replace
// SampleData() with real rows without touching the templates.
package dashboard

import (
	"math"
	"math/rand"
	"time"

	"templ-app/components/chart"
)

// Trend is the direction of a value compared to the previous period.
type Trend string

const (
	TrendUp   Trend = "up"
	TrendDown Trend = "down"
	TrendFlat Trend = "flat"
)

// KPI is one stat tile in the top row.
type KPI struct {
	Label  string
	Value  string
	Change string // e.g. "+12.4%"
	Trend  Trend
	Hint   string // muted footer text, e.g. "vs. previous 30 days"
	Spark  []float64
	Invert bool // true when a downward trend is good (e.g. failures)
}

// Range is one selectable period of the volume chart.
type Range struct {
	Key    string // tab value, e.g. "30d"
	Label  string // tab label, e.g. "30 days"
	Series []chart.Series
	Total  string
	Change string
	Trend  Trend
}

// AssetShare is one slice of the volume-by-asset donut.
type AssetShare struct {
	Symbol string
	Name   string
	Volume float64
	Fiat   string
	Color  string // CSS color, e.g. "var(--chart-1)"
}

// NetworkShare is one row of the payments-by-network list.
type NetworkShare struct {
	Name     string
	Payments int
	Share    float64 // 0..100
	Color    string
}

// Settlement is the payouts card.
type Settlement struct {
	Available       string
	Pending         string
	PendingHint     string
	InTransit       string
	Fees            string
	FeesHint        string
	NextPayoutDate  string
	NextPayoutTo    string
	ThresholdValue  int // percent towards the auto-payout threshold
	ThresholdLabel  string
	AutoConvertNote string
}

// Balance is one row in the wallet balances card.
type Balance struct {
	Symbol string
	Name   string
	Amount string // native amount, e.g. "41,220.00 USDT"
	Fiat   string // e.g. "$41,215.88"
	Change string // 24h change, e.g. "+1.2%"
	Trend  Trend
	Share  float64 // 0..100 of total balance
	Color  string  // Tailwind background class of the icon
}

// Status is the state of one payment.
type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusPending   Status = "pending"
	StatusFailed    Status = "failed"
	StatusRefunded  Status = "refunded"
)

// Payment is one row of the recent payments table.
type Payment struct {
	ID       string
	Customer string
	Email    string
	Asset    string
	Network  string
	Amount   string // crypto amount
	Fiat     string
	Status   Status
	Time     string
	Color    string // Tailwind background class of the asset icon
	// Fee and Confirmations are shown on the full payments page only.
	Fee           string
	Confirmations string
}

// WebhookEvent is one delivery in the sandbox developer card.
type WebhookEvent struct {
	Type string // e.g. "payment.succeeded"
	ID   string
	Time string
	OK   bool
}

// Developer is the sandbox tooling: test keys, webhook health and the
// latest deliveries.
type Developer struct {
	PublishableKey string
	SecretKey      string // masked
	APIVersion     string
	WebhookURL     string
	Delivered      int
	Failed         int
	Events         []WebhookEvent
}

// Data is everything the overview renders.
type Data struct {
	MerchantName string
	Initials     string
	Period       string // e.g. "Last 30 days"
	Today        string // e.g. "Thu, Sep 17"
	// Sandbox is true when the merchant is viewing simulated test data.
	Sandbox   bool
	Developer Developer

	KPIs        []KPI
	Ranges      []Range
	Assets      []AssetShare
	Networks    []NetworkShare
	StatusByDay []chart.Group
	Settlement  Settlement
	Balances    []Balance
	Payments    []Payment
	TotalCount  int
}

// TotalAssetVolume sums the donut slices.
func (d Data) TotalAssetVolume() float64 {
	sum := 0.0
	for _, a := range d.Assets {
		sum += a.Volume
	}
	return sum
}

// Fixture returns the live or sandbox preview data.
func Fixture(sandbox bool) Data {
	if sandbox {
		return SandboxData()
	}
	return SampleData()
}

var today = time.Date(2026, time.September, 17, 15, 0, 0, 0, time.UTC)

// SampleData returns a deterministic fixture for previews.
func SampleData() Data {
	daily := dailyVolume(today, 180, 20260917, 1)

	return Data{
		MerchantName: "Northwind Labs",
		Initials:     "NL",
		Period:       "Last 30 days",
		Today:        today.Format("Mon, Jan 2"),
		KPIs: []KPI{
			{Label: "Volume processed", Value: "$1,284,320.55", Change: "+12.4%", Trend: TrendUp, Hint: "vs. $1,142,610 previous 30 days", Spark: tail(daily, 30)},
			{Label: "Payments", Value: "4,812", Change: "+8.1%", Trend: TrendUp, Hint: "312 today · avg. $266.90", Spark: scale(tail(daily, 30), 1.0/266.9)},
			{Label: "Settled to bank", Value: "$912,400.00", Change: "+5.3%", Trend: TrendUp, Hint: "Next payout Fri, Sep 19", Spark: []float64{58, 61, 57, 63, 66, 64, 69, 71, 68, 74, 76, 79, 77, 82}},
			{Label: "Success rate", Value: "98.6%", Change: "-0.3 pt", Trend: TrendDown, Hint: "68 failed · 21 expired", Spark: []float64{98.9, 99.1, 98.8, 99.0, 98.7, 98.9, 98.6, 98.8, 98.5, 98.7, 98.6, 98.4, 98.7, 98.6}},
		},
		Ranges: []Range{
			volumeRange("7d", "7 days", today, daily, 7, "$298,140.20", "+4.6%", TrendUp),
			volumeRange("30d", "30 days", today, daily, 30, "$1,284,320.55", "+12.4%", TrendUp),
			volumeRange("90d", "90 days", today, daily, 90, "$3,611,870.10", "+21.9%", TrendUp),
		},
		Assets: []AssetShare{
			{Symbol: "USDT", Name: "Tether", Volume: 593356, Fiat: "$593,356", Color: "var(--chart-1)"},
			{Symbol: "USDC", Name: "USD Coin", Volume: 287688, Fiat: "$287,688", Color: "var(--chart-2)"},
			{Symbol: "BTC", Name: "Bitcoin", Volume: 228609, Fiat: "$228,609", Color: "var(--chart-3)"},
			{Symbol: "ETH", Name: "Ethereum", Volume: 139991, Fiat: "$139,991", Color: "var(--chart-4)"},
			{Symbol: "TRX", Name: "Tron", Volume: 34676, Fiat: "$34,676", Color: "var(--chart-5)"},
		},
		Networks: []NetworkShare{
			{Name: "Tron", Payments: 1973, Share: 41, Color: "var(--chart-1)"},
			{Name: "Ethereum", Payments: 1155, Share: 24, Color: "var(--chart-2)"},
			{Name: "Arc", Payments: 818, Share: 17, Color: "var(--chart-3)"},
			{Name: "Solana", Payments: 577, Share: 12, Color: "var(--chart-4)"},
			{Name: "Bitcoin", Payments: 289, Share: 6, Color: "var(--chart-5)"},
		},
		StatusByDay: statusByDay(today, 14, 1, 1),
		Settlement: Settlement{
			Available:       "$48,210.30",
			Pending:         "$12,940.00",
			PendingHint:     "Settles in ~2h",
			InTransit:       "$61,150.30",
			Fees:            "$3,210.80",
			FeesHint:        "0.25% effective rate",
			NextPayoutDate:  "Fri, Sep 19",
			NextPayoutTo:    "Chase Business •••• 4821",
			ThresholdValue:  81,
			ThresholdLabel:  "$61,150 of $75,000 auto-payout threshold",
			AutoConvertNote: "65% of incoming crypto auto-converts to USD",
		},
		Balances: []Balance{
			{Symbol: "USDT", Name: "Tether", Amount: "41,220.00 USDT", Fiat: "$41,215.88", Change: "0.0%", Trend: TrendFlat, Share: 42.8, Color: "bg-emerald-500"},
			{Symbol: "USDC", Name: "USD Coin", Amount: "11,880.00 USDC", Fiat: "$11,880.00", Change: "0.0%", Trend: TrendFlat, Share: 12.3, Color: "bg-sky-500"},
			{Symbol: "BTC", Name: "Bitcoin", Amount: "0.4182 BTC", Fiat: "$27,884.12", Change: "+2.1%", Trend: TrendUp, Share: 28.9, Color: "bg-orange-500"},
			{Symbol: "ETH", Name: "Ethereum", Amount: "5.9020 ETH", Fiat: "$15,318.40", Change: "-1.4%", Trend: TrendDown, Share: 15.9, Color: "bg-indigo-500"},
			{Symbol: "TRX", Name: "Tron", Amount: "330.01 TRX", Fiat: "$110.40", Change: "-0.6%", Trend: TrendDown, Share: 0.1, Color: "bg-red-500"},
		},
		Payments: []Payment{
			{ID: "pay_3Kq9fT2mL8", Customer: "Acme Retail GmbH", Email: "billing@acme-retail.de", Asset: "USDT", Network: "Tron", Amount: "2,450.00 USDT", Fiat: "$2,449.76", Status: StatusSucceeded, Time: "2 min ago", Color: "bg-emerald-500"},
			{ID: "pay_8Hs2vN1pQ4", Customer: "Lena Hoffmann", Email: "lena.h@proton.me", Asset: "BTC", Network: "Bitcoin", Amount: "0.0182 BTC", Fiat: "$1,213.58", Status: StatusPending, Time: "9 min ago", Color: "bg-orange-500"},
			{ID: "pay_5Tz7bR4kW2", Customer: "Orbit Media Ltd", Email: "ap@orbitmedia.co", Asset: "USDC", Network: "Arc", Amount: "8,000.00 USDC", Fiat: "$8,000.00", Status: StatusSucceeded, Time: "26 min ago", Color: "bg-sky-500"},
			{ID: "pay_1Mv4cX9sD7", Customer: "Kenji Watanabe", Email: "kenji@wtnb.jp", Asset: "ETH", Network: "Ethereum", Amount: "0.7400 ETH", Fiat: "$1,920.60", Status: StatusFailed, Time: "41 min ago", Color: "bg-indigo-500"},
			{ID: "pay_9Rd6nJ3hF5", Customer: "Bloom & Co.", Email: "finance@bloomco.io", Asset: "USDT", Network: "Ethereum", Amount: "12,300.00 USDT", Fiat: "$12,298.77", Status: StatusSucceeded, Time: "1 h ago", Color: "bg-emerald-500"},
			{ID: "pay_2Lw8gK5tB9", Customer: "Sofia Almeida", Email: "sofia.almeida@gmail.com", Asset: "TRX", Network: "Tron", Amount: "1,480.00 TRX", Fiat: "$495.06", Status: StatusRefunded, Time: "2 h ago", Color: "bg-red-500"},
			{ID: "pay_7Pn3eY8qZ1", Customer: "Helix Games", Email: "pay@helixgames.gg", Asset: "SOL", Network: "Solana", Amount: "18.20 SOL", Fiat: "$3,028.84", Status: StatusSucceeded, Time: "3 h ago", Color: "bg-violet-500"},
			{ID: "pay_4Fj1uV6rC3", Customer: "Nordic Supply AB", Email: "invoices@nordicsupply.se", Asset: "USDC", Network: "Solana", Amount: "5,600.00 USDC", Fiat: "$5,600.00", Status: StatusSucceeded, Time: "4 h ago", Color: "bg-sky-500"},
		},
		TotalCount: 4812,
	}
}

// dailyVolume is a deterministic series of daily USD volume ending today,
// with a gentle upward trend and quieter weekends.
func dailyVolume(today time.Time, days int, seed int64, scale float64) []float64 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float64, days)
	for i := range out {
		day := today.AddDate(0, 0, -(days - 1 - i))
		base := (26000 + 19000*float64(i)/float64(days-1)) * scale
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			base *= 0.62
		}
		wave := 1 + 0.08*math.Sin(float64(i)/5.5)
		noise := 1 + (rng.Float64()-0.5)*0.34
		out[i] = math.Round(base * wave * noise)
	}
	return out
}

func tail(v []float64, n int) []float64 {
	if n >= len(v) {
		return v
	}
	return v[len(v)-n:]
}

func scale(v []float64, k float64) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = math.Round(x * k)
	}
	return out
}

// volumeRange builds the current and previous period series for one tab.
func volumeRange(key, label string, today time.Time, daily []float64, days int, total, change string, trend Trend) Range {
	current := make([]chart.Point, days)
	previous := make([]chart.Point, days)
	n := len(daily)
	for i := 0; i < days; i++ {
		day := today.AddDate(0, 0, -(days - 1 - i))
		current[i] = chart.Point{Label: day.Format("Jan 2"), Value: daily[n-days+i]}
		previous[i] = chart.Point{Label: day.Format("Jan 2"), Value: daily[n-2*days+i]}
	}
	return Range{
		Key:   key,
		Label: label,
		Series: []chart.Series{
			{Name: "This period", Color: "var(--chart-1)", Points: current},
			{Name: "Previous period", Color: "var(--muted-foreground)", Points: previous, Dashed: true, NoFill: true},
		},
		Total:  total,
		Change: change,
		Trend:  trend,
	}
}

// statusByDay is succeeded/pending/failed counts per day.
func statusByDay(today time.Time, days int, scale, failMul float64) []chart.Group {
	rng := rand.New(rand.NewSource(1709))
	out := make([]chart.Group, days)
	for i := range out {
		day := today.AddDate(0, 0, -(days - 1 - i))
		base := (140.0 + 40*float64(i)/float64(days)) * scale
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			base *= 0.6
		}
		ok := math.Round(base * (0.9 + rng.Float64()*0.2))
		pending := math.Round((4 + rng.Float64()*9) * scale)
		failed := math.Round((1 + rng.Float64()*4) * scale * failMul)
		out[i] = chart.Group{Label: day.Format("Jan 2"), Values: []float64{ok, pending, failed}}
	}
	return out
}

// SandboxData is the simulated dataset shown in sandbox mode: test keys,
// test IDs, small balances and deliberately noisier failures.
func SandboxData() Data {
	daily := dailyVolume(today, 180, 4242, 0.0145)
	live := SampleData()
	return Data{
		MerchantName: live.MerchantName,
		Initials:     live.Initials,
		Period:       live.Period,
		Today:        live.Today,
		Sandbox:      true,
		Developer: Developer{
			PublishableKey: "pk_test_51HxQ9kZ2mN8vT4rL7pC3sD1f",
			SecretKey:      "sk_test_••••••••••••••••••••4f2a",
			APIVersion:     "2026-09-01",
			WebhookURL:     "https://api.northwind.dev/webhooks/paychain",
			Delivered:      128,
			Failed:         2,
			Events: []WebhookEvent{
				{Type: "payment.succeeded", ID: "evt_test_9Ab3", Time: "1 min ago", OK: true},
				{Type: "payment.pending", ID: "evt_test_7Zx1", Time: "6 min ago", OK: true},
				{Type: "payout.paid", ID: "evt_test_5Qw8", Time: "22 min ago", OK: true},
				{Type: "payment.failed", ID: "evt_test_2Kd4", Time: "38 min ago", OK: false},
				{Type: "refund.created", ID: "evt_test_1Ls6", Time: "1 h ago", OK: true},
			},
		},
		KPIs: []KPI{
			{Label: "Volume processed", Value: "$18,420.00", Change: "+3.2%", Trend: TrendUp, Hint: "vs. $17,850 previous 30 days · simulated", Spark: tail(daily, 30)},
			{Label: "Payments", Value: "612", Change: "+1.8%", Trend: TrendUp, Hint: "48 today · avg. $30.10", Spark: scale(tail(daily, 30), 1.0/30.1)},
			{Label: "Settled to bank", Value: "$0.00", Change: "0.0%", Trend: TrendFlat, Hint: "Payouts are simulated in sandbox", Spark: []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
			{Label: "Success rate", Value: "91.4%", Change: "-2.1 pt", Trend: TrendDown, Hint: "Includes 52 forced test failures", Spark: []float64{94, 93.5, 92.8, 93.1, 91.9, 92.4, 91.2, 92.0, 90.8, 91.6, 91.9, 90.9, 91.7, 91.4}},
		},
		Ranges: []Range{
			volumeRange("7d", "7 days", today, daily, 7, "$4,310.50", "+2.1%", TrendUp),
			volumeRange("30d", "30 days", today, daily, 30, "$18,420.00", "+3.2%", TrendUp),
			volumeRange("90d", "90 days", today, daily, 90, "$52,180.90", "+9.4%", TrendUp),
		},
		Assets: []AssetShare{
			{Symbol: "USDT", Name: "Tether", Volume: 8290, Fiat: "$8,290", Color: "var(--chart-1)"},
			{Symbol: "USDC", Name: "USD Coin", Volume: 5120, Fiat: "$5,120", Color: "var(--chart-2)"},
			{Symbol: "BTC", Name: "Bitcoin", Volume: 2830, Fiat: "$2,830", Color: "var(--chart-3)"},
			{Symbol: "ETH", Name: "Ethereum", Volume: 1660, Fiat: "$1,660", Color: "var(--chart-4)"},
			{Symbol: "TRX", Name: "Tron", Volume: 520, Fiat: "$520", Color: "var(--chart-5)"},
		},
		Networks: []NetworkShare{
			{Name: "Tron", Payments: 251, Share: 41, Color: "var(--chart-1)"},
			{Name: "Ethereum", Payments: 147, Share: 24, Color: "var(--chart-2)"},
			{Name: "Arc", Payments: 104, Share: 17, Color: "var(--chart-3)"},
			{Name: "Solana", Payments: 73, Share: 12, Color: "var(--chart-4)"},
			{Name: "Bitcoin", Payments: 37, Share: 6, Color: "var(--chart-5)"},
		},
		StatusByDay: statusByDay(today, 14, 0.13, 4),
		Settlement: Settlement{
			Available:       "$1,000.00",
			Pending:         "$240.00",
			PendingHint:     "Simulated settlement",
			InTransit:       "$0.00",
			Fees:            "$46.05",
			FeesHint:        "0.25% effective rate",
			NextPayoutDate:  "Simulated",
			NextPayoutTo:    "Test bank •••• 0000",
			ThresholdValue:  12,
			ThresholdLabel:  "$1,240 of $10,000 test payout threshold",
			AutoConvertNote: "Sandbox balances reset nightly at 00:00 UTC",
		},
		Balances: []Balance{
			{Symbol: "USDT", Name: "Tether", Amount: "10,000.00 USDT", Fiat: "$10,000.00", Change: "0.0%", Trend: TrendFlat, Share: 55, Color: "bg-emerald-500"},
			{Symbol: "USDC", Name: "USD Coin", Amount: "5,000.00 USDC", Fiat: "$5,000.00", Change: "0.0%", Trend: TrendFlat, Share: 27, Color: "bg-sky-500"},
			{Symbol: "BTC", Name: "Bitcoin", Amount: "0.0250 BTC", Fiat: "$1,667.00", Change: "+2.1%", Trend: TrendUp, Share: 9, Color: "bg-orange-500"},
			{Symbol: "ETH", Name: "Ethereum", Amount: "0.5000 ETH", Fiat: "$1,297.70", Change: "-1.4%", Trend: TrendDown, Share: 7, Color: "bg-indigo-500"},
			{Symbol: "TRX", Name: "Tron", Amount: "1,000.00 TRX", Fiat: "$334.50", Change: "-0.6%", Trend: TrendDown, Share: 2, Color: "bg-red-500"},
		},
		Payments: []Payment{
			{ID: "pay_test_3Kq9fT2m", Customer: "Test customer 01", Email: "test+01@example.com", Asset: "USDT", Network: "Tron", Amount: "25.00 USDT", Fiat: "$25.00", Status: StatusSucceeded, Time: "1 min ago", Color: "bg-emerald-500"},
			{ID: "pay_test_8Hs2vN1p", Customer: "Test customer 02", Email: "test+02@example.com", Asset: "BTC", Network: "Bitcoin", Amount: "0.0015 BTC", Fiat: "$100.02", Status: StatusPending, Time: "6 min ago", Color: "bg-orange-500"},
			{ID: "pay_test_5Tz7bR4k", Customer: "Test customer 03", Email: "test+03@example.com", Asset: "USDC", Network: "Arc", Amount: "500.00 USDC", Fiat: "$500.00", Status: StatusSucceeded, Time: "14 min ago", Color: "bg-sky-500"},
			{ID: "pay_test_1Mv4cX9s", Customer: "Test customer 04", Email: "test+04@example.com", Asset: "ETH", Network: "Ethereum", Amount: "0.0400 ETH", Fiat: "$103.82", Status: StatusFailed, Time: "38 min ago", Color: "bg-indigo-500"},
			{ID: "pay_test_9Rd6nJ3h", Customer: "Test customer 05", Email: "test+05@example.com", Asset: "USDT", Network: "Ethereum", Amount: "1,200.00 USDT", Fiat: "$1,199.88", Status: StatusSucceeded, Time: "52 min ago", Color: "bg-emerald-500"},
			{ID: "pay_test_2Lw8gK5t", Customer: "Test customer 06", Email: "test+06@example.com", Asset: "TRX", Network: "Tron", Amount: "150.00 TRX", Fiat: "$50.18", Status: StatusRefunded, Time: "1 h ago", Color: "bg-red-500"},
			{ID: "pay_test_7Pn3eY8q", Customer: "Test customer 07", Email: "test+07@example.com", Asset: "SOL", Network: "Solana", Amount: "0.60 SOL", Fiat: "$99.85", Status: StatusFailed, Time: "2 h ago", Color: "bg-violet-500"},
			{ID: "pay_test_4Fj1uV6r", Customer: "Test customer 08", Email: "test+08@example.com", Asset: "USDC", Network: "Solana", Amount: "75.00 USDC", Fiat: "$75.00", Status: StatusSucceeded, Time: "3 h ago", Color: "bg-sky-500"},
		},
		TotalCount: 612,
	}
}
