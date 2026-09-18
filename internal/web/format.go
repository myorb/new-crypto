package web

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"templ-app/blocks/dashboard"
	"templ-app/components/chart"
	"templ-app/internal/reference"
	"templ-app/internal/store"
)

// fx converts asset amounts to the merchant's display currency with the
// latest stored rates and knows how to print both.
type fx struct {
	currency string
	rates    map[int16]decimal.Decimal
	assets   map[int16]reference.Asset
	networks map[string]store.Network
}

func (s *Server) loadFX(ctx context.Context, currency string) (*fx, error) {
	f := &fx{currency: currency, rates: map[int16]decimal.Decimal{}, assets: map[int16]reference.Asset{}, networks: map[string]store.Network{}}
	rates, err := s.app.Pricing.LatestRates(ctx, currency)
	if err != nil {
		return nil, err
	}
	for _, r := range rates {
		f.rates[r.BaseAssetID] = decimal.NewFromBigInt(r.Rate.Int, r.Rate.Exp)
	}
	assets, err := s.app.Catalog.Assets(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		f.assets[a.ID] = a
		f.networks[a.Network.Code] = a.Network
	}
	return f, nil
}

// fiat values an asset amount in the display currency; ok is false when no
// rate is known (the value is then 0 and callers print a dash).
func (f *fx) fiat(assetID int16, amount decimal.Decimal) (float64, bool) {
	if a, ok := f.assets[assetID]; ok && a.Code == f.currency {
		v, _ := amount.Float64()
		return v, true
	}
	rate, ok := f.rates[assetID]
	if !ok {
		return 0, false
	}
	v, _ := amount.Mul(rate).Float64()
	return v, true
}

func (f *fx) fiatStr(v float64) string { return formatFiat(v, f.currency) }

// fiatOrDash prints a converted amount or a dash when there is no rate.
func (f *fx) fiatOrDash(assetID int16, amount decimal.Decimal) string {
	v, ok := f.fiat(assetID, amount)
	if !ok {
		return "—"
	}
	return f.fiatStr(v)
}

func (f *fx) asset(id int16) reference.Asset { return f.assets[id] }

func (f *fx) networkName(code string) string {
	if n, ok := f.networks[code]; ok {
		return n.Name
	}
	return code
}

// amount prints an asset amount with its symbol, e.g. "2,450.00 USDT".
func (f *fx) amount(assetID int16, v decimal.Decimal) string {
	a, ok := f.assets[assetID]
	if !ok {
		return v.String()
	}
	return amountStr(v, a.Symbol, a.Decimals)
}

var currencySymbols = map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥"}

// formatFiat prints 1284320.5 as "$1,284,320.50" (or "CHF 1,284,320.50").
func formatFiat(v float64, currency string) string {
	if sym, ok := currencySymbols[currency]; ok {
		if v < 0 {
			return "-" + sym + chart.Thousands(-v, 2)
		}
		return sym + chart.Thousands(v, 2)
	}
	return currency + " " + chart.Thousands(v, 2)
}

// amountStr prints a crypto amount with two decimals when it is round and
// up to the asset's precision (capped at 8) otherwise.
func amountStr(v decimal.Decimal, symbol string, decimals int16) string {
	places := int32(2)
	if !v.Equal(v.Round(2)) {
		places = int32(min(int(decimals), 8))
		v = v.Round(places)
		// trim trailing zeros but keep at least two places
		s := v.String()
		if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i-1 > 2 {
			places = int32(len(strings.TrimRight(s[i+1:], "0")))
			if places < 2 {
				places = 2
			}
		}
	}
	f, _ := v.Float64()
	if v.Abs().GreaterThan(decimal.NewFromInt(1e15)) {
		return v.StringFixed(places) + " " + symbol
	}
	return chart.Thousands(f, int(places)) + " " + symbol
}

// ago prints a relative time such as "2 min ago".
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "Yesterday"
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
	return t.Format("Jan 2, 2006")
}

func agoPtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return ago(*t)
}

// shortAddr abbreviates an address or hash: "TXk4…9pQe".
func shortAddr(a string) string {
	if len(a) <= 12 {
		return a
	}
	return a[:6] + "…" + a[len(a)-4:]
}

// shortID renders a uuid as a display id with a prefix: "pay_3Kq9fT2m".
// It keeps the tail: uuidv7 starts with a millisecond timestamp, so rows
// created together share their first characters and would look identical.
func shortID(prefix string, id fmt.Stringer) string {
	s := strings.ReplaceAll(id.String(), "-", "")
	if len(s) > 10 {
		s = s[len(s)-10:]
	}
	return prefix + s
}

// initials picks up to two upper-case initials from a name or email.
func initials(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, '@'); i > 0 {
		name = name[:i]
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '.' || r == '-' || r == '_' })
	var b strings.Builder
	for _, p := range parts {
		if len(p) > 0 {
			b.WriteString(strings.ToUpper(p[:1]))
		}
		if b.Len() == 2 {
			break
		}
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// displayName turns an email into something to show when no name is known.
func displayName(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}

var assetColors = map[string]string{
	"USDT": "bg-emerald-500", "USDC": "bg-sky-500", "BTC": "bg-orange-500", "ETH": "bg-indigo-500",
	"TRX": "bg-red-500", "SOL": "bg-violet-500", "BNB": "bg-yellow-500", "POL": "bg-purple-500", "TON": "bg-blue-500",
}

func assetColor(symbol string) string {
	if c, ok := assetColors[symbol]; ok {
		return c
	}
	return "bg-slate-500"
}

func chartColor(i int) string {
	if i < 5 {
		return fmt.Sprintf("var(--chart-%d)", i+1)
	}
	return "var(--muted-foreground)"
}

// change formats the relative move between two values.
func change(cur, prev float64) (string, dashboard.Trend) {
	switch {
	case prev == 0 && cur == 0:
		return "0.0%", dashboard.TrendFlat
	case prev == 0:
		return "new", dashboard.TrendUp
	}
	p := (cur - prev) / math.Abs(prev) * 100
	switch {
	case p > 0.05:
		return fmt.Sprintf("+%.1f%%", p), dashboard.TrendUp
	case p < -0.05:
		return fmt.Sprintf("%.1f%%", p), dashboard.TrendDown
	}
	return "0.0%", dashboard.TrendFlat
}

func pct(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(part/total*1000) / 10
}

func planLabel(status store.OrganizationStatus) string {
	switch status {
	case store.OrganizationStatusActive:
		return "Active"
	case store.OrganizationStatusPendingReview:
		return "Pending review"
	case store.OrganizationStatusSuspended:
		return "Suspended"
	case store.OrganizationStatusClosed:
		return "Closed"
	}
	return string(status)
}

func roleLabel(r store.OrganizationRole) string {
	if r == "" {
		return "Member"
	}
	return strings.ToUpper(string(r)[:1]) + string(r)[1:]
}

// dayKey and dayLabel keep per-day maps consistent across views.
func dayKey(t time.Time) string   { return t.UTC().Format("2006-01-02") }
func dayLabel(t time.Time) string { return t.Format("Jan 2") }

// days returns the last n UTC days ending today, oldest first.
func days(n int) []time.Time {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	out := make([]time.Time, n)
	for i := range out {
		out[i] = today.AddDate(0, 0, -(n - 1 - i))
	}
	return out
}

func zeros(n int) []float64 { return make([]float64, n) }

// f64 is the float value of a decimal, for display only.
func f64(d decimal.Decimal) float64 {
	v, _ := d.Float64()
	return v
}

func sum(v []float64) float64 {
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t
}
