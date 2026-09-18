# Domain layout

The Go code is split by business domain. Each package under `internal/` owns
a set of tables (its queries live in `db/queries/<domain>.sql`, generated into
the shared `internal/store`) and exposes one `Service`. Layers depend only on
the layers below them.

```
delivery     web (templ pages, HTTP)      worker (background loops)      main.go
commerce     checkout   payments   payouts   treasury
money/chain  ledger     pricing    chain     events
platform     identity   org        reference audit
shared       store (sqlc)   postgres (pool, tx)   money (decimal)   secrets (crypto)
```

| package     | owns                                                                  | depends on                                   |
|-------------|-----------------------------------------------------------------------|----------------------------------------------|
| `identity`  | users, sessions, MFA (TOTP + recovery codes), social identities, login attempts | secrets                             |
| `org`       | organizations, members, invitations, API keys, per-merchant asset settings | secrets                                 |
| `reference` | networks, assets, providers (cached catalog), provider routing        | -                                            |
| `audit`     | audit log, idempotency keys                                           | -                                            |
| `ledger`    | accounts, journals, entries, balances; all standard postings          | money                                        |
| `pricing`   | exchange rates, fee schedules, quotes, deposit / withdrawal fees       | reference                                    |
| `chain`     | wallets, addresses, observed transactions and transfers, cursors, adapter registry | reference                        |
| `events`    | events, webhook endpoints, signed deliveries, provider callbacks       | secrets                                      |
| `customers` | payer records (identity, blocking); activity is summed from invoices   | -                                            |
| `checkout`  | invoices, payment options and payment links                            | pricing, chain, events, org, customers, reference |
| `payments`  | deposits: detect, confirm, credit, revert                             | checkout, pricing, ledger, chain, events     |
| `payouts`   | withdrawals: request, four-eyes approval, route, settle, release      | pricing, ledger, chain, events, reference    |
| `treasury`  | sweeps, fee top-ups, hot/cold rebalances                              | chain, ledger, reference                     |
| `app`       | configuration and wiring of everything above                          | all                                          |
| `web`       | HTTP routes and pages                                                 | app                                          |
| `worker`    | expiry, reconciliation, webhook delivery, housekeeping loops          | app                                          |

## Rules

**Nothing calls upward.** The ledger never imports checkout; it records a
`reference_type` / `reference_id` and nothing more. `chain` never imports
`payments`: it publishes incoming transfers through `chain.TransferHandler`
and `payments` subscribes in its constructor.

**Only the ledger writes money.** Other domains build a `ledger.Posting` from
the helpers in `ledger.go` (`PaymentCredited`, `WithdrawalRequested`, …) and
call `Post`. Postings are validated in Go, balanced by a deferred database
trigger at COMMIT, and idempotent on `(event_type, reference_type,
reference_id)`.

**One transaction per business change.** Every service has `WithTx(tx)`.
A commerce service opens the transaction and binds its collaborators to it,
so the payment row, the ledger journal, the invoice status change and the
outgoing event commit or roll back together. A service whose `pool` is nil
is already inside a transaction and never opens its own.

**Events are the only side channel.** Anything a merchant should learn about
is `events.Emit`-ed inside the same transaction; the worker delivers it later
with an HMAC-SHA256 signature (`Webhook-Signature: v1=<hex>` over
`<timestamp>.<body>`). Delivery URLs are checked against private ranges at
registration and again at dial time.

## Flows

Payment link: `checkout.OpenLink` (slug → price, assets and TTL from the link)
→ an ordinary invoice carrying `payment_link_id`, so everything below is the
deposit flow. `ConfirmPayment` counts the use and closes a link that reached
`max_uses`.

Deposit: scanner → `chain.RecordTransaction` → `payments.HandleIncomingTransfer`
→ `checkout.FindMatch` + `pricing.DepositFees` → payment `detected` →
(`worker` when confirmations reach the network threshold) `payments.Credit` →
`ledger.PaymentCredited` + `checkout.ConfirmPayment` → `payment.credited`,
`invoice.confirmed` events.

Payout: `payouts.Request` (fee, balance check, `ledger.WithdrawalRequested`
lock) → `Approve` by a different user → worker `Route` → provider broadcast →
`MarkBroadcast` → `ReconcileBroadcast` → `Complete`
(`ledger.WithdrawalCompleted` + network fee) or `Fail` (funds released).

## What is not here yet

- **Real provider adapters.** The interfaces and the loops that drive them
  exist (see *Chain adapters* below), but no adapter talks to a real network
  yet: `tron`, `evm`, `bitcoin` and `solana` each need a client against their
  RPC, plus a signing story. Wallets carry `key_ref` and providers carry
  `credentials_ref`; nothing resolves either, and `payment_providers.config`
  (endpoints, rate limits) is still unread. Only `chain/devchain` implements
  the interfaces today, against an in-memory chain.
- **Merchant REST API.** The dashboard reads through `internal/web`; the
  public API still has to be built on `org.AuthenticateAPIKey` +
  `audit.BeginIdempotent`.
- **Dashboard writes.** Every dashboard page reads real data, but the forms
  and dialogs in `blocks/` are still inert (`onsubmit="return false"`).
  Creating a key, endpoint, link or payout from the UI needs POST handlers;
  the services behind them already exist.
- **Hosted checkout page.** Payment links resolve (`checkout.LinkBySlug`,
  `OpenLink`) but nothing serves `/l/<slug>` yet: add the public route, count
  the view with `RecordLinkView` and render the invoice it opens.

## Chain adapters

A provider integration implements up to three interfaces in `internal/chain`:

| interface | methods | needed for |
|---|---|---|
| `Adapter` | `Code`, `DeriveAddress` | allocating deposit addresses |
| `Scanner` | `Head`, `Earliest`, `ScanBlock`, `TransactionStatus` | seeing deposits and confirming anything |
| `Broadcaster` | `Broadcast` | sending payouts, sweeps and top-ups |

`Adapter` is mandatory; the other two are optional and discovered by type
assertion, so a provider that only reports through callbacks simply omits
them. Adapters report raw facts and never decide finality: they say which
block a transaction is in, and the service turns that into a confirmation
count and a status against `networks.required_confirmations`.

Four worker loops drive them, for every enabled provider / network pair whose
adapter supports the interface:

| loop | does |
|---|---|
| `scan_chains` | advances each `chain_cursors` row, records what touches our addresses |
| `track_confirmations` | re-checks pending transactions, so transactions we broadcast confirm too |
| `broadcast_payouts` | routes approved withdrawals to a hot address and sends them |
| `broadcast_treasury` | sends queued sweeps, fee top-ups and rebalances |

Sending is idempotent per business object: `chain.Send` passes a `Reference`
(`withdrawal:<id>`) that the adapter must use as the provider-side
idempotency key, so a crash between the broadcast and `MarkBroadcast` cannot
pay twice. A broadcast that fails with `chain.ErrBroadcastRejected` fails the
payout and releases the funds; any other error leaves it queued to retry.

Scanning saves the cursor after every block and detects reorgs by parent
hash, rewinding by the network's confirmation depth. A cursor older than the
oldest block the provider still serves (`Earliest`) is skipped forward with a
warning, because no amount of retrying will fetch pruned blocks.

### The dev chain

`DEV_CHAIN_ADAPTER=true` registers `chain/devchain` for every routed provider.
It derives format-valid fake addresses and implements `Scanner` and
`Broadcaster` against an in-memory chain whose head advances one block per
`DEV_CHAIN_BLOCK_TIME` (default 1s, so a Tron deposit confirms in about 19
seconds). Nothing it produces exists on a real network.

Deposits only appear if something injects them, through `app.DevChain`:

```go
a.DevChain.Deposit(network, assetID, address, memo, money.MustParse("100"))
a.DevChain.Reject("withdrawal:" + id.String()) // next broadcast is refused
a.DevChain.Drop(network.ID, hash)              // a mined transaction disappears
```

The fake chain has no history and restarts at its genesis height, so on boot
the app lifts its head above the highest stored cursor (`StartAbove`) and the
scanner skips the unreachable range. Both directions are handled; a dev
database carried across restarts does not wedge the scanner.

## Dashboard (internal/web)

`web` maps HTTP to the services. Routing and session handling live in
`web.go` / `session.go`; each page has a view assembler (`view_*.go`) that
turns service results into the view model its block in `blocks/dashboard`
already expects, so the templates were left alone. `format.go` holds the
shared money, time and identifier formatting, including `fx`, which converts
asset amounts to the merchant's display currency using the latest stored
rates.

Sign-in posts to real handlers: password login with lockout, TOTP or a
recovery code when the user has MFA, sign-up that also creates the
organization, session cookie, organization switcher and sign-out. Form posts
are checked same-origin; `next` targets are restricted to relative paths.
Every dashboard route goes through `requireViewer`, which resolves the
session, enforces the second factor and loads the current membership.

Without `DATABASE_URL` the server keeps serving the old fixture pages so the
UI can be worked on alone.

Development helpers (mounted only when `GO_ENV` is not production):

| route | does |
|---|---|
| `/dev/simulate?n=14` | creates invoices and pays them through the real flow |
| `/dev/fixtures` | adds an API key, webhook endpoint, payout addresses and payment links |
| `/dev/payout` | requests a withdrawal from the available balance |

With `DEV_SEED=true` the app creates a demo user, merchant, wallets on every
network and placeholder rates at startup, and logs the credentials.

## Tests

```bash
task test               # unit tests, no database
task test:integration   # full lifecycle against the dev database
```

There are two integration tests. `internal/app/chain_integration_test.go`
starts the real worker against the dev chain and checks that an injected
deposit travels through scanning, confirmation tracking and reconciliation to
a credited payment on its own, and that an approved payout is broadcast,
confirmed and settled without the test touching `chain.RecordTransaction`.

The other (`internal/app/app_integration_test.go`) registers users,
creates a merchant, wallets, a rate and a fee schedule, then runs an invoice
through deposit, credit, withdrawal, approval, settlement and a reverted
deposit, and checks that every event was delivered to a test webhook with a
valid signature. It leaves its rows in place: the app role cannot delete
financial rows. Point `TEST_DATABASE_URL` at a development database only.

## Working with the schema

`sqlc.yaml` maps `uuid` → `uuid.UUID`, `timestamptz` → `time.Time`, nullable
text → `*string`; amounts stay `pgtype.Numeric` and go through
`money.FromNumeric` / `money.ToNumeric`. After changing a query or migration
run `task sqlc`.

The baseline migrations (00001–00009) are edited in place while the schema is
still v1. If the dev database was migrated before such an edit, goose thinks
it is current while columns are missing (and `SELECT *` column order no longer
matches the generated scanners). Rebuild it: `task db:reset && task db`.
