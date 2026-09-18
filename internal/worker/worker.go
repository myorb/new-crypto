// Package worker runs the background loops: chain scanning and confirmation
// tracking, payout and treasury broadcasting, invoice expiry, reconciliation,
// webhook delivery and housekeeping. The chain loops only run for providers
// whose adapter implements chain.Scanner / chain.Broadcaster; the others are
// driven by provider callbacks or by hand, and the reconciliation loops react
// to whatever was recorded either way.
package worker

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"templ-app/internal/app"
)

// Intervals tune the loops; zero uses the default.
type Intervals struct {
	ExpireInvoices time.Duration // default 30s
	Reconcile      time.Duration // payments, payouts, treasury; default 15s
	Webhooks       time.Duration // default 5s
	Housekeeping   time.Duration // default 1h
	Scan           time.Duration // block scanning and confirmation tracking; default 5s
	Broadcast      time.Duration // payout and treasury sending; default 10s

	// MaxBlocks caps how many blocks one scan pass may cover per network, so
	// catching up after downtime cannot hold the loop open indefinitely.
	MaxBlocks int // default 100
	// Batch caps the rows one broadcast or tracking pass claims. Default 100.
	Batch int32
}

func (i *Intervals) defaults() {
	if i.ExpireInvoices == 0 {
		i.ExpireInvoices = 30 * time.Second
	}
	if i.Reconcile == 0 {
		i.Reconcile = 15 * time.Second
	}
	if i.Webhooks == 0 {
		i.Webhooks = 5 * time.Second
	}
	if i.Housekeeping == 0 {
		i.Housekeeping = time.Hour
	}
	if i.Scan == 0 {
		i.Scan = 5 * time.Second
	}
	if i.Broadcast == 0 {
		i.Broadcast = 10 * time.Second
	}
	if i.MaxBlocks == 0 {
		i.MaxBlocks = 100
	}
	if i.Batch == 0 {
		i.Batch = 100
	}
}

// Run starts every loop and blocks until ctx is cancelled and the loops
// have stopped.
func Run(ctx context.Context, a *app.App, iv Intervals) {
	iv.defaults()
	log := a.Log.With("component", "worker")
	var wg sync.WaitGroup

	loop := func(name string, every time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// stagger start so loops do not all hit the database at once
			select {
			case <-time.After(time.Duration(rand.Int64N(int64(every)))):
			case <-ctx.Done():
				return
			}
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				start := time.Now()
				if err := fn(ctx); err != nil && ctx.Err() == nil {
					log.Error("job failed", "job", name, "err", err)
				} else if d := time.Since(start); d > every/2 {
					log.Warn("job is slow", "job", name, "took", d)
				}
				select {
				case <-t.C:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	loop("scan_chains", iv.Scan, func(ctx context.Context) error {
		return scanChains(ctx, a, log, iv.MaxBlocks)
	})
	loop("track_confirmations", iv.Scan, func(ctx context.Context) error {
		return trackConfirmations(ctx, a, log, iv.Batch)
	})
	loop("broadcast_payouts", iv.Broadcast, func(ctx context.Context) error {
		return broadcastPayouts(ctx, a, log, iv.Batch)
	})
	loop("broadcast_treasury", iv.Broadcast, func(ctx context.Context) error {
		return broadcastTreasury(ctx, a, log, iv.Batch)
	})
	loop("expire_invoices", iv.ExpireInvoices, func(ctx context.Context) error {
		n, err := a.Checkout.ExpireDue(ctx)
		if n > 0 {
			log.Info("invoices expired", "count", n)
		}
		return err
	})
	loop("expire_payment_links", iv.ExpireInvoices, func(ctx context.Context) error {
		n, err := a.Checkout.ExpireDueLinks(ctx)
		if n > 0 {
			log.Info("payment links expired", "count", n)
		}
		return err
	})
	loop("reconcile_payments", iv.Reconcile, func(ctx context.Context) error {
		st, err := a.Payments.ReconcileDetected(ctx, 200)
		if st.Credited+st.Reverted > 0 {
			log.Info("payments reconciled", "credited", st.Credited, "reverted", st.Reverted)
		}
		return err
	})
	loop("reconcile_payouts", iv.Reconcile, func(ctx context.Context) error {
		st, err := a.Payouts.ReconcileBroadcast(ctx, 200)
		if st.Completed+st.Failed > 0 {
			log.Info("payouts reconciled", "completed", st.Completed, "failed", st.Failed)
		}
		return err
	})
	loop("reconcile_treasury", iv.Reconcile, func(ctx context.Context) error {
		st, err := a.Treasury.ReconcileBroadcast(ctx, 200)
		if st.Completed+st.Failed > 0 {
			log.Info("treasury reconciled", "completed", st.Completed, "failed", st.Failed)
		}
		return err
	})
	if a.Keys != nil {
		loop("deliver_webhooks", iv.Webhooks, func(ctx context.Context) error {
			st, err := a.Events.DeliverDue(ctx, 50)
			if st.Claimed > 0 {
				log.Info("webhooks delivered", "ok", st.Succeeded, "failed", st.Failed, "exhausted", st.Exhausted)
			}
			return err
		})
	} else {
		log.Warn("webhook delivery disabled: no encryption key")
	}
	if a.Config.DevSeed && !a.Config.Production() {
		loop("dev_rates", 5*time.Minute, a.RefreshDevRates)
	}
	loop("housekeeping", iv.Housekeeping, func(ctx context.Context) error {
		if _, err := a.Identity.PurgeExpiredSessions(ctx); err != nil {
			return err
		}
		if _, err := a.Audit.PurgeExpiredIdempotencyKeys(ctx); err != nil {
			return err
		}
		_, err := a.Pricing.PurgeRatesBefore(ctx, time.Now().Add(-30*24*time.Hour))
		return err
	})

	<-ctx.Done()
	wg.Wait()
	log.Info("worker stopped")
}
