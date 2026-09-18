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
| `checkout`  | invoices and payment options                                          | pricing, chain, events, org, reference       |
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

- **Provider adapters.** `chain.Adapter` only covers address derivation.
  Scanning blocks / provider callbacks and broadcasting signed transactions
  are worker concerns that need per-provider code (`tron`, `evm`, `bitcoin`,
  `solana`). `chain/devchain` derives format-valid fake addresses for
  development (`DEV_CHAIN_ADAPTER=true`).
- **Handlers.** `web` still serves the fixture-backed pages in `blocks/`.
  Wire each page to the services (list payments, balances, payouts …) and add
  the merchant REST API on top of `org.AuthenticateAPIKey` + `audit.BeginIdempotent`.
- **Customers and payment links** have UI but no schema; decide whether they
  are entities or views before adding packages.

## Tests

```bash
task test               # unit tests, no database
task test:integration   # full lifecycle against the dev database
```

The integration test (`internal/app/app_integration_test.go`) registers users,
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
