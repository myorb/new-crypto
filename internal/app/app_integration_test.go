package app_test

// End-to-end test against a real database. Run with:
//
//	TEST_DATABASE_URL=postgres://templ_app:...@localhost:5432/templ_app?sslmode=disable go test ./internal/app/ -run Integration -v
//
// It creates users, an organization, wallets, an invoice, a deposit and a
// withdrawal with random identifiers and leaves them in place (the app role
// cannot delete financial rows by design). Point it at a dev database.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"templ-app/internal/app"
	"templ-app/internal/chain"
	"templ-app/internal/chain/devchain"
	"templ-app/internal/events"
	"templ-app/internal/money"
	"templ-app/internal/payouts"
	"templ-app/internal/postgres"
	"templ-app/internal/pricing"
	"templ-app/internal/secrets"
	"templ-app/internal/store"
)

const testKey = "v1:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestIntegrationPaymentLifecycle(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	keys, _ := secrets.LoadKeyring(testKey)
	cfg := app.Config{DatabaseURL: url, EncryptionKey: testKey, DevChainAdapter: true, WebhookAllowPrivate: true,
		SessionTTL: time.Hour, RateLock: 15 * time.Minute, InvoiceTTL: time.Hour, MaxRateAge: 10 * time.Minute, WebhookTimeout: 5 * time.Second}
	a, err := app.Build(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)), pool, keys)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	// --- platform: users, merchant ------------------------------------------------
	alice, err := a.Identity.Register(ctx, "alice+"+suffix+"@example.com", "correct horse battery", "Alice")
	must(t, err, "register alice")
	bob, err := a.Identity.Register(ctx, "bob+"+suffix+"@example.com", "correct horse battery", "Bob")
	must(t, err, "register bob")
	if _, err := a.Identity.Authenticate(ctx, alice.Email, "wrong", identityClient()); !errors.Is(err, errInvalidCredentials()) {
		t.Fatalf("bad password: %v", err)
	}
	if _, err := a.Identity.Authenticate(ctx, alice.Email, "correct horse battery", identityClient()); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	token, _, err := a.Identity.CreateSession(ctx, alice.ID, uuid.NullUUID{}, identityClient())
	must(t, err, "create session")
	principal, err := a.Identity.Resolve(ctx, token)
	must(t, err, "resolve session")
	if principal.User.ID != alice.ID {
		t.Fatal("session resolved to wrong user")
	}

	merchant, err := a.Org.Create(ctx, orgInput("acme-"+suffix, alice.ID))
	must(t, err, "create org")
	must(t, a.Org.SetStatus(ctx, merchant.ID, store.OrganizationStatusActive), "activate org")
	if err := a.Org.RequireRole(ctx, merchant.ID, alice.ID, store.OrganizationRoleOwner); err != nil {
		t.Fatalf("alice should be owner: %v", err)
	}
	invToken, _, err := a.Org.Invite(ctx, merchant.ID, alice.ID, bob.Email, store.OrganizationRoleFinance, 0)
	must(t, err, "invite bob")
	if _, err := a.Org.AcceptInvitation(ctx, invToken, bob); err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	rawKey, _, err := a.Org.CreateAPIKey(ctx, merchant.ID, apiKeyInput(alice.ID))
	must(t, err, "create api key")
	if p, err := a.Org.AuthenticateAPIKey(ctx, rawKey, nil); err != nil || p.Organization.ID != merchant.ID {
		t.Fatalf("authenticate api key: %v", err)
	}

	// --- reference and chain setup -----------------------------------------------
	tron, err := a.Catalog.NetworkByCode(ctx, "tron")
	must(t, err, "tron network")
	usdt, err := a.Catalog.AssetByCode(ctx, "USDT_TRON")
	must(t, err, "usdt asset")
	trx, err := a.Catalog.NativeAsset(ctx, tron.ID)
	must(t, err, "trx asset")
	provider, err := a.Catalog.ProviderByCode(ctx, "tron_rpc")
	must(t, err, "tron provider")

	keyRef := "kms://test/" + suffix
	_, err = a.Chain.CreateWallet(ctx, chain.WalletInput{ProviderID: provider.ID, NetworkID: tron.ID, Kind: store.WalletKindDeposit, Name: "deposits " + suffix, KeyRef: &keyRef})
	must(t, err, "deposit wallet")
	hotWallet, err := a.Chain.CreateWallet(ctx, chain.WalletInput{ProviderID: provider.ID, NetworkID: tron.ID, Kind: store.WalletKindHot, Name: "hot " + suffix, KeyRef: &keyRef})
	must(t, err, "hot wallet")
	hotSeed := sha256.Sum256([]byte("hot" + suffix))
	hotAddr, err := a.Chain.RegisterPlatformAddress(ctx, hotWallet.ID, store.WalletKindHot, devchain.Format(tron.Family, hotSeed[:]), nil, nil)
	must(t, err, "hot address")

	_, err = a.Pricing.RecordRate(ctx, usdt.ID, "USD", money.MustParse("1"), "test")
	must(t, err, "record rate")
	_, err = a.Pricing.ReplaceSchedule(ctx, pricing.ScheduleInput{
		OrganizationID: uuid.NullUUID{UUID: merchant.ID, Valid: true}, DepositFeeBps: 100, WithdrawalFeeBps: 50, NetworkFeePayer: store.FeePayerPlatform, PassNetworkFee: true,
	})
	must(t, err, "fee schedule")

	// --- webhook endpoint capturing deliveries -------------------------------------
	var mu sync.Mutex
	var received []events.Envelope
	var sigOK = true
	var secret string
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, _ := strconv.ParseInt(r.Header.Get(events.HeaderTimestamp), 10, 64)
		var env events.Envelope
		_ = json.Unmarshal(body, &env)
		mu.Lock()
		if !events.Verify(secret, r.Header.Get(events.HeaderSignature), ts, body, time.Minute) {
			sigOK = false
		}
		received = append(received, env)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	// rebuild the app so the webhook client trusts the test server's certificate
	cfg.WebhookTLS = hook.Client().Transport.(*http.Transport).TLSClientConfig
	a, err = app.Build(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)), pool, keys)
	must(t, err, "rebuild app with test TLS")
	secret, _, err = a.Events.CreateEndpoint(ctx, merchant.ID, events.EndpointInput{URL: hook.URL + "/hook"})
	must(t, err, "create endpoint")

	// --- checkout ---------------------------------------------------------------------
	inv, err := a.Checkout.Create(ctx, checkoutInput(merchant.ID, usdt.ID))
	must(t, err, "create invoice")
	if len(inv.Options) != 1 || !money.FromNumeric(inv.Options[0].AmountDue).Equal(money.MustParse("100")) {
		t.Fatalf("expected one option of 100 USDT, got %+v", inv.Options)
	}
	inv, err = a.Checkout.SelectOption(ctx, inv.ID, usdt.ID)
	must(t, err, "select option")
	sel := inv.Selected()
	if sel == nil || sel.Address == nil {
		t.Fatalf("no address allocated: %+v", inv.Options)
	}
	if again, err := a.Checkout.SelectOption(ctx, inv.ID, usdt.ID); err != nil || *again.Selected().Address != *sel.Address {
		t.Fatalf("re-select should be idempotent: %v", err)
	}

	// --- deposit observed on chain, confirmed -----------------------------------------
	payer := devchain.Format(tron.Family, hotSeed[:16])
	obs, err := a.Chain.RecordTransaction(ctx, chain.ObservedTransaction{
		NetworkID: tron.ID, ProviderID: provider.ID, Hash: fakeHash("dep" + suffix), Status: store.TxStatusConfirmed, Confirmations: 19,
		From: &payer, To: sel.Address, Transfers: []chain.ObservedTransfer{{AssetID: usdt.ID, LogIndex: 0, From: &payer, To: *sel.Address, Amount: money.MustParse("100")}},
	})
	must(t, err, "record deposit")
	if len(obs.New) != 1 {
		t.Fatalf("expected one new incoming transfer, got %d", len(obs.New))
	}
	pays, err := a.Payments.ForInvoice(ctx, inv.ID)
	must(t, err, "payments for invoice")
	if len(pays) != 1 || pays[0].Status != store.PaymentStatusCredited {
		t.Fatalf("expected one credited payment, got %+v", pays)
	}
	if !money.FromNumeric(pays[0].FeeAmount).Equal(money.MustParse("1")) {
		t.Fatalf("fee should be 1 USDT (100 bps), got %s", money.FromNumeric(pays[0].FeeAmount))
	}
	inv, _ = a.Checkout.Get(ctx, inv.ID)
	if inv.Status != store.InvoiceStatusConfirmed {
		t.Fatalf("invoice should be confirmed, is %s", inv.Status)
	}
	bal, err := a.Ledger.MerchantBalance(ctx, merchant.ID, usdt.ID)
	must(t, err, "balance")
	if !bal.Available.Equal(money.MustParse("99")) {
		t.Fatalf("available should be 99, got %s", bal.Available)
	}
	// replaying the same observation must not double credit
	if _, err := a.Chain.RecordTransaction(ctx, chain.ObservedTransaction{
		NetworkID: tron.ID, ProviderID: provider.ID, Hash: fakeHash("dep" + suffix), Status: store.TxStatusConfirmed, Confirmations: 25,
		Transfers: []chain.ObservedTransfer{{AssetID: usdt.ID, LogIndex: 0, From: &payer, To: *sel.Address, Amount: money.MustParse("100")}},
	}); err != nil {
		t.Fatal(err)
	}
	if pays, _ = a.Payments.ForInvoice(ctx, inv.ID); len(pays) != 1 {
		t.Fatalf("replay created a second payment")
	}
	if bal, _ = a.Ledger.MerchantBalance(ctx, merchant.ID, usdt.ID); !bal.Available.Equal(money.MustParse("99")) {
		t.Fatalf("replay changed the balance: %s", bal.Available)
	}

	// --- payout with four-eyes approval ------------------------------------------------
	destSeed := sha256.Sum256([]byte("dest" + suffix))
	dest := devchain.Format(tron.Family, destSeed[:])
	if _, err := a.Payouts.Request(ctx, payoutInput(merchant.ID, usdt.ID, dest, "500", alice.ID)); !errors.Is(err, payouts.ErrInsufficientFunds) {
		t.Fatalf("overdraw should fail with insufficient funds, got %v", err)
	}
	w, err := a.Payouts.Request(ctx, payoutInput(merchant.ID, usdt.ID, dest, "50", alice.ID))
	must(t, err, "request withdrawal")
	if !money.FromNumeric(w.FeeAmount).Equal(money.MustParse("0.25")) {
		t.Fatalf("withdrawal fee should be 0.25 (50 bps), got %s", money.FromNumeric(w.FeeAmount))
	}
	bal, _ = a.Ledger.MerchantBalance(ctx, merchant.ID, usdt.ID)
	if !bal.Available.Equal(money.MustParse("48.75")) || !bal.Locked.Equal(money.MustParse("50.25")) {
		t.Fatalf("after request: available %s locked %s", bal.Available, bal.Locked)
	}
	if _, err := a.Payouts.Approve(ctx, merchant.ID, w.ID, alice.ID); !errors.Is(err, payouts.ErrSelfApproval) {
		t.Fatalf("self approval should be refused, got %v", err)
	}
	if _, err := a.Payouts.Approve(ctx, merchant.ID, w.ID, bob.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	w, from, err := a.Payouts.Route(ctx, w.ID)
	must(t, err, "route")
	if w.Status != store.WithdrawalStatusQueued || from.ID != hotAddr.ID {
		t.Fatalf("route: status %s from %s", w.Status, from.Address)
	}
	fee := money.MustParse("0.5")
	outObs, err := a.Chain.RecordTransaction(ctx, chain.ObservedTransaction{
		NetworkID: tron.ID, ProviderID: provider.ID, Hash: fakeHash("wd" + suffix), Status: store.TxStatusPending, Confirmations: 0, FeeNative: &fee,
		From: &hotAddr.Address, To: &dest, Transfers: []chain.ObservedTransfer{{AssetID: usdt.ID, LogIndex: 0, From: &hotAddr.Address, To: dest, Amount: money.MustParse("50")}},
	})
	must(t, err, "record payout tx")
	if len(outObs.Transfers) != 1 || outObs.Transfers[0].Direction != store.TransferDirectionOut {
		t.Fatalf("payout transfer should be outgoing: %+v", outObs.Transfers)
	}
	if _, err := a.Payouts.MarkBroadcast(ctx, w.ID, outObs.Transaction.ID, nil); err != nil {
		t.Fatalf("mark broadcast: %v", err)
	}
	if _, err := a.Chain.UpdateConfirmations(ctx, outObs.Transaction.ID, 19, nil, store.TxStatusConfirmed); err != nil {
		t.Fatal(err)
	}
	st, err := a.Payouts.ReconcileBroadcast(ctx, 10)
	must(t, err, "reconcile payouts")
	if st.Completed < 1 {
		t.Fatalf("expected the withdrawal to complete: %+v", st)
	}
	w, _ = a.Payouts.Get(ctx, merchant.ID, w.ID)
	if w.Status != store.WithdrawalStatusConfirmed {
		t.Fatalf("withdrawal status %s", w.Status)
	}
	bal, _ = a.Ledger.MerchantBalance(ctx, merchant.ID, usdt.ID)
	if !bal.Available.Equal(money.MustParse("48.75")) || !bal.Locked.IsZero() {
		t.Fatalf("after payout: available %s locked %s", bal.Available, bal.Locked)
	}
	if j, err := a.Ledger.JournalFor(ctx, "withdrawal.network_fee", "withdrawal", w.ID); err != nil || j.ID == uuid.Nil {
		t.Fatalf("network fee journal in %s missing: %v", trx.Code, err)
	}

	// --- a dropped deposit is reverted -------------------------------------------------
	inv2, err := a.Checkout.Create(ctx, checkoutInput(merchant.ID, usdt.ID))
	must(t, err, "second invoice")
	inv2, err = a.Checkout.SelectOption(ctx, inv2.ID, usdt.ID)
	must(t, err, "select second")
	addr2 := *inv2.Selected().Address
	pending, err := a.Chain.RecordTransaction(ctx, chain.ObservedTransaction{
		NetworkID: tron.ID, ProviderID: provider.ID, Hash: fakeHash("drop" + suffix), Status: store.TxStatusPending,
		Transfers: []chain.ObservedTransfer{{AssetID: usdt.ID, LogIndex: 0, From: &payer, To: addr2, Amount: money.MustParse("100")}},
	})
	must(t, err, "record pending deposit")
	if inv2, _ = a.Checkout.Get(ctx, inv2.ID); inv2.Status != store.InvoiceStatusPaid {
		t.Fatalf("pending payment should mark invoice paid, is %s", inv2.Status)
	}
	if _, err := a.Chain.UpdateConfirmations(ctx, pending.Transaction.ID, 0, nil, store.TxStatusDropped); err != nil {
		t.Fatal(err)
	}
	pst, err := a.Payments.ReconcileDetected(ctx, 50)
	must(t, err, "reconcile payments")
	if pst.Reverted < 1 {
		t.Fatalf("expected a revert: %+v", pst)
	}
	if inv2, _ = a.Checkout.Get(ctx, inv2.ID); inv2.Status != store.InvoiceStatusNew {
		t.Fatalf("reverted invoice should be new again, is %s", inv2.Status)
	}
	if bal, _ = a.Ledger.MerchantBalance(ctx, merchant.ID, usdt.ID); !bal.Available.Equal(money.MustParse("48.75")) {
		t.Fatalf("dropped deposit changed balance: %s", bal.Available)
	}

	// --- webhooks were queued for every event and deliver with valid signatures --------
	evs, err := a.Events.Events(ctx, merchant.ID, nil, 100, 0)
	must(t, err, "list events")
	if len(evs) < 10 {
		t.Fatalf("expected a full event trail, got %d events", len(evs))
	}
	dst, err := a.Events.DeliverDue(ctx, 100)
	must(t, err, "deliver webhooks")
	mu.Lock()
	defer mu.Unlock()
	// stale deliveries from earlier runs (dead endpoints) may be claimed too; only ours must all succeed
	if dst.Succeeded < len(evs) || len(received) != len(evs) || !sigOK {
		t.Fatalf("deliveries: stats %+v received %d of %d, signatures ok: %v", dst, len(received), len(evs), sigOK)
	}
	types := map[string]bool{}
	for _, e := range received {
		types[e.Type] = true
	}
	for _, want := range []string{events.InvoiceCreated, events.InvoicePaid, events.InvoiceConfirmed, events.PaymentCredited, events.PaymentReverted, events.WithdrawalRequested, events.WithdrawalApproved, events.WithdrawalCompleted} {
		if !types[want] {
			t.Errorf("no %s webhook delivered", want)
		}
	}
}

func fakeHash(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:])
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
