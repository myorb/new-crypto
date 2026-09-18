package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/chain/devchain"
	"templ-app/internal/identity"
	"templ-app/internal/org"
	"templ-app/internal/postgres"
	"templ-app/internal/store"
)

// devRates are placeholder USD prices so quoting works without a rate feed.
// Source is "dev"; never enabled in production.
var devRates = map[string]float64{
	"USDT": 1, "USDC": 1, "BTC": 66500, "ETH": 2600, "BNB": 580, "POL": 0.45, "TRX": 0.33, "SOL": 165, "TON": 6.5,
}

// SeedResult reports what Seed made available.
type SeedResult struct {
	Email, Password string
	Organization    string
	Created         bool
}

// Seed makes a development environment usable: a demo user and merchant,
// deposit and hot wallets on every enabled network (dev adapter), and fresh
// placeholder rates. Idempotent; refuses to run in production.
func (a *App) Seed(ctx context.Context) (SeedResult, error) {
	if a.Config.Production() {
		return SeedResult{}, errors.New("app: dev seed is not allowed in production")
	}
	if !a.Config.DevChainAdapter {
		return SeedResult{}, errors.New("app: dev seed needs DEV_CHAIN_ADAPTER=true")
	}
	res := SeedResult{Email: a.Config.DevSeedEmail, Password: a.Config.DevSeedPassword, Organization: "demo"}

	user, err := a.Identity.UserByEmail(ctx, res.Email)
	if postgres.IsNotFound(err) {
		if user, err = a.Identity.Register(ctx, res.Email, res.Password, "Demo Merchant"); err != nil {
			return res, err
		}
		res.Created = true
	} else if err != nil {
		return res, err
	}

	o, err := a.Org.GetBySlug(ctx, res.Organization)
	if postgres.IsNotFound(err) {
		o, err = a.Org.Create(ctx, org.CreateInput{Slug: res.Organization, Name: "Demo Merchant", DefaultCurrency: "USD", OwnerID: user.ID})
		if err != nil {
			return res, err
		}
		if err := a.Org.SetStatus(ctx, o.ID, store.OrganizationStatusActive); err != nil {
			return res, err
		}
	} else if err != nil {
		return res, err
	} else if _, err := a.Org.Role(ctx, o.ID, user.ID); errors.Is(err, org.ErrNotMember) {
		_ = a.Org.SetMemberRole(ctx, o.ID, user.ID, store.OrganizationRoleOwner)
	}

	if err := a.seedWallets(ctx); err != nil {
		return res, err
	}
	return res, a.RefreshDevRates(ctx)
}

// seedWallets creates a deposit and a hot wallet (with a hot address) per
// enabled network for the network's preferred provider.
func (a *App) seedWallets(ctx context.Context) error {
	existing, err := a.Chain.Wallets(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, w := range existing {
		have[w.Wallet.Name] = true
	}
	hot, err := a.Chain.PlatformAddresses(ctx)
	if err != nil {
		return err
	}
	haveHot := map[int16]bool{}
	for _, h := range hot {
		if h.Address.Kind == store.WalletKindHot {
			haveHot[h.Address.NetworkID] = true
		}
	}
	nets, err := a.Catalog.Networks(ctx)
	if err != nil {
		return err
	}
	keyRef := "dev://not-a-real-key"
	for _, n := range nets {
		route, err := a.Catalog.RouteDeposit(ctx, n.ID, nil)
		if err != nil {
			continue
		}
		if name := "dev deposits " + n.Code; !have[name] {
			if _, err := a.Chain.CreateWallet(ctx, chain.WalletInput{ProviderID: route.ProviderID, NetworkID: n.ID, Kind: store.WalletKindDeposit, Name: name, KeyRef: &keyRef}); err != nil {
				return fmt.Errorf("seed deposit wallet %s: %w", n.Code, err)
			}
		}
		if haveHot[n.ID] {
			continue
		}
		hw, err := a.Chain.CreateWallet(ctx, chain.WalletInput{ProviderID: route.ProviderID, NetworkID: n.ID, Kind: store.WalletKindHot, Name: "dev hot " + n.Code, KeyRef: &keyRef})
		if err != nil {
			return fmt.Errorf("seed hot wallet %s: %w", n.Code, err)
		}
		seed := sha256.Sum256([]byte("dev-hot-" + n.Code))
		if _, err := a.Chain.RegisterPlatformAddress(ctx, hw.ID, store.WalletKindHot, devchain.Format(n.Family, seed[:]), nil, nil); err != nil {
			return fmt.Errorf("seed hot address %s: %w", n.Code, err)
		}
	}
	return nil
}

// RefreshDevRates records placeholder USD rates for every enabled asset so
// quotes never go stale in development. The worker calls it periodically.
func (a *App) RefreshDevRates(ctx context.Context) error {
	if a.Config.Production() {
		return nil
	}
	assets, err := a.Catalog.Assets(ctx)
	if err != nil {
		return err
	}
	for _, as := range assets {
		price, ok := devRates[as.Symbol]
		if !ok {
			if as.IsStablecoin {
				price = 1
			} else {
				price = 10
			}
		}
		if _, err := a.Pricing.RecordRate(ctx, as.ID, "USD", decimal.NewFromFloat(price), "dev"); err != nil {
			return err
		}
	}
	return nil
}

// DemoLogin is a convenience for logs: the credentials Seed guarantees.
func (a *App) DemoLogin() identity.Client { return identity.Client{} }
