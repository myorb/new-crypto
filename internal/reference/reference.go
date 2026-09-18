// Package reference is the read-mostly catalog of networks, assets and
// payment providers. Every other domain resolves ids through it. Rows change
// only by migration (plus enable flags), so the whole catalog is cached in
// memory and refreshed on a TTL or after a write.
package reference

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"templ-app/internal/postgres"
	"templ-app/internal/store"
)

var (
	ErrUnknownNetwork  = errors.New("reference: unknown network")
	ErrUnknownAsset    = errors.New("reference: unknown asset")
	ErrUnknownProvider = errors.New("reference: unknown provider")
	ErrAssetDisabled   = errors.New("reference: asset is disabled")
	ErrNoProvider      = errors.New("reference: no enabled provider for network")
)

// DefaultTTL bounds how stale the in-memory catalog may get.
const DefaultTTL = 5 * time.Minute

// Asset is an asset joined with the network it lives on.
type Asset struct {
	store.Asset
	Network store.Network
}

// Catalog is the cached view of the reference tables. Copies made with
// WithTx share the same cache.
type Catalog struct {
	q *store.Queries
	c *cache
}

type cache struct {
	ttl time.Duration

	mu        sync.RWMutex
	loadedAt  time.Time
	networks  map[int16]store.Network
	netByCode map[string]store.Network
	assets    map[int16]store.Asset
	assetCode map[string]store.Asset
	providers map[int16]store.PaymentProvider
	provCode  map[string]store.PaymentProvider
	// provider ids per network, already ordered by priority
	provByNet map[int16][]store.ProviderNetwork
	provAsset map[[2]int16]store.ProviderAsset
}

// New builds a catalog bound to the pool. It loads lazily on first use.
func New(pool *pgxpool.Pool) *Catalog {
	return &Catalog{q: store.New(pool), c: &cache{ttl: DefaultTTL, provAsset: map[[2]int16]store.ProviderAsset{}}}
}

// WithTx returns a catalog that reads through tx (writes are rare; this is
// mostly so a transaction sees its own enable-flag changes).
func (c *Catalog) WithTx(tx pgx.Tx) *Catalog {
	return &Catalog{q: c.q.WithTx(tx), c: c.c}
}

// Refresh reloads everything from the database.
func (c *Catalog) Refresh(ctx context.Context) error {
	networks, err := c.q.ListNetworks(ctx)
	if err != nil {
		return fmt.Errorf("reference: load networks: %w", err)
	}
	assets, err := c.q.ListAssets(ctx)
	if err != nil {
		return fmt.Errorf("reference: load assets: %w", err)
	}
	providers, err := c.q.ListEnabledProviders(ctx)
	if err != nil {
		return fmt.Errorf("reference: load providers: %w", err)
	}

	n := make(map[int16]store.Network, len(networks))
	nc := make(map[string]store.Network, len(networks))
	for _, x := range networks {
		n[x.ID], nc[x.Code] = x, x
	}
	a := make(map[int16]store.Asset, len(assets))
	ac := make(map[string]store.Asset, len(assets))
	for _, x := range assets {
		a[x.ID], ac[x.Code] = x, x
	}
	p := make(map[int16]store.PaymentProvider, len(providers))
	pc := make(map[string]store.PaymentProvider, len(providers))
	for _, x := range providers {
		p[x.ID], pc[x.Code] = x, x
	}
	pn := make(map[int16][]store.ProviderNetwork, len(networks))
	for _, net := range networks {
		rows, err := c.q.ListProviderNetworks(ctx, net.ID)
		if err != nil {
			return fmt.Errorf("reference: load provider networks: %w", err)
		}
		for _, r := range rows {
			pn[net.ID] = append(pn[net.ID], r.ProviderNetwork)
		}
	}

	c.c.mu.Lock()
	c.c.networks, c.c.netByCode = n, nc
	c.c.assets, c.c.assetCode = a, ac
	c.c.providers, c.c.provCode = p, pc
	c.c.provByNet = pn
	c.c.provAsset = map[[2]int16]store.ProviderAsset{}
	c.c.loadedAt = time.Now()
	c.c.mu.Unlock()
	return nil
}

func (c *Catalog) ensure(ctx context.Context) error {
	c.c.mu.RLock()
	fresh := !c.c.loadedAt.IsZero() && time.Since(c.c.loadedAt) < c.c.ttl
	c.c.mu.RUnlock()
	if fresh {
		return nil
	}
	return c.Refresh(ctx)
}

// Network returns a network by id.
func (c *Catalog) Network(ctx context.Context, id int16) (store.Network, error) {
	if err := c.ensure(ctx); err != nil {
		return store.Network{}, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	n, ok := c.c.networks[id]
	if !ok {
		return store.Network{}, ErrUnknownNetwork
	}
	return n, nil
}

// NetworkByCode returns a network by its short code ("tron", "ethereum").
func (c *Catalog) NetworkByCode(ctx context.Context, code string) (store.Network, error) {
	if err := c.ensure(ctx); err != nil {
		return store.Network{}, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	n, ok := c.c.netByCode[code]
	if !ok {
		return store.Network{}, ErrUnknownNetwork
	}
	return n, nil
}

// Networks lists enabled networks ordered by code.
func (c *Catalog) Networks(ctx context.Context) ([]store.Network, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	out := make([]store.Network, 0, len(c.c.networks))
	for _, n := range c.c.networks {
		if n.IsEnabled {
			out = append(out, n)
		}
	}
	sortByCode(out, func(n store.Network) string { return n.Code })
	return out, nil
}

// Asset returns an asset with its network by id.
func (c *Catalog) Asset(ctx context.Context, id int16) (Asset, error) {
	if err := c.ensure(ctx); err != nil {
		return Asset{}, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	a, ok := c.c.assets[id]
	if !ok {
		return Asset{}, ErrUnknownAsset
	}
	return Asset{Asset: a, Network: c.c.networks[a.NetworkID]}, nil
}

// AssetByCode returns an asset with its network by code ("USDT_TRON").
func (c *Catalog) AssetByCode(ctx context.Context, code string) (Asset, error) {
	if err := c.ensure(ctx); err != nil {
		return Asset{}, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	a, ok := c.c.assetCode[code]
	if !ok {
		return Asset{}, ErrUnknownAsset
	}
	return Asset{Asset: a, Network: c.c.networks[a.NetworkID]}, nil
}

// EnabledAsset is Asset that also rejects disabled assets or networks.
func (c *Catalog) EnabledAsset(ctx context.Context, id int16) (Asset, error) {
	a, err := c.Asset(ctx, id)
	if err != nil {
		return Asset{}, err
	}
	if !a.IsEnabled || !a.Network.IsEnabled {
		return Asset{}, ErrAssetDisabled
	}
	return a, nil
}

// Assets lists enabled assets on enabled networks, ordered by network then code.
func (c *Catalog) Assets(ctx context.Context) ([]Asset, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	out := make([]Asset, 0, len(c.c.assets))
	for _, a := range c.c.assets {
		n := c.c.networks[a.NetworkID]
		if a.IsEnabled && n.IsEnabled {
			out = append(out, Asset{Asset: a, Network: n})
		}
	}
	sortByCode(out, func(a Asset) string { return a.Network.Code + "/" + a.Code })
	return out, nil
}

// NativeAsset returns the network's gas / fee asset.
func (c *Catalog) NativeAsset(ctx context.Context, networkID int16) (Asset, error) {
	if err := c.ensure(ctx); err != nil {
		return Asset{}, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	for _, a := range c.c.assets {
		if a.NetworkID == networkID && a.Kind == store.AssetKindNative {
			return Asset{Asset: a, Network: c.c.networks[networkID]}, nil
		}
	}
	return Asset{}, ErrUnknownAsset
}

// Provider returns an enabled provider by id.
func (c *Catalog) Provider(ctx context.Context, id int16) (store.PaymentProvider, error) {
	if err := c.ensure(ctx); err != nil {
		return store.PaymentProvider{}, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	p, ok := c.c.providers[id]
	if !ok {
		return store.PaymentProvider{}, ErrUnknownProvider
	}
	return p, nil
}

// ProviderByCode returns an enabled provider by code ("tron_rpc").
func (c *Catalog) ProviderByCode(ctx context.Context, code string) (store.PaymentProvider, error) {
	if err := c.ensure(ctx); err != nil {
		return store.PaymentProvider{}, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	p, ok := c.c.provCode[code]
	if !ok {
		return store.PaymentProvider{}, ErrUnknownProvider
	}
	return p, nil
}

// ProvidersForNetwork lists the enabled provider routes for a network, best
// priority first.
func (c *Catalog) ProvidersForNetwork(ctx context.Context, networkID int16) ([]store.ProviderNetwork, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.c.mu.RLock()
	defer c.c.mu.RUnlock()
	return append([]store.ProviderNetwork(nil), c.c.provByNet[networkID]...), nil
}

// RouteDeposit picks the provider that should generate deposit addresses on
// a network: the preferred one when it is enabled there, else the top priority.
func (c *Catalog) RouteDeposit(ctx context.Context, networkID int16, preferred *int16) (store.ProviderNetwork, error) {
	routes, err := c.ProvidersForNetwork(ctx, networkID)
	if err != nil {
		return store.ProviderNetwork{}, err
	}
	for _, r := range routes {
		if preferred != nil && r.ProviderID == *preferred && r.SupportsDeposits && r.SupportsAddressGeneration {
			return r, nil
		}
	}
	for _, r := range routes {
		if r.SupportsDeposits && r.SupportsAddressGeneration {
			return r, nil
		}
	}
	return store.ProviderNetwork{}, ErrNoProvider
}

// RouteWithdrawal picks the provider that should broadcast payouts on a network.
func (c *Catalog) RouteWithdrawal(ctx context.Context, networkID int16, preferred *int16) (store.ProviderNetwork, error) {
	routes, err := c.ProvidersForNetwork(ctx, networkID)
	if err != nil {
		return store.ProviderNetwork{}, err
	}
	for _, r := range routes {
		if preferred != nil && r.ProviderID == *preferred && r.SupportsWithdrawals {
			return r, nil
		}
	}
	for _, r := range routes {
		if r.SupportsWithdrawals {
			return r, nil
		}
	}
	return store.ProviderNetwork{}, ErrNoProvider
}

// ProviderAsset returns the provider-specific mapping for an asset, cached
// per (provider, asset) once fetched.
func (c *Catalog) ProviderAsset(ctx context.Context, providerID, assetID int16) (store.ProviderAsset, error) {
	key := [2]int16{providerID, assetID}
	c.c.mu.RLock()
	pa, ok := c.c.provAsset[key]
	c.c.mu.RUnlock()
	if ok {
		return pa, nil
	}
	pa, err := c.q.GetProviderAsset(ctx, store.GetProviderAssetParams{ProviderID: providerID, AssetID: assetID})
	if err != nil {
		return store.ProviderAsset{}, postgres.MapNotFound(err)
	}
	c.c.mu.Lock()
	if c.c.provAsset == nil {
		c.c.provAsset = map[[2]int16]store.ProviderAsset{}
	}
	c.c.provAsset[key] = pa
	c.c.mu.Unlock()
	return pa, nil
}

// SetAssetEnabled flips an asset's enable flag and refreshes the cache.
func (c *Catalog) SetAssetEnabled(ctx context.Context, assetID int16, enabled bool) error {
	if err := c.q.SetAssetEnabled(ctx, store.SetAssetEnabledParams{ID: assetID, IsEnabled: enabled}); err != nil {
		return err
	}
	return c.Refresh(ctx)
}

// SetNetworkEnabled flips a network's enable flag and refreshes the cache.
func (c *Catalog) SetNetworkEnabled(ctx context.Context, networkID int16, enabled bool) error {
	if err := c.q.SetNetworkEnabled(ctx, store.SetNetworkEnabledParams{ID: networkID, IsEnabled: enabled}); err != nil {
		return err
	}
	return c.Refresh(ctx)
}

// SetProviderEnabled flips a provider's enable flag and refreshes the cache.
func (c *Catalog) SetProviderEnabled(ctx context.Context, providerID int16, enabled bool) error {
	if err := c.q.SetProviderEnabled(ctx, store.SetProviderEnabledParams{ID: providerID, IsEnabled: enabled}); err != nil {
		return err
	}
	return c.Refresh(ctx)
}

func sortByCode[T any](xs []T, key func(T) string) {
	// insertion sort: catalogs are tiny and this avoids importing sort for a closure type
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && key(xs[j]) < key(xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
