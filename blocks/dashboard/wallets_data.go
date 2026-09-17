package dashboard

// Wallet is one asset wallet card.
type Wallet struct {
	Symbol  string
	Name    string
	Network string
	Address string
	Amount  string
	Fiat    string
	Value   float64 // fiat value used for the donut
	Change  string
	Trend   Trend
	Spark   []float64
	Color   string
	Chart   string // CSS color for the donut slice
}

// DepositAddress is one receiving address.
type DepositAddress struct {
	Network string
	Assets  []string
	Address string
	Memo    string
	Label   string
}

// ActivityKind is the direction of an on-chain movement.
type ActivityKind string

const (
	ActivityIn   ActivityKind = "received"
	ActivityOut  ActivityKind = "sent"
	ActivitySwap ActivityKind = "swap"
)

// Activity is one on-chain transaction.
type Activity struct {
	Hash          string
	Kind          ActivityKind
	Asset         string
	Network       string
	Amount        string
	Fiat          string
	Counterparty  string
	Confirmations string
	Status        Status
	Time          string
}

// WalletsData is everything the wallets page renders.
type WalletsData struct {
	Sandbox     bool
	Total       string
	Change      string
	Trend       Trend
	Wallets     []Wallet
	Addresses   []DepositAddress
	Activity    []Activity
	ActivityAll int
}

// TotalValue sums wallet fiat values.
func (d WalletsData) TotalValue() float64 {
	sum := 0.0
	for _, w := range d.Wallets {
		sum += w.Value
	}
	return sum
}

// WalletsFixture returns the live or sandbox wallets.
func WalletsFixture(sandbox bool) WalletsData {
	w := []Wallet{
		{Symbol: "USDT", Name: "Tether", Network: "Tron · Ethereum", Address: "TXk4…9pQe", Amount: "41,220.00 USDT", Fiat: "$41,215.88", Value: 41215.88, Change: "0.0%", Trend: TrendFlat, Spark: []float64{38, 39, 41, 40, 42, 41, 43, 44, 43, 45, 44, 46, 45, 41}, Chart: "var(--chart-1)"},
		{Symbol: "BTC", Name: "Bitcoin", Network: "Bitcoin", Address: "bc1q…x8k2", Amount: "0.4182 BTC", Fiat: "$27,884.12", Value: 27884.12, Change: "+2.1%", Trend: TrendUp, Spark: []float64{25, 26, 25.4, 26.8, 27.1, 26.5, 27.9, 27.2, 28.4, 27.8, 28.9, 28.1, 27.6, 27.9}, Chart: "var(--chart-3)"},
		{Symbol: "ETH", Name: "Ethereum", Network: "Ethereum · Arc", Address: "0x7a3f…c21e", Amount: "5.9020 ETH", Fiat: "$15,318.40", Value: 15318.4, Change: "-1.4%", Trend: TrendDown, Spark: []float64{16.2, 16.0, 15.8, 16.1, 15.6, 15.9, 15.4, 15.7, 15.2, 15.5, 15.1, 15.4, 15.6, 15.3}, Chart: "var(--chart-4)"},
		{Symbol: "USDC", Name: "USD Coin", Network: "Arc · Solana", Address: "0x7a3f…c21e", Amount: "11,880.00 USDC", Fiat: "$11,880.00", Value: 11880, Change: "0.0%", Trend: TrendFlat, Spark: []float64{9, 10, 11, 10.5, 12, 11.5, 13, 12, 11, 12.5, 12, 11.8, 12.2, 11.9}, Chart: "var(--chart-2)"},
		{Symbol: "SOL", Name: "Solana", Network: "Solana", Address: "9xQe…Ff3a", Amount: "18.20 SOL", Fiat: "$3,028.84", Value: 3028.84, Change: "+4.8%", Trend: TrendUp, Spark: []float64{2.6, 2.7, 2.65, 2.8, 2.75, 2.9, 2.85, 3.0, 2.95, 3.1, 3.05, 3.0, 3.02, 3.03}, Chart: "var(--chart-5)"},
		{Symbol: "TRX", Name: "Tron", Network: "Tron", Address: "TXk4…9pQe", Amount: "330.01 TRX", Fiat: "$110.40", Value: 110.4, Change: "-0.6%", Trend: TrendDown, Spark: []float64{0.12, 0.118, 0.115, 0.117, 0.113, 0.114, 0.111, 0.112, 0.11, 0.111, 0.109, 0.11, 0.111, 0.11}, Chart: "var(--muted-foreground)"},
	}
	for i := range w {
		w[i].Color = assetColor(w[i].Symbol)
	}
	acts := []Activity{
		{Hash: "tx_a91f3c…7e2b", Kind: ActivityIn, Asset: "USDT", Network: "Tron", Amount: "+2,450.00 USDT", Fiat: "$2,449.76", Counterparty: "TQn9…4kLm", Confirmations: "19/19", Status: StatusSucceeded, Time: "2 min ago"},
		{Hash: "tx_5d2e8a…c410", Kind: ActivityIn, Asset: "BTC", Network: "Bitcoin", Amount: "+0.0182 BTC", Fiat: "$1,213.58", Counterparty: "bc1q…m3ns", Confirmations: "1/3", Status: StatusPending, Time: "9 min ago"},
		{Hash: "tx_0c7b19…f88d", Kind: ActivitySwap, Asset: "ETH", Network: "Ethereum", Amount: "-1.5000 ETH → +3,893.10 USDC", Fiat: "$3,893.10", Counterparty: "Auto-convert", Confirmations: "12/12", Status: StatusSucceeded, Time: "35 min ago"},
		{Hash: "tx_e3f4a2…1b9c", Kind: ActivityOut, Asset: "USDC", Network: "Arc", Amount: "-25,000.00 USDC", Fiat: "$25,000.00", Counterparty: "Treasury wallet", Confirmations: "1/1", Status: StatusSucceeded, Time: "Sep 10"},
		{Hash: "tx_77ab0d…e5f1", Kind: ActivityIn, Asset: "SOL", Network: "Solana", Amount: "+18.20 SOL", Fiat: "$3,028.84", Counterparty: "9xQe…Ff3a", Confirmations: "32/32", Status: StatusSucceeded, Time: "3 h ago"},
		{Hash: "tx_b2c4d8…a03e", Kind: ActivityOut, Asset: "TRX", Network: "Tron", Amount: "-1,480.00 TRX", Fiat: "$495.06", Counterparty: "Refund · Sofia Almeida", Confirmations: "19/19", Status: StatusSucceeded, Time: "2 h ago"},
		{Hash: "tx_9e1f6c…d27a", Kind: ActivityIn, Asset: "USDT", Network: "Ethereum", Amount: "+12,300.00 USDT", Fiat: "$12,298.77", Counterparty: "0x3b9d…e771", Confirmations: "12/12", Status: StatusSucceeded, Time: "1 h ago"},
	}
	for i := range acts {
		acts[i].Hash = tid(sandbox, acts[i].Hash)
	}
	d := WalletsData{
		Sandbox: sandbox,
		Total:   "$99,437.64",
		Change:  "+0.9%",
		Trend:   TrendUp,
		Wallets: w,
		Addresses: []DepositAddress{
			{Network: "Tron", Assets: []string{"USDT", "TRX"}, Address: "TXk4Wq2mR8sLp9vN3bJf7Hc5dYe1Za6t9pQe", Label: "Primary"},
			{Network: "Ethereum", Assets: []string{"ETH", "USDT", "USDC"}, Address: "0x7a3f9C2e41B8d6F05aE7c9D1b3F24e8A90Bc21e", Label: "Primary"},
			{Network: "Arc", Assets: []string{"USDC", "ETH"}, Address: "0x7a3f9C2e41B8d6F05aE7c9D1b3F24e8A90Bc21e", Label: "Primary"},
			{Network: "Solana", Assets: []string{"SOL", "USDC"}, Address: "9xQeWvG816acX7uDEL1T4RnvDK8z9HfqsM3mB2Ff3a", Label: "Primary"},
			{Network: "Bitcoin", Assets: []string{"BTC"}, Address: "bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlhx8k2", Label: "Primary"},
		},
		Activity:    acts,
		ActivityAll: 9_240,
	}
	if sandbox {
		d.Total = "$18,299.20"
		d.Change = "0.0%"
		d.Trend = TrendFlat
		for i := range d.Wallets {
			d.Wallets[i].Address = "test…0000"
		}
		for i := range d.Addresses {
			d.Addresses[i].Address = "sandbox_" + d.Addresses[i].Network + "_0000000000000000000000000000"
			d.Addresses[i].Label = "Sandbox"
		}
		d.ActivityAll = 1_120
	}
	return d
}

func activityIcon(k ActivityKind) string {
	switch k {
	case ActivityIn:
		return "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
	case ActivityOut:
		return "bg-rose-500/10 text-rose-600 dark:text-rose-400"
	}
	return "bg-sky-500/10 text-sky-600 dark:text-sky-400"
}
