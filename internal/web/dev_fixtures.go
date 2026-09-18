package web

import (
	"context"
	"crypto/sha256"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"templ-app/internal/chain/devchain"
	"templ-app/internal/checkout"
	"templ-app/internal/events"
	"templ-app/internal/org"
	"templ-app/internal/payouts"
	"templ-app/internal/store"
)

// devFixtures makes the pages that list configuration non-empty: an API key,
// a webhook endpoint, a saved withdrawal address and a payment link. It is
// idempotent and only creates what is missing. Development only.
func (s *Server) devFixtures(ctx context.Context, v *viewer) error {
	orgID := v.orgID()

	if keys, err := s.app.Org.APIKeys(ctx, orgID); err != nil {
		return err
	} else if len(keys) == 0 {
		for _, sandbox := range []bool{false, true} {
			if _, _, err := s.app.Org.CreateAPIKey(ctx, orgID, org.KeyInput{
				Name: "Server key", Sandbox: sandbox, CreatedBy: uuid.NullUUID{UUID: v.User.ID, Valid: true},
			}); err != nil {
				return err
			}
		}
	}

	if s.app.Keys != nil {
		if eps, err := s.app.Events.Endpoints(ctx, orgID); err != nil {
			return err
		} else if len(eps) == 0 {
			desc := "Development endpoint"
			if _, _, err := s.app.Events.CreateEndpoint(ctx, orgID, events.EndpointInput{
				URL: "https://localhost:9443/webhooks", Description: &desc, CreatedBy: uuid.NullUUID{UUID: v.User.ID, Valid: true},
			}); err != nil {
				return err
			}
		}
	}

	if dests, err := s.app.Payouts.Addresses(ctx, orgID); err != nil {
		return err
	} else if len(dests) == 0 {
		nets, err := s.app.Catalog.Networks(ctx)
		if err != nil {
			return err
		}
		for i, n := range nets {
			if i == 2 {
				break
			}
			seed := sha256.Sum256([]byte("dev-payout-" + n.Code))
			if _, err := s.app.Payouts.AddAddress(ctx, orgID, n.ID, devchain.Format(n.Family, seed[:]), nil,
				"Treasury "+n.Name, true, uuid.NullUUID{UUID: v.User.ID, Valid: true}); err != nil {
				return err
			}
		}
	}

	if n, err := s.app.Checkout.CountLinks(ctx, orgID, nil); err != nil {
		return err
	} else if n == 0 {
		cur := v.Org.Organization.DefaultCurrency
		price := decimal.NewFromInt(49)
		min, max := decimal.NewFromInt(5), decimal.NewFromInt(500)
		expires := time.Now().AddDate(0, 1, 0)
		links := []checkout.LinkInput{
			{Name: "Pro plan · monthly", PriceCurrency: cur, PriceAmount: &price, CollectEmail: true, ExpiresAt: &expires},
			{Name: "Pay what you want", PriceCurrency: cur, MinAmount: &min, MaxAmount: &max, CollectEmail: true},
		}
		for _, in := range links {
			in.OrganizationID = orgID
			in.CreatedBy = uuid.NullUUID{UUID: v.User.ID, Valid: true}
			if _, err := s.app.Checkout.CreateLink(ctx, in); err != nil {
				return err
			}
		}
	}
	return nil
}

// devFixturesHandler creates the fixtures and returns to the dashboard.
func (s *Server) devFixturesHandler(w http.ResponseWriter, r *http.Request, v *viewer) {
	if err := s.devFixtures(r.Context(), v); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next"), "/dashboard/api-keys"), http.StatusSeeOther)
}

// devPayout requests a withdrawal from the first whitelisted destination so
// the payouts page has a row. It approves it as the same user only when the
// policy allows (the four-eyes rule otherwise leaves it pending approval).
func (s *Server) devPayout(w http.ResponseWriter, r *http.Request, v *viewer) {
	ctx := r.Context()
	orgID := v.orgID()
	if err := s.devFixtures(ctx, v); err != nil {
		s.fail(w, r, err)
		return
	}
	balances, err := s.app.Ledger.MerchantBalances(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	dests, err := s.app.Payouts.Addresses(ctx, orgID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, b := range balances {
		if !b.Available.IsPositive() {
			continue
		}
		asset, err := s.app.Catalog.Asset(ctx, b.AssetID)
		if err != nil {
			continue
		}
		var dest *store.WithdrawalAddress
		for i := range dests {
			if dests[i].WithdrawalAddress.NetworkID == asset.NetworkID {
				dest = &dests[i].WithdrawalAddress
				break
			}
		}
		if dest == nil {
			continue
		}
		amount := b.Available.Div(decimal.NewFromInt(2)).RoundFloor(int32(asset.Decimals))
		if !amount.IsPositive() {
			continue
		}
		if _, err := s.app.Payouts.Request(ctx, payoutRequest(orgID, asset.ID, dest, amount, v.User.ID)); err != nil {
			s.log.WarnContext(ctx, "dev payout skipped", "asset", asset.Code, "err", err)
			continue
		}
		break
	}
	http.Redirect(w, r, "/dashboard/payouts", http.StatusSeeOther)
}

func payoutRequest(orgID uuid.UUID, assetID int16, dest *store.WithdrawalAddress, amount decimal.Decimal, userID uuid.UUID) payouts.RequestInput {
	return payouts.RequestInput{
		OrganizationID: orgID, AssetID: assetID, ToAddress: dest.Address, ToMemo: dest.Memo, Amount: amount,
		RequestedBy: uuid.NullUUID{UUID: userID, Valid: true},
	}
}
