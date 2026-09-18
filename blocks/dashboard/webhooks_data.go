package dashboard

import "templ-app/components/chart"

// EndpointStatus is the health of a webhook endpoint.
type EndpointStatus string

const (
	EndpointEnabled  EndpointStatus = "enabled"
	EndpointFailing  EndpointStatus = "failing"
	EndpointDisabled EndpointStatus = "disabled"
)

// Endpoint is one webhook destination.
type Endpoint struct {
	ID          string
	URL         string
	Description string
	Events      []string
	Status      EndpointStatus
	SuccessRate float64
	Last        string
	Version     string
}

// Delivery is one webhook attempt.
type Delivery struct {
	ID       string
	Event    string
	Endpoint string
	Code     int
	Attempts int
	Latency  string
	OK       bool
	Pending  bool
	Time     string
}

// WebhooksData is everything the webhooks page renders.
type WebhooksData struct {
	Sandbox    bool
	Stats      []Stat
	Endpoints  []Endpoint
	Deliveries []Delivery
	Total      int
	ByDay      []chart.Datum
	Secret     string
	EventTypes []string
}

// WebhooksFixture returns the live or sandbox webhooks.
func WebhooksFixture(sandbox bool) WebhooksData {
	host := "api.northwind.dev"
	if sandbox {
		host = "staging.northwind.dev"
	}
	eps := []Endpoint{
		{ID: "we_4Kq8sLm2", URL: "https://" + host + "/webhooks/paychain", Description: "Primary · order fulfilment", Events: []string{"payment.succeeded", "payment.failed", "refund.created", "payout.paid"}, Status: EndpointEnabled, SuccessRate: 99.6, Last: "1 min ago", Version: "2026-09-01"},
		{ID: "we_9Tz3bRp7", URL: "https://hooks.zapier.com/hooks/catch/1823/pc9a", Description: "Zapier · Slack alerts", Events: []string{"payment.failed", "payout.failed"}, Status: EndpointEnabled, SuccessRate: 100, Last: "38 min ago", Version: "2026-09-01"},
		{ID: "we_1Hs6vNc5", URL: "https://erp.northwind-legacy.com/api/pc", Description: "Legacy ERP sync", Events: []string{"payment.succeeded", "payout.paid"}, Status: EndpointFailing, SuccessRate: 61.2, Last: "12 min ago", Version: "2025-11-01"},
		{ID: "we_6Lw2gKd9", URL: "https://" + host + "/webhooks/paychain-dev", Description: "Developer preview", Events: []string{"*"}, Status: EndpointDisabled, SuccessRate: 0, Last: "Aug 30", Version: "2026-09-01"},
	}
	dels := []Delivery{
		{ID: "evt_9Ab3kLq2", Event: "payment.succeeded", Endpoint: eps[0].URL, Code: 200, Attempts: 1, Latency: "182 ms", OK: true, Time: "1 min ago"},
		{ID: "evt_7Zx1mNp8", Event: "payment.pending", Endpoint: eps[0].URL, Code: 200, Attempts: 1, Latency: "204 ms", OK: true, Time: "6 min ago"},
		{ID: "evt_2Kd4sRw5", Event: "payment.succeeded", Endpoint: eps[2].URL, Code: 502, Attempts: 3, Latency: "30.0 s", OK: false, Time: "12 min ago"},
		{ID: "evt_5Qw8tVb1", Event: "payout.paid", Endpoint: eps[0].URL, Code: 200, Attempts: 1, Latency: "165 ms", OK: true, Time: "22 min ago"},
		{ID: "evt_3Fj7uHc6", Event: "payment.failed", Endpoint: eps[1].URL, Code: 200, Attempts: 1, Latency: "412 ms", OK: true, Time: "38 min ago"},
		{ID: "evt_8Hs2vNx4", Event: "payment.failed", Endpoint: eps[0].URL, Code: 200, Attempts: 2, Latency: "1.9 s", OK: true, Time: "41 min ago"},
		{ID: "evt_1Ls6yGe9", Event: "refund.created", Endpoint: eps[0].URL, Code: 200, Attempts: 1, Latency: "171 ms", OK: true, Time: "2 h ago"},
		{ID: "evt_4Pn9cJt3", Event: "payout.paid", Endpoint: eps[2].URL, Code: 0, Attempts: 5, Latency: "—", OK: false, Pending: true, Time: "3 h ago"},
		{ID: "evt_6Rd0eYk7", Event: "payment.succeeded", Endpoint: eps[0].URL, Code: 200, Attempts: 1, Latency: "190 ms", OK: true, Time: "3 h ago"},
		{ID: "evt_0Mv5zXa2", Event: "customer.created", Endpoint: eps[0].URL, Code: 200, Attempts: 1, Latency: "158 ms", OK: true, Time: "4 h ago"},
	}
	for i := range eps {
		eps[i].ID = tid(sandbox, eps[i].ID)
	}
	for i := range dels {
		dels[i].ID = tid(sandbox, dels[i].ID)
	}
	d := WebhooksData{
		Sandbox:    sandbox,
		Endpoints:  eps,
		Deliveries: dels,
		Total:      18_420,
		ByDay:      webhookByDay(14, 1),
		Secret:     "whsec_••••••••••••••••••••••••••••7Qa2",
		EventTypes: []string{"payment.created", "payment.pending", "payment.succeeded", "payment.failed", "payment.expired", "refund.created", "refund.succeeded", "payout.created", "payout.paid", "payout.failed", "customer.created", "link.paid"},
		Stats: []Stat{
			{Label: "Deliveries 24h", Value: "1,912", Change: "+4.1%", Trend: TrendUp, Hint: "Across 3 active endpoints"},
			{Label: "Success rate", Value: "97.8%", Change: "-1.6 pt", Trend: TrendDown, Hint: "1 endpoint failing"},
			{Label: "Median latency", Value: "188 ms", Change: "-12 ms", Trend: TrendUp, Hint: "p95 · 1.4 s"},
			{Label: "Pending retries", Value: "7", Change: "", Hint: "Next retry in 14 min"},
		},
	}
	if sandbox {
		d.Total = 128
		d.ByDay = webhookByDay(14, 0.07)
		d.Stats = []Stat{
			{Label: "Deliveries 24h", Value: "130", Hint: "Simulated events"},
			{Label: "Success rate", Value: "98.5%", Hint: "2 failed"},
			{Label: "Median latency", Value: "201 ms", Hint: "p95 · 0.9 s"},
			{Label: "Pending retries", Value: "1", Hint: "Retries every 5 min in sandbox"},
		}
	}
	return d
}

func webhookByDay(days int, scale float64) []chart.Datum {
	out := make([]chart.Datum, days)
	for i := range out {
		day := today.AddDate(0, 0, -(days - 1 - i))
		base := (1500.0 + 40*float64(i)) * scale
		if wd := day.Weekday(); wd == 6 || wd == 0 {
			base *= 0.6
		}
		failed := base * 0.02
		if i == days-2 || i == days-1 {
			failed = base * 0.09
		}
		out[i] = chart.Datum{
			"day":       day.Format("Jan 2"),
			"delivered": float64(int(base - failed)),
			"failed":    float64(int(failed)),
		}
	}
	return out
}

func endpointStatusClass(s EndpointStatus) string {
	switch s {
	case EndpointEnabled:
		return pillOK
	case EndpointFailing:
		return pillBad
	}
	return pillNeutral
}

// filterDeliveries returns all, only successful or only failed deliveries.
func filterDeliveries(rows []Delivery, key string) []Delivery {
	if key == "all" {
		return rows
	}
	out := make([]Delivery, 0, len(rows))
	for _, r := range rows {
		if (key == "succeeded" && r.OK) || (key == "failed" && !r.OK) {
			out = append(out, r)
		}
	}
	return out
}
