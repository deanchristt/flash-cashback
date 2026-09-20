# Flash Cashback

A production-grade MVP of a flash cashback campaign: users earn 5% cashback on
payments, subject to a per-payment minimum, a per-user daily cap, and a global
campaign budget, and can redeem their balance.

The interesting part of this exercise isn't the four rules — those are an hour of
typing. It's **what has to be true before a feature can be trusted with real
money.** This README is organised around that question. The rules are the easy
part; the guarantees are the product.

- **Backend:** Go, PostgreSQL, Redis — `flash-cashback-service/`
- **Mobile:** React Native (Expo) — `flash-cashback-mobile/`

---

## The rules

| Rule | Value |
|------|-------|
| Cashback rate | 5% of the payment |
| Minimum payment to earn | 20,000 IDR (below earns nothing) |
| Per-user daily cap | 50,000 IDR of cashback per day |
| Campaign budget | 10,000,000 IDR total; when gone, the campaign is over |
| Redemption | Users can redeem their cashback balance |

---

## What I decided — and why (the judgment)

These are the decisions that separate a demo from something you'd let near money.
Each is a choice I can defend, and most have a rejected alternative.

### 1. Money is never a float. Ever.
All amounts are `int64` / `BIGINT` in IDR minor units (rupiah). 5% of a payment
can be fractional; float rounding drift is unacceptable when it accumulates over
millions of transactions. **Rounding is floor** (`amount * 5 / 100` in integer
math). Floor is conservative for the budget and is documented so it's a decision,
not an accident. The API rejects fractional amounts outright rather than silently
rounding client input.

### 2. Every award is idempotent.
Payments and redemptions carry a client-supplied **idempotency key**. A network
retry of the same payment must never award cashback twice — that's a direct money
leak. Enforced by a `UNIQUE (user_id, idempotency_key)` constraint; a retry
returns the *original* outcome. A second guard, `UNIQUE (source_type, source_id)`
on the ledger, makes a double-entry structurally impossible.

### 3. Postgres is the only source of truth for money. Redis is not.
Redis is fast but not the place to make money decisions — a Redis failure or
eviction must never cause an over-award or a lost balance. So **every money
decision happens inside a single Postgres transaction.** Redis is used only for
per-user rate limiting and short-TTL caching of read-only campaign status, and it
**fails open**: if Redis is down, requests are still served (availability is not
sacrificed to a cache).

### 4. Concurrency is the real threat, so it's handled explicitly.
All three limits are shared mutable state under contention. Naive
read-check-write loses money. The design:

- **Daily cap:** the user's daily tally row is locked `FOR UPDATE` before it's
  read and updated. Concurrent payments for one user serialise and can't jointly
  exceed 50,000/day.
- **Budget:** the single `campaign_budget` row is locked `FOR UPDATE`, so awards
  across *all* users serialise on it and can't overspend the 10,000,000 total.
- **Redemption:** an atomic guarded update
  (`UPDATE ... SET balance = balance - :amt WHERE balance >= :amt`) makes
  double-spend and negative balances impossible.
- **Lock order** is always (user daily row) → (global budget row). The budget
  row is a single shared row acquired last, so no deadlock cycle can form.
- **Backstops:** `CHECK (remaining >= 0)` and `CHECK (balance >= 0)` mean even a
  logic bug cannot persist an overspend or a negative balance.

These aren't asserted — they're **proven by concurrency tests** (see Testing).

### 5. Partial awards, not all-or-nothing.
When a payment would earn more than the remaining daily cap or budget, we award
`min(5%, daily_remaining, budget_remaining)` — as much as still fits — rather
than zero. This pays users exactly what the rules allow and lets the budget land
on *exactly* 10,000,000 instead of stopping early. The alternative (all-or-
nothing) is simpler but wastes budget and confuses users. *(This was a deliberate
choice; the rejected option is defensible too.)*

### 6. "Per day" means a calendar day in Asia/Jakarta.
"Per day" is meaningless without a timezone. A UTC boundary would reset the cap
at 07:00 local for an Indonesian audience. The daily bucket is the calendar date
in **Asia/Jakarta (WIB)**. tzdata is embedded in the binary so it resolves on a
minimal container.

### 7. An append-only ledger is the backbone.
Cashback is money, so the system is built around an **immutable ledger** of every
EARN/REDEEM movement. Materialized counters (`user_balance`,
`user_daily_cashback`, `campaign_budget`) are kept consistent with the ledger in
the same transaction — they make reads and the hot concurrency checks cheap,
while the ledger makes the system **auditable, reconcilable, and reconstructable**
(and makes clawback possible later, even though it's out of scope now). A test
asserts the invariant `sum(ledger) == materialized balance`.

### 8. It's operable, because it moves money.
Structured logs (JSON in prod), request IDs, `/healthz` (liveness), `/readyz`
(DB reachable), and `/metrics` in Prometheus format exposing budget remaining,
cashback awarded, redemptions, and rejection reasons. Graceful shutdown drains
in-flight requests. Panics are recovered per-request (the in-flight DB tx rolls
back) instead of crashing the process.

---

## What I deliberately left out — and why

Scope discipline matters as much as scope. These are conscious cuts, not
oversights:

- **Refunds / clawback** — explicitly out of scope. The ledger is designed so
  they *can* be added (append compensating entries) without a rewrite.
- **Authentication** — out of scope per the brief. Identity comes from an
  `X-User-Id` header; in production an auth gateway would set this from a
  verified token and the client could never spoof it.
- **Fraud / velocity checks** — a real money campaign needs abuse detection
  (self-payment loops, collusion, device fingerprinting). Noted, not built.
- **Sharded budget counter** — the single hot budget row is the throughput
  ceiling. Fine at MVP volume; at scale you'd shard the budget into N buckets or
  use a reservation pattern. Called out as a known limit, not hidden.
- **Outbox / event publishing** — awarding cashback would normally emit an event
  for downstream (notifications, ledger export). Left out; the transactional
  design leaves room for a transactional outbox later.
- **Multi-campaign, admin UI, notifications** — not needed for the MVP.

---

## Architecture

```
flash-cashback/
├── flash-cashback-service/            # Go backend
│   ├── cmd/server/                    # main: wiring, graceful shutdown
│   ├── config/                        # env-driven config (12-factor)
│   ├── internal/
│   │   ├── domain/                    # PURE rules: rates, caps, rounding, WIB day (unit-tested)
│   │   ├── store/                     # Postgres: migrations + atomic earn/redeem tx (integration-tested)
│   │   ├── redisx/                    # rate limiter + cache (non-authoritative, fails open)
│   │   ├── usecase/                   # application layer: validation + metrics
│   │   ├── httpapi/                   # transport: routing, middleware, error mapping
│   │   └── observability/            # structured logging + Prometheus metrics
│   ├── Dockerfile                     # multi-stage, distroless runtime
│   └── Makefile
├── flash-cashback-mobile/             # Expo React Native app
├── docker-compose.yml                 # Postgres + Redis + service, one command
└── init-testdb.sql                    # separate DB for integration tests
```

**Layering:** `httpapi → usecase → store → Postgres`, with `domain` as a pure,
dependency-free core. The money rules can be reasoned about and tested without a
database; the concurrency guarantees are tested with one.

### Data model

- `payments` — raw payments; idempotent per `(user_id, idempotency_key)`.
- `cashback_ledger` — append-only EARN/REDEEM; idempotent per `(source_type, source_id)`.
- `user_balance` — materialized redeemable balance; `CHECK (balance >= 0)`.
- `user_daily_cashback` — per-user, per-WIB-day tally; locked during awards.
- `campaign_budget` — single-row global budget; `CHECK (remaining >= 0)`.
- `redemptions` — idempotent per `(user_id, idempotency_key)`.

---

## Running the demo

### Backend (one command)

Requires Docker.

```bash
cd flash-cashback
docker compose up --build
```

This starts Postgres, Redis, and the service (which runs migrations on startup
and seeds the 10,000,000 IDR budget). The API is at `http://localhost:8080`.

Smoke test:

```bash
# Campaign status
curl localhost:8080/v1/campaign

# Make a 50,000 payment -> earns 2,500
curl -XPOST localhost:8080/v1/payments \
  -H 'X-User-Id: demo-user' -H 'Content-Type: application/json' \
  -d '{"amount":50000,"idempotency_key":"pay-1"}'

# Retry same key -> same result, no double award (replayed:true)
curl -XPOST localhost:8080/v1/payments \
  -H 'X-User-Id: demo-user' -H 'Content-Type: application/json' \
  -d '{"amount":50000,"idempotency_key":"pay-1"}'

# Balance, history
curl localhost:8080/v1/cashback/balance -H 'X-User-Id: demo-user'
curl localhost:8080/v1/cashback/history -H 'X-User-Id: demo-user'

# Redeem 1,000
curl -XPOST localhost:8080/v1/cashback/redeem \
  -H 'X-User-Id: demo-user' -H 'Content-Type: application/json' \
  -d '{"amount":1000,"idempotency_key":"red-1"}'

# Metrics
curl localhost:8080/metrics
```

### Mobile

Requires Node and the Expo tooling.

```bash
cd flash-cashback-mobile
npm install
npm start        # press w for web, or scan the QR with Expo Go
```

On a physical device, set the **API base URL** field in the app to your
machine's LAN IP (e.g. `http://192.168.1.10:8080`) — `localhost` on the phone is
the phone itself. The Android emulator uses `http://10.0.2.2:8080` by default.

---

### Testing the API with Postman

A ready-to-use collection lives in `postman/`. Import
`FlashCashback.postman_collection.json` and the `FlashCashback.Local`
environment, pick the environment, and run. It auto-generates idempotency keys,
asserts responses, and includes the edge cases (idempotent retry, below-minimum,
insufficient balance, fractional amount, missing user header). Headless run:

```bash
npx newman run postman/FlashCashback.postman_collection.json \
  -e postman/FlashCashback.Local.postman_environment.json
```

See `postman/README.md` for details.

## Testing

Two tiers, matched to what each can prove:

```bash
cd flash-cashback-service

# 1. Unit tests — pure rules: rate, threshold, floor rounding, cap math,
#    and the Jakarta day boundary. No dependencies.
make test-unit

# 2. Concurrency / correctness tests — the money-safety proofs. Need Postgres.
createdb cashback_test   # or use the cashback_test DB from docker compose
TEST_DATABASE_URL='postgres://cashback:cashback@localhost:5432/cashback_test?sslmode=disable' \
  make test-integration
```

The concurrency tests are the ones that matter for "trusted with money". They
fire dozens–hundreds of goroutines and assert:

- **Daily cap:** 50 concurrent large payments for one user → balance is exactly
  50,000, never more.
- **Budget:** 200 concurrent payments against a 100,000 budget → exactly 100,000
  awarded, remaining lands on 0, never negative.
- **Redemption:** 100 concurrent redeems of a 50,000 balance → exactly 50 succeed,
  50 fail cleanly, balance ends at 0 and never goes negative.
- **Idempotency:** the same payment sent 30× concurrently → awarded exactly once.
- **Ledger invariant:** `sum(ledger) == materialized balance`.

---

## Where it would break (honest limits)

- **Budget hot row.** Every award serialises on one `campaign_budget` row — the
  correctness guarantee *is* the bottleneck. At high write throughput this caps
  TPS. Fix: shard the budget into N reservation buckets, or a two-phase reserve/
  commit. Deliberately not done for the MVP.
- **Rate limiter fails open.** If Redis is down, the per-user limit stops
  applying (money correctness is unaffected — that's still in Postgres). A stricter
  posture would fail closed for writes; I chose availability for the MVP.
- **Single-region Postgres.** No HA/replication configured here. Production would
  need a managed primary with failover and PITR backups.
- **No clawback yet.** If a payment is later reversed, awarded cashback isn't
  automatically recovered. The ledger makes this addable; it's out of scope now.

## API reference

| Method | Path | Auth | Body | Notes |
|--------|------|------|------|-------|
| GET | `/v1/campaign` | — | | Budget total / remaining / active |
| GET | `/v1/cashback/balance` | `X-User-Id` | | Current redeemable balance |
| GET | `/v1/cashback/history` | `X-User-Id` | `?limit=` | Ledger entries, newest first |
| POST | `/v1/payments` | `X-User-Id` | `{amount, idempotency_key}` | Records payment, awards cashback |
| POST | `/v1/cashback/redeem` | `X-User-Id` | `{amount, idempotency_key}` | Debits balance |
| GET | `/healthz` `/readyz` `/metrics` | — | | Ops endpoints |
