// Package worker runs the background loops: invoice expiry, payment and
// payout reconciliation, webhook delivery and housekeeping. Chain scanning
// (calling provider RPCs and feeding chain.RecordTransaction) plugs in here
// too once an adapter exists for it; the reconciliation loops only react to
// what scanners record.
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

	loop("expire_invoices", iv.ExpireInvoices, func(ctx context.Context) error {
		n, err := a.Checkout.ExpireDue(ctx)
		if n > 0 {
			log.Info("invoices expired", "count", n)
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
