package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"templ-app/internal/app"
	"templ-app/internal/chain"
	"templ-app/internal/money"
	"templ-app/internal/store"
)

// scanChains advances every scannable provider / network cursor. One network
// failing (an RPC is down) must not stop the others, so failures are logged
// and the pass continues.
func scanChains(ctx context.Context, a *app.App, log *slog.Logger, maxBlocks int) error {
	targets, err := a.Chain.ScanTargets(ctx)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st, err := a.Chain.Scan(ctx, t.Provider.ID, t.Network.ID, maxBlocks)
		if err != nil {
			log.ErrorContext(ctx, "scan failed", "network", t.Network.Code, "provider", t.Provider.Code, "err", err)
			continue
		}
		if st.Transactions > 0 || st.Rewound {
			log.InfoContext(ctx, "blocks scanned", "network", t.Network.Code, "provider", t.Provider.Code,
				"from", st.From, "to", st.To, "head", st.Head, "transactions", st.Transactions, "rewound", st.Rewound)
		}
	}
	return nil
}

// trackConfirmations re-checks pending transactions so deposits and payouts
// reach the confirmation threshold even when the scanner never saw the block.
func trackConfirmations(ctx context.Context, a *app.App, log *slog.Logger, batch int32) error {
	targets, err := a.Chain.ScanTargets(ctx)
	if err != nil {
		return err
	}
	seen := map[int16]bool{}
	for _, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if seen[t.Network.ID] {
			continue
		}
		seen[t.Network.ID] = true
		st, err := a.Chain.TrackPending(ctx, t.Network.ID, batch)
		if err != nil {
			log.ErrorContext(ctx, "confirmation tracking failed", "network", t.Network.Code, "err", err)
			continue
		}
		if st.Updated > 0 {
			log.InfoContext(ctx, "confirmations updated", "network", t.Network.Code,
				"checked", st.Checked, "updated", st.Updated, "confirmed", st.Confirmed, "failed", st.Failed)
		}
	}
	return nil
}

// broadcastPayouts routes approved withdrawals to a hot address and sends
// them. A withdrawal whose provider has no broadcasting adapter is left
// queued for whatever sends it instead. Sending is idempotent on the
// withdrawal id, so a crash between Send and MarkBroadcast cannot pay twice.
func broadcastPayouts(ctx context.Context, a *app.App, log *slog.Logger, batch int32) error {
	due, err := a.Payouts.ToProcess(ctx, batch)
	if err != nil {
		return err
	}
	for _, w := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w, from, err := a.Payouts.Route(ctx, w.ID)
		if err != nil {
			log.ErrorContext(ctx, "payout routing failed", "withdrawal", w.ID, "err", err)
			continue
		}
		if !a.Chain.CanBroadcast(ctx, from.ProviderID) {
			continue
		}
		obs, err := a.Chain.Send(ctx, chain.SendRequest{
			FromAddressID: from.ID, AssetID: w.AssetID, To: w.ToAddress, ToMemo: w.ToMemo,
			Amount: money.FromNumeric(w.Amount), Reference: payoutReference(w),
		})
		if err != nil {
			if errors.Is(err, chain.ErrBroadcastRejected) {
				if _, ferr := a.Payouts.Fail(ctx, w.ID, truncate("broadcast rejected: "+err.Error())); ferr != nil {
					log.ErrorContext(ctx, "failing rejected payout failed", "withdrawal", w.ID, "err", ferr)
				}
				continue
			}
			log.ErrorContext(ctx, "payout broadcast failed", "withdrawal", w.ID, "err", err)
			continue
		}
		if _, err := a.Payouts.MarkBroadcast(ctx, w.ID, obs.Transaction.ID, obs.Transaction.ExternalTxID); err != nil {
			log.ErrorContext(ctx, "marking payout broadcast failed", "withdrawal", w.ID, "tx", obs.Transaction.Hash, "err", err)
			continue
		}
		log.InfoContext(ctx, "payout broadcast", "withdrawal", w.ID, "tx", obs.Transaction.Hash)
	}
	return nil
}

// broadcastTreasury sends queued sweeps, top-ups and rebalances.
func broadcastTreasury(ctx context.Context, a *app.App, log *slog.Logger, batch int32) error {
	queued, err := a.Treasury.Queued(ctx, batch)
	if err != nil {
		return err
	}
	for _, t := range queued {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		from, err := a.Chain.Address(ctx, t.FromAddressID)
		if err != nil {
			log.ErrorContext(ctx, "treasury from address missing", "transfer", t.ID, "err", err)
			continue
		}
		if !a.Chain.CanBroadcast(ctx, from.ProviderID) {
			continue
		}
		to, err := a.Chain.Address(ctx, t.ToAddressID)
		if err != nil {
			log.ErrorContext(ctx, "treasury to address missing", "transfer", t.ID, "err", err)
			continue
		}
		obs, err := a.Chain.Send(ctx, chain.SendRequest{
			FromAddressID: from.ID, AssetID: t.AssetID, To: to.Address, ToMemo: to.Memo,
			Amount: money.FromNumeric(t.Amount), Reference: transferReference(t),
		})
		if err != nil {
			if errors.Is(err, chain.ErrBroadcastRejected) {
				if _, ferr := a.Treasury.Fail(ctx, t.ID, truncate("broadcast rejected: "+err.Error())); ferr != nil {
					log.ErrorContext(ctx, "failing rejected transfer failed", "transfer", t.ID, "err", ferr)
				}
				continue
			}
			log.ErrorContext(ctx, "treasury broadcast failed", "transfer", t.ID, "err", err)
			continue
		}
		if _, err := a.Treasury.MarkBroadcast(ctx, t.ID, obs.Transaction.ID, obs.Transaction.ExternalTxID); err != nil {
			log.ErrorContext(ctx, "marking transfer broadcast failed", "transfer", t.ID, "tx", obs.Transaction.Hash, "err", err)
			continue
		}
		log.InfoContext(ctx, "treasury transfer broadcast", "transfer", t.ID, "kind", t.Kind, "tx", obs.Transaction.Hash)
	}
	return nil
}

// References are the provider-side idempotency keys: the same business
// object always produces the same one, so a retried send is deduplicated.
func payoutReference(w store.Withdrawal) string { return "withdrawal:" + w.ID.String() }

func transferReference(t store.InternalTransfer) string {
	return fmt.Sprintf("internal_transfer:%s", t.ID)
}

// truncate keeps failure reasons short enough to read in the dashboard.
func truncate(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
