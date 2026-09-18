package app_test

import (
	"github.com/google/uuid"

	"templ-app/internal/checkout"
	"templ-app/internal/identity"
	"templ-app/internal/money"
	"templ-app/internal/org"
	"templ-app/internal/payouts"
)

func errInvalidCredentials() error { return identity.ErrInvalidCredentials }

func identityClient() identity.Client { return identity.Client{UserAgent: "integration-test"} }

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
