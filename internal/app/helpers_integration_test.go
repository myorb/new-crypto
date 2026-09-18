package app_test

import (
	"math/rand/v2"
	"net/netip"

	"github.com/google/uuid"

	"templ-app/internal/checkout"
	"templ-app/internal/identity"
	"templ-app/internal/money"
	"templ-app/internal/org"
	"templ-app/internal/payouts"
)

func errInvalidCredentials() error { return identity.ErrInvalidCredentials }

// testClientIP is unique per test process. The login lockout counts failures
// per email OR per IP, and a client with no IP is recorded as 0.0.0.0, so
// without this every run of every integration test shares one lockout bucket
// and the tenth run in fifteen minutes fails on a deliberate bad password.
var testClientIP = netip.AddrFrom4([4]byte{203, 0, 113, byte(1 + rand.IntN(254))})

func identityClient() identity.Client {
	return identity.Client{UserAgent: "integration-test", IP: &testClientIP}
}

func orgInput(slug string, owner uuid.UUID) org.CreateInput {
	return org.CreateInput{Slug: slug, Name: "Acme " + slug, DefaultCurrency: "USD", OwnerID: owner}
}

func apiKeyInput(createdBy uuid.UUID) org.KeyInput {
	return org.KeyInput{Name: "integration", CreatedBy: uuid.NullUUID{UUID: createdBy, Valid: true}}
}

func checkoutInput(orgID uuid.UUID, assetID int16) checkout.CreateInput {
	desc := "Order from integration test"
	return checkout.CreateInput{OrganizationID: orgID, PriceCurrency: "USD", PriceAmount: money.MustParse("100"), Description: &desc, AssetIDs: []int16{assetID}}
}

func payoutInput(orgID uuid.UUID, assetID int16, dest, amount string, requestedBy uuid.UUID) payouts.RequestInput {
	return payouts.RequestInput{
		OrganizationID: orgID, AssetID: assetID, ToAddress: dest, Amount: money.MustParse(amount),
		RequestedBy: uuid.NullUUID{UUID: requestedBy, Valid: true},
	}
}
