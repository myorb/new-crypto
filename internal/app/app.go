// Package app wires the domain services together. It is the only package
// that knows the whole dependency graph; handlers and workers receive an
// *App and use the services they need.
//
// Layering (each depends only on layers below it):
//
//	web, api, worker                 delivery
//	checkout, payments, payouts,     commerce
//	treasury
//	ledger, pricing, chain, events   money and chain
//	identity, org, reference, audit  platform
package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"templ-app/internal/audit"
	"templ-app/internal/chain"
	"templ-app/internal/chain/devchain"
	"templ-app/internal/checkout"
	"templ-app/internal/events"
	"templ-app/internal/identity"
	"templ-app/internal/ledger"
	"templ-app/internal/org"
	"templ-app/internal/payments"
	"templ-app/internal/payouts"
	"templ-app/internal/postgres"
	"templ-app/internal/pricing"
	"templ-app/internal/reference"
	"templ-app/internal/secrets"
	"templ-app/internal/treasury"
)

// Config is everything the app reads from the environment.
type Config struct {
	Env           string // GO_ENV: "production" or anything else (development)
	DatabaseURL   string // DATABASE_URL, least-privilege role
	EncryptionKey string // APP_ENCRYPTION_KEY: "v1:<64 hex>" (comma-separated for rotation)

	SessionTTL     time.Duration // SESSION_TTL, default 720h
	RateLock       time.Duration // RATE_LOCK, default 15m
	InvoiceTTL     time.Duration // INVOICE_TTL, default 1h
	MaxRateAge     time.Duration // MAX_RATE_AGE, default 10m
	WebhookTimeout time.Duration // WEBHOOK_TIMEOUT, default 10s

	// DevChainAdapter registers fake address derivation for every seeded
	// provider. Refused when Env is production.
	DevChainAdapter bool // DEV_CHAIN_ADAPTER=true
	// WebhookAllowPrivate turns off the SSRF guard so local endpoints work.
	WebhookAllowPrivate bool // WEBHOOK_ALLOW_PRIVATE=true
	// RequireWithdrawalWhitelist refuses payouts to unsaved addresses.
	RequireWithdrawalWhitelist bool   // REQUIRE_WITHDRAWAL_WHITELIST=true
	Issuer                     string // TOTP issuer name; default "Payments"

	// WebhookTLS is only set by tests to trust an httptest certificate.
	WebhookTLS *tls.Config
}

// Production reports whether the app runs with production safety rails.
func (c Config) Production() bool { return c.Env == "production" }

// ConfigFromEnv reads Config from the process environment.
func ConfigFromEnv() Config {
	return Config{
		Env:                        os.Getenv("GO_ENV"),
		DatabaseURL:                os.Getenv("DATABASE_URL"),
		EncryptionKey:              os.Getenv("APP_ENCRYPTION_KEY"),
		SessionTTL:                 envDuration("SESSION_TTL", 720*time.Hour),
		RateLock:                   envDuration("RATE_LOCK", 15*time.Minute),
		InvoiceTTL:                 envDuration("INVOICE_TTL", time.Hour),
		MaxRateAge:                 envDuration("MAX_RATE_AGE", 10*time.Minute),
		WebhookTimeout:             envDuration("WEBHOOK_TIMEOUT", 10*time.Second),
		DevChainAdapter:            envBool("DEV_CHAIN_ADAPTER"),
		WebhookAllowPrivate:        envBool("WEBHOOK_ALLOW_PRIVATE"),
		RequireWithdrawalWhitelist: envBool("REQUIRE_WITHDRAWAL_WHITELIST"),
		Issuer:                     os.Getenv("TOTP_ISSUER"),
	}
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envBool(key string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return v
}

// App holds every domain service, wired.
type App struct {
	Config Config
	Log    *slog.Logger
	Pool   *pgxpool.Pool
	Keys   *secrets.Keyring

	Catalog  *reference.Catalog
	Identity *identity.Service
	Org      *org.Service
	Audit    *audit.Service
	Ledger   *ledger.Service
	Pricing  *pricing.Service
	Chain    *chain.Service
	Adapters *chain.Registry
	Events   *events.Service
	Checkout *checkout.Service
	Payments *payments.Service
	Payouts  *payouts.Service
	Treasury *treasury.Service
}

// New connects to the database and builds the service graph. The encryption
// key is optional in development (MFA and webhook endpoints then refuse to
// enrol) and required in production.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("app: DATABASE_URL is required")
	}
	var keys *secrets.Keyring
	if cfg.EncryptionKey != "" {
		var err error
		if keys, err = secrets.LoadKeyring(cfg.EncryptionKey); err != nil {
			return nil, err
		}
	} else if cfg.Production() {
		return nil, fmt.Errorf("app: APP_ENCRYPTION_KEY is required in production")
	} else {
		log.Warn("APP_ENCRYPTION_KEY not set: MFA enrolment and webhook endpoints are disabled")
	}
	if cfg.DevChainAdapter && cfg.Production() {
		return nil, fmt.Errorf("app: DEV_CHAIN_ADAPTER cannot be enabled in production")
	}
	if cfg.WebhookAllowPrivate && cfg.Production() {
		return nil, fmt.Errorf("app: WEBHOOK_ALLOW_PRIVATE cannot be enabled in production")
	}

	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	a, err := Build(ctx, cfg, log, pool, keys)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return a, nil
}

// Build wires services on an existing pool (tests use this).
func Build(ctx context.Context, cfg Config, log *slog.Logger, pool *pgxpool.Pool, keys *secrets.Keyring) (*App, error) {
	a := &App{Config: cfg, Log: log, Pool: pool, Keys: keys}

	a.Catalog = reference.New(pool)
	if err := a.Catalog.Refresh(ctx); err != nil {
		return nil, err
	}
	a.Identity = identity.New(pool, keys, identity.Options{SessionTTL: cfg.SessionTTL, Issuer: cfg.Issuer})
	a.Org = org.New(pool)
	a.Audit = audit.New(pool)
	a.Ledger = ledger.New(pool)
	a.Pricing = pricing.New(pool, a.Catalog, pricing.Options{MaxRateAge: cfg.MaxRateAge, RateLock: cfg.RateLock})

	a.Adapters = chain.NewRegistry()
	if cfg.DevChainAdapter {
		providers, err := a.Catalog.Networks(ctx)
		if err != nil {
			return nil, err
		}
		codes := map[string]bool{}
		for _, n := range providers {
			routes, err := a.Catalog.ProvidersForNetwork(ctx, n.ID)
			if err != nil {
				return nil, err
			}
			for _, r := range routes {
				if p, err := a.Catalog.Provider(ctx, r.ProviderID); err == nil {
					codes[p.Code] = true
				}
			}
		}
		for code := range codes {
			devchain.Register(a.Adapters, a.Catalog, code)
		}
		log.Warn("dev chain adapter enabled: derived addresses are not real", "providers", len(codes))
	}
	a.Chain = chain.New(pool, a.Catalog, a.Adapters, log)
	a.Events = events.New(pool, keys, events.Options{Timeout: cfg.WebhookTimeout, AllowPrivate: cfg.WebhookAllowPrivate, TLSClientConfig: cfg.WebhookTLS})
	a.Checkout = checkout.New(pool, a.Catalog, a.Pricing, a.Chain, a.Events, a.Org, checkout.Options{DefaultTTL: cfg.InvoiceTTL, AllowPrivate: cfg.WebhookAllowPrivate})
	a.Payments = payments.New(pool, a.Catalog, a.Pricing, a.Ledger, a.Checkout, a.Chain, a.Events, log)
	a.Payouts = payouts.New(pool, a.Catalog, a.Pricing, a.Ledger, a.Chain, a.Events, payouts.Options{RequireWhitelist: cfg.RequireWithdrawalWhitelist}, log)
	a.Treasury = treasury.New(pool, a.Catalog, a.Chain, a.Ledger, log)
	return a, nil
}

// Close releases the pool.
func (a *App) Close() {
	if a != nil && a.Pool != nil {
		a.Pool.Close()
	}
}

// Ping checks database connectivity for health endpoints.
func (a *App) Ping(ctx context.Context) error {
	if a == nil || a.Pool == nil {
		return fmt.Errorf("app: no database")
	}
	return a.Pool.Ping(ctx)
}
