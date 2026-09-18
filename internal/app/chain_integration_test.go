package app_test

// End-to-end test of the chain worker loops against a real database and the
// in-memory dev chain. Run with:
//
//	TEST_DATABASE_URL=postgres://templ_app:...@localhost:5432/templ_app?sslmode=disable go test ./internal/app/ -run Integration -v
//
// Unlike TestIntegrationPaymentLifecycle, which calls the services by hand,
// this drives the real worker: a deposit injected into the fake chain must
// travel through scanning, confirmation tracking and reconciliation on its
// own, and an approved payout must be broadcast and settled without any test
// code touching chain.RecordTransaction.

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"templ-app/internal/app"
	"templ-app/internal/chain"
	"templ-app/internal/chain/devchain"
	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/pricing"
	"templ-app/internal/secrets"
	"templ-app/internal/store"
	"templ-app/internal/worker"
)

func TestIntegrationChainWorker(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := postgres.Open(ctx, url)
	must(t, err, "open pool")
	defer pool.Close()
	keys, _ := secrets.LoadKeyring(testKey)

	// A 20ms block makes Tron's 19 confirmations take about 400ms.
	cfg := app.Config{
		DatabaseURL: url, EncryptionKey: testKey, DevChainAdapter: true, DevChainBlockTime: 20 * time.Millisecond,
		WebhookAllowPrivate: true, SessionTTL: time.Hour, RateLock: 15 * time.Minute, InvoiceTTL: time.Hour,
		MaxRateAge: 10 * time.Minute, WebhookTimeout: 5 * time.Second,
	}
	a, err := app.Build(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), pool, keys)
	must(t, err, "build app")
	if a.DevChain == nil {
		t.Fatal("dev chain should be wired when DevChainAdapter is set")
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	// --- merchant, wallets, prices -------------------------------------------------
	alice, err := a.Identity.Register(ctx, "scan-alice+"+suffix+"@example.com", "correct horse battery", "Alice")
	must(t, err, "register alice")
	bob, err := a.Identity.Register(ctx, "scan-bob+"+suffix+"@example.com", "correct horse battery", "Bob")
	must(t, err, "register bob")
	merchant, err := a.Org.Create(ctx, orgInput("scan-"+suffix, alice.ID))
	must(t, err, "create org")
	must(t, a.Org.SetStatus(ctx, merchant.ID, store.OrganizationStatusActive), "activate org")
	invToken, _, err := a.Org.Invite(ctx, merchant.ID, alice.ID, bob.Email, store.OrganizationRoleFinance, 0)
	must(t, err, "invite bob")
	_, err = a.Org.AcceptInvitation(ctx, invToken, bob)
	must(t, err, "accept invitation")

	tron, err := a.Catalog.NetworkByCode(ctx, "tron")
	must(t, err, "tron network")
	usdt, err := a.Catalog.AssetByCode(ctx, "USDT_TRON")
	must(t, err, "usdt asset")
	provider, err := a.Catalog.ProviderByCode(ctx, "tron_rpc")
	must(t, err, "tron provider")

	keyRef := "kms://scan/" + suffix
	_, err = a.Chain.CreateWallet(ctx, chain.WalletInput{ProviderID: provider.ID, NetworkID: tron.ID, Kind: store.WalletKindDeposit, Name: "deposits " + suffix, KeyRef: &keyRef})
	must(t, err, "deposit wallet")
	hotWallet, err := a.Chain.CreateWallet(ctx, chain.WalletInput{ProviderID: provider.ID, NetworkID: tron.ID, Kind: store.WalletKindHot, Name: "hot " + suffix, KeyRef: &keyRef})
	must(t, err, "hot wallet")
	hotSeed := sha256.Sum256([]byte("scan-hot" + suffix))
	hotAddr, err := a.Chain.RegisterPlatformAddress(ctx, hotWallet.ID, store.WalletKindHot, devchain.Format(tron.Family, hotSeed[:]), nil, nil)
	must(t, err, "hot address")

	_, err = a.Pricing.RecordRate(ctx, usdt.ID, "USD", money.MustParse("1"), "test")
	must(t, err, "record rate")
	_, err = a.Pricing.ReplaceSchedule(ctx, pricing.ScheduleInput{
		OrganizationID: uuid.NullUUID{UUID: merchant.ID, Valid: true}, DepositFeeBps: 100, WithdrawalFeeBps: 50,
		NetworkFeePayer: store.FeePayerPlatform, PassNetworkFee: true,
	})
	must(t, err, "fee schedule")

	depositAddr, err := a.Chain.AllocateDepositAddress(ctx, merchant.ID, tron.ID, nil)
	must(t, err, "allocate deposit address")

	// --- run the real worker -------------------------------------------------------
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(workerCtx, a, worker.Intervals{
			Scan: 40 * time.Millisecond, Broadcast: 40 * time.Millisecond, Reconcile: 40 * time.Millisecond,
			ExpireInvoices: time.Minute, Webhooks: time.Minute, Housekeeping: time.Hour,
		})
	}()

	// The first scan pass only anchors the cursor at the head; wait for it so
	// the deposit below is mined into a block the scanner will actually visit.
	waitFor(t, ctx, "scanner cursor to be anchored", func() bool {
		cur, err := a.Chain.Cursor(ctx, provider.ID, tron.ID)
		return err == nil && cur.LastScannedBlock > 0
	})

	// --- deposit: injected into the chain, never handed to the services ------------
	deposit := a.DevChain.Deposit(tron, usdt.ID, depositAddr.Address, depositAddr.Memo, money.MustParse("100"))
	t.Logf("injected deposit %s at block %d", deposit.Hash, *deposit.BlockNumber)

	waitFor(t, ctx, "the scanner to record the deposit", func() bool {
		_, err := a.Chain.TransactionByHash(ctx, tron.ID, deposit.Hash)
		return err == nil
	})
	waitFor(t, ctx, "the deposit to be credited", func() bool {
		pays, err := a.Payments.List(ctx, merchant.ID, nil, 10, 0)
		return err == nil && len(pays) == 1 && pays[0].Payment.Status == store.PaymentStatusCredited
	})

	bal, err := a.Ledger.MerchantBalance(ctx, merchant.ID, usdt.ID)
	must(t, err, "merchant balance")
	if !bal.Available.Equal(money.MustParse("99")) {
		t.Fatalf("available balance should be 99 after a 100 deposit with 100bps fee, got %s", bal.Available)
	}
	tx, err := a.Chain.TransactionByHash(ctx, tron.ID, deposit.Hash)
	must(t, err, "deposit transaction")
	if tx.Status != store.TxStatusConfirmed || tx.Confirmations < tron.RequiredConfirmations {
		t.Fatalf("deposit tx should be confirmed with at least %d confirmations, got %s/%d", tron.RequiredConfirmations, tx.Status, tx.Confirmations)
	}
	if tx.BlockNumber.Int64 != *deposit.BlockNumber {
		t.Errorf("deposit recorded at block %d, chain mined it at %d", tx.BlockNumber.Int64, *deposit.BlockNumber)
	}

	// --- payout: approved, then broadcast and settled by the worker ----------------
	destSeed := sha256.Sum256([]byte("scan-dest" + suffix))
	dest := devchain.Format(tron.Family, destSeed[:])
	w, err := a.Payouts.Request(ctx, payoutInput(merchant.ID, usdt.ID, dest, "50", alice.ID))
	must(t, err, "request withdrawal")
	_, err = a.Payouts.Approve(ctx, merchant.ID, w.ID, bob.ID)
	must(t, err, "approve withdrawal")

	waitFor(t, ctx, "the worker to broadcast the payout", func() bool {
		got, err := a.Payouts.Get(ctx, merchant.ID, w.ID)
		return err == nil && got.TransactionID.Valid
	})
	waitFor(t, ctx, "the payout to complete", func() bool {
		got, err := a.Payouts.Get(ctx, merchant.ID, w.ID)
		return err == nil && got.Status == store.WithdrawalStatusConfirmed
	})

	got, err := a.Payouts.Get(ctx, merchant.ID, w.ID)
	must(t, err, "get withdrawal")
	if got.FromAddressID.UUID != hotAddr.ID {
		t.Errorf("payout should have been sent from the hot address")
	}
	if !money.FromNumeric(got.NetworkFeeNative).IsPositive() {
		t.Errorf("a completed payout should record the network fee it paid, got %s", money.FromNumeric(got.NetworkFeeNative))
	}
	outTx, err := a.Chain.Transaction(ctx, got.TransactionID.UUID)
	must(t, err, "payout transaction")
	if outTx.Status != store.TxStatusConfirmed {
		t.Errorf("payout transaction should be confirmed, is %s", outTx.Status)
	}
	if _, ok := a.DevChain.MinedAt(tron.ID, outTx.Hash); !ok {
		t.Errorf("the payout hash %s is not one the fake chain mined", outTx.Hash)
	}

	// Balance: 99 available, minus 50 sent and 0.25 fee.
	bal, err = a.Ledger.MerchantBalance(ctx, merchant.ID, usdt.ID)
	must(t, err, "merchant balance after payout")
	if !bal.Available.Equal(money.MustParse("48.75")) || !bal.Locked.IsZero() {
		t.Fatalf("after the payout expected 48.75 available and nothing locked, got %s available / %s locked", bal.Available, bal.Locked)
	}

	stopWorker()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("worker did not stop within 10s of cancellation")
	}
}

// waitFor polls until cond holds, and fails the test with what it was waiting
// for when it never does.
func waitFor(t *testing.T, ctx context.Context, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatalf("context cancelled while waiting for %s: %v", what, ctx.Err())
		}
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after 25s waiting for %s", what)
}
