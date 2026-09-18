package app_test

// Customers and payment links against a real database. Run with:
//
//	TEST_DATABASE_URL=postgres://templ_app:...@localhost:5432/templ_app?sslmode=disable go test ./internal/app/ -run Integration -v
//
// It creates a merchant, a customer and two payment links, opens one link
// twice and checks that the invoices carry their payer and their link, that a
// blocked customer cannot open an invoice, and that the dashboard figures add
// up. Rows are left in place, like the lifecycle test.

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"templ-app/internal/app"
	"templ-app/internal/chain"
	"templ-app/internal/chain/devchain"
	"templ-app/internal/checkout"
	"templ-app/internal/customers"
	"templ-app/internal/money"
	"templ-app/internal/postgres"
	"templ-app/internal/pricing"
	"templ-app/internal/secrets"
	"templ-app/internal/store"
)

func TestIntegrationCustomersAndPaymentLinks(t *testing.T) {
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
	must(t, err, "build app")
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	owner, err := a.Identity.Register(ctx, "carol+"+suffix+"@example.com", "correct horse battery", "Carol")
	must(t, err, "register owner")
	merchant, err := a.Org.Create(ctx, orgInput("links-"+suffix, owner.ID))
	must(t, err, "create org")
	must(t, a.Org.SetStatus(ctx, merchant.ID, store.OrganizationStatusActive), "activate org")

	usdt, err := a.Catalog.AssetByCode(ctx, "USDT_TRON")
	must(t, err, "usdt asset")
	_, err = a.Pricing.RecordRate(ctx, usdt.ID, "USD", money.MustParse("1"), "test")
	must(t, err, "record rate")
	_, err = a.Pricing.ReplaceSchedule(ctx, pricing.ScheduleInput{
		OrganizationID: uuid.NullUUID{UUID: merchant.ID, Valid: true}, DepositFeeBps: 100, WithdrawalFeeBps: 50,
		NetworkFeePayer: store.FeePayerPlatform, PassNetworkFee: true,
	})
	must(t, err, "fee schedule")

	// --- customers -----------------------------------------------------------------
	email := "payer+" + suffix + "@example.com"
	name, country, external := "Nordic Supply AB", "SE", "cust-"+suffix
	cust, err := a.Customers.Create(ctx, merchant.ID, customers.Input{Email: &email, Name: &name, CountryCode: &country, ExternalID: &external})
	must(t, err, "create customer")
	if cust.Status != store.CustomerStatusActive {
		t.Fatalf("new customer should be active, got %s", cust.Status)
	}
	if _, err := a.Customers.Create(ctx, merchant.ID, customers.Input{Email: &email}); !errors.Is(err, customers.ErrDuplicate) {
		t.Fatalf("second customer with the same email: want ErrDuplicate, got %v", err)
	}
	if _, err := a.Customers.Create(ctx, merchant.ID, customers.Input{Name: &name}); !errors.Is(err, customers.ErrNoIdentity) {
		t.Fatalf("customer without email or external id: want ErrNoIdentity, got %v", err)
	}
	if again, err := a.Customers.Ensure(ctx, merchant.ID, "  "+email+"  "); err != nil || again.ID != cust.ID {
		t.Fatalf("Ensure should find the existing customer: %v", err)
	}

	// An invoice with a customer_email attaches to the payer record.
	in := checkoutInput(merchant.ID, usdt.ID)
	in.CustomerEmail = &email
	inv, err := a.Checkout.Create(ctx, in)
	must(t, err, "invoice with customer email")
	if !inv.CustomerID.Valid || inv.CustomerID.UUID != cust.ID {
		t.Fatalf("invoice should point at the customer, got %v", inv.CustomerID)
	}

	// An unknown email creates the payer on the fly.
	fresh := "walkin+" + suffix + "@example.com"
	in = checkoutInput(merchant.ID, usdt.ID)
	in.CustomerEmail = &fresh
	inv2, err := a.Checkout.Create(ctx, in)
	must(t, err, "invoice with new customer email")
	if !inv2.CustomerID.Valid {
		t.Fatal("invoice for an unknown payer should have created a customer")
	}

	// --- payment links --------------------------------------------------------------
	desc := "Pro plan, annual"
	price := money.MustParse("1188")
	fixed, err := a.Checkout.CreateLink(ctx, checkout.LinkInput{
		OrganizationID: merchant.ID, Name: "Pro plan", Description: &desc, PriceCurrency: "USD",
		PriceAmount: &price, CollectEmail: true, AssetIDs: []int16{usdt.ID},
	})
	must(t, err, "create fixed-price link")
	if fixed.Slug == "" || len(fixed.Assets) != 1 || fixed.Assets[0].ID != usdt.ID {
		t.Fatalf("link should carry a slug and its asset, got %+v", fixed)
	}

	min, max := money.MustParse("5"), money.MustParse("500")
	one := int32(1)
	donation, err := a.Checkout.CreateLink(ctx, checkout.LinkInput{
		OrganizationID: merchant.ID, Name: "Donation", PriceCurrency: "USD",
		MinAmount: &min, MaxAmount: &max, MaxUses: &one, AssetIDs: []int16{usdt.ID},
	})
	must(t, err, "create open-amount link")

	if _, err := a.Checkout.CreateLink(ctx, checkout.LinkInput{
		OrganizationID: merchant.ID, Name: "Broken", PriceCurrency: "USD", PriceAmount: &price, MinAmount: &min,
	}); !errors.Is(err, checkout.ErrLinkPrice) {
		t.Fatalf("fixed price plus a range: want ErrLinkPrice, got %v", err)
	}
	if _, err := a.Checkout.CreateLink(ctx, checkout.LinkInput{
		OrganizationID: merchant.ID, Slug: fixed.Slug, Name: "Clash", PriceCurrency: "USD", PriceAmount: &price,
	}); !errors.Is(err, checkout.ErrLinkSlugTaken) {
		t.Fatalf("duplicate slug: want ErrLinkSlugTaken, got %v", err)
	}

	// Opening a fixed-price link creates an invoice priced by the link.
	must(t, a.Checkout.RecordLinkView(ctx, fixed.ID), "record view")
	payer := "linkpayer+" + suffix + "@example.com"
	linkInv, err := a.Checkout.OpenLink(ctx, fixed.Slug, nil, &payer)
	must(t, err, "open fixed link")
	if !linkInv.PaymentLinkID.Valid || linkInv.PaymentLinkID.UUID != fixed.ID {
		t.Fatalf("invoice should point at the link, got %v", linkInv.PaymentLinkID)
	}
	if got := money.FromNumeric(linkInv.PriceAmount); !got.Equal(price) {
		t.Fatalf("invoice price: want %s, got %s", price, got)
	}
	if !linkInv.CustomerID.Valid {
		t.Fatal("a link that collects an email should attach a customer")
	}

	// The open-amount link needs an amount, and enforces its bounds.
	if _, err := a.Checkout.OpenLink(ctx, donation.Slug, nil, nil); !errors.Is(err, checkout.ErrLinkNeedAmount) {
		t.Fatalf("open-amount link without an amount: want ErrLinkNeedAmount, got %v", err)
	}
	tooMuch := money.MustParse("5000")
	if _, err := a.Checkout.OpenLink(ctx, donation.Slug, &tooMuch, nil); !errors.Is(err, checkout.ErrLinkAmount) {
		t.Fatalf("amount above max: want ErrLinkAmount, got %v", err)
	}
	fifty := money.MustParse("50")
	donInv, err := a.Checkout.OpenLink(ctx, donation.Slug, &fifty, nil)
	must(t, err, "open donation link")
	if got := money.FromNumeric(donInv.PriceAmount); !got.Equal(fifty) {
		t.Fatalf("donation price: want 50, got %s", got)
	}

	// Paused and unknown links refuse payers.
	if _, err := a.Checkout.SetLinkStatus(ctx, merchant.ID, donation.ID, store.PaymentLinkStatusPaused); err != nil {
		t.Fatalf("pause link: %v", err)
	}
	if _, err := a.Checkout.OpenLink(ctx, donation.Slug, &fifty, nil); !errors.Is(err, checkout.ErrLinkClosed) {
		t.Fatalf("paused link: want ErrLinkClosed, got %v", err)
	}
	if _, err := a.Checkout.LinkBySlug(ctx, "nosuchlink"); !errors.Is(err, checkout.ErrLinkNotFound) {
		t.Fatalf("unknown slug: want ErrLinkNotFound, got %v", err)
	}

	// --- blocking --------------------------------------------------------------------
	reason := "chargeback fraud"
	blocked, err := a.Customers.Block(ctx, merchant.ID, cust.ID, &reason)
	must(t, err, "block customer")
	if blocked.Status != store.CustomerStatusBlocked || blocked.BlockedAt == nil {
		t.Fatalf("blocked customer should carry a timestamp, got %+v", blocked)
	}
	in = checkoutInput(merchant.ID, usdt.ID)
	in.CustomerEmail = &email
	if _, err := a.Checkout.Create(ctx, in); !errors.Is(err, checkout.ErrCustomerBlocked) {
		t.Fatalf("invoice for a blocked payer: want ErrCustomerBlocked, got %v", err)
	}
	if _, err := a.Customers.Unblock(ctx, merchant.ID, cust.ID); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	in = checkoutInput(merchant.ID, usdt.ID)
	in.CustomerEmail = &email
	if _, err := a.Checkout.Create(ctx, in); err != nil {
		t.Fatalf("invoice after unblocking: %v", err)
	}

	// --- dashboard read models ---------------------------------------------------------
	list, err := a.Customers.List(ctx, merchant.ID, nil, "USD", 50, 0)
	must(t, err, "list customers")
	var seen *customers.Summary
	for i := range list {
		if list[i].ID == cust.ID {
			seen = &list[i]
		}
	}
	if seen == nil {
		t.Fatal("the customer should appear in the list")
	}
	if seen.Invoices < 2 {
		t.Fatalf("customer should have at least 2 invoices, got %d", seen.Invoices)
	}
	if seen.Paid != 0 || !seen.Volume.IsZero() {
		t.Fatalf("nothing is paid yet: paid=%d volume=%s", seen.Paid, seen.Volume)
	}
	total, err := a.Customers.Count(ctx, merchant.ID, nil)
	must(t, err, "count customers")
	if total < 3 {
		t.Fatalf("expected at least 3 customers, got %d", total)
	}
	if _, err := a.Customers.Stats(ctx, merchant.ID, "USD"); err != nil {
		t.Fatalf("customer stats: %v", err)
	}
	invoices, err := a.Customers.Invoices(ctx, merchant.ID, cust.ID, 10, 0)
	must(t, err, "customer invoices")
	if len(invoices) < 2 {
		t.Fatalf("expected the customer's invoices, got %d", len(invoices))
	}

	links, err := a.Checkout.Links(ctx, merchant.ID, nil, 50, 0)
	must(t, err, "list links")
	var fixedRow *checkout.LinkSummary
	for i := range links {
		if links[i].ID == fixed.ID {
			fixedRow = &links[i]
		}
	}
	if fixedRow == nil {
		t.Fatal("the link should appear in the list")
	}
	if fixedRow.Invoices != 1 || fixedRow.ViewCount != 1 {
		t.Fatalf("link should have 1 invoice and 1 view, got %d / %d", fixedRow.Invoices, fixedRow.ViewCount)
	}
	if fixedRow.Paid != 0 || !fixedRow.Revenue.IsZero() {
		t.Fatalf("link revenue before payment: paid=%d revenue=%s", fixedRow.Paid, fixedRow.Revenue)
	}
	stats, err := a.Checkout.LinkStats(ctx, merchant.ID, "USD", time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	must(t, err, "link stats")
	if stats.Active < 1 || stats.Views < 1 {
		t.Fatalf("link stats: %+v", stats)
	}
	byID, err := a.Checkout.LinkAssets(ctx, []uuid.UUID{fixed.ID, donation.ID})
	must(t, err, "link assets")
	if len(byID[fixed.ID]) != 1 || byID[fixed.ID][0].Code != usdt.Code {
		t.Fatalf("link assets: %+v", byID[fixed.ID])
	}

	// --- a single-use link completes when its invoice is paid ---------------------------
	// The donation link has max_uses = 1, so paying its invoice closes it.
	tron, err := a.Catalog.NetworkByCode(ctx, "tron")
	must(t, err, "tron network")
	provider, err := a.Catalog.ProviderByCode(ctx, "tron_rpc")
	must(t, err, "tron provider")
	keyRef := "kms://test/" + suffix
	if _, err := a.Chain.CreateWallet(ctx, chain.WalletInput{
		ProviderID: provider.ID, NetworkID: tron.ID, Kind: store.WalletKindDeposit, Name: "deposits " + suffix, KeyRef: &keyRef,
	}); err != nil {
		t.Fatalf("deposit wallet: %v", err)
	}
	donInv, err = a.Checkout.SelectOption(ctx, donInv.ID, usdt.ID)
	must(t, err, "select option on the donation invoice")
	sel := donInv.Selected()
	if sel == nil || sel.Address == nil {
		t.Fatalf("no address allocated: %+v", donInv.Options)
	}
	payerSeed := sha256.Sum256([]byte("payer" + suffix))
	from := devchain.Format(tron.Family, payerSeed[:])
	if _, err := a.Chain.RecordTransaction(ctx, chain.ObservedTransaction{
		NetworkID: tron.ID, ProviderID: provider.ID, Hash: fakeHash("link" + suffix), Status: store.TxStatusConfirmed, Confirmations: 19,
		From: &from, To: sel.Address,
		Transfers: []chain.ObservedTransfer{{AssetID: usdt.ID, LogIndex: 0, From: &from, To: *sel.Address, Amount: fifty}},
	}); err != nil {
		t.Fatalf("record deposit: %v", err)
	}
	if paid, _ := a.Checkout.Get(ctx, donInv.ID); paid.Status != store.InvoiceStatusConfirmed {
		t.Fatalf("donation invoice should be confirmed, is %s", paid.Status)
	}
	used, err := a.Checkout.GetLink(ctx, merchant.ID, donation.ID)
	must(t, err, "reload the donation link")
	if used.UsesCount != 1 {
		t.Fatalf("link should have counted one use, got %d", used.UsesCount)
	}
	if used.Open(time.Now()) {
		t.Fatal("a link at max_uses should be closed")
	}
	rows, err := a.Checkout.Links(ctx, merchant.ID, nil, 50, 0)
	must(t, err, "list links after payment")
	for _, r := range rows {
		if r.ID == donation.ID {
			if r.Paid != 1 || !r.Revenue.Equal(fifty) {
				t.Fatalf("link revenue after payment: paid=%d revenue=%s", r.Paid, r.Revenue)
			}
		}
	}
}
