// Package marketing holds the public pages shown before sign-in. The
// landing page is static content: the copy lives in this file and
// landing.templ only lays it out.
package marketing

import (
	"github.com/a-h/templ"

	"templ-app/components/icon"
)

// Stat is one number in the hero's proof strip.
type Stat struct {
	Value string
	Label string
}

// Feature is one tile in the product grid.
type Feature struct {
	Icon  func(...icon.Props) templ.Component
	Title string
	Body  string
}

// Step is one numbered item in the "how it works" list.
type Step struct {
	Title string
	Body  string
}

var stats = []Stat{
	{Value: "$4.2B", Label: "processed to date"},
	{Value: "2,400+", Label: "merchants live"},
	{Value: "99.98%", Label: "uptime · 12 months"},
	{Value: "5", Label: "networks supported"},
}

var features = []Feature{
	{
		Icon:  icon.Receipt,
		Title: "Payments",
		Body:  "Hosted checkout and shareable payment links that take BTC, ETH and USDC, with the rate locked while the customer pays.",
	},
	{
		Icon:  icon.ArrowLeftRight,
		Title: "Payouts",
		Body:  "Pay vendors and contractors in batches, on-chain or to their bank, behind the approval rules your team sets.",
	},
	{
		Icon:  icon.Wallet,
		Title: "Wallets",
		Body:  "MPC custody across five networks. Sweep, consolidate and keep the balances you actually want to hold.",
	},
	{
		Icon:  icon.Landmark,
		Title: "Settlement",
		Body:  "Auto-convert to USD or EUR on receipt and settle to your bank daily, weekly or on demand.",
	},
	{
		Icon:  icon.Code,
		Title: "Developer API",
		Body:  "A REST API with idempotency keys and signed webhooks, plus a sandbox that mirrors production exactly.",
	},
	{
		Icon:  icon.ChartLine,
		Title: "Reporting",
		Body:  "Fee breakdowns, reconciliation-ready exports and an audit log your finance team can sign off on.",
	},
}

var steps = []Step{
	{
		Title: "Create your account",
		Body:  "Sign up, verify your email and switch on two-factor. Sandbox keys are ready right away.",
	},
	{
		Title: "Choose networks and settlement",
		Body:  "Pick the assets you accept, then point settlement at a wallet you hold or a bank account.",
	},
	{
		Title: "Take your first payment",
		Body:  "Send a payment link in a minute, or drop the API into your existing checkout when you're ready.",
	},
}
