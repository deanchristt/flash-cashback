# Flash Cashback — Architecture & Design

A visual walk-through of the system, written to be presented in the technical
interview. GitHub renders every Mermaid diagram below inline. Each section ends
with **talking points** — the sentences you can actually say out loud.

---

## 1. System overview

```mermaid
flowchart LR
    subgraph Client
      M["React Native / Expo app<br/>(balance, pay, redeem, history)"]
    end

    subgraph Backend["flash-cashback-service (Go)"]
      direction TB
      A["HTTP API<br/>chi router + middleware"]
      U["Usecase / service<br/>validation, metrics"]
      ST["Store<br/>atomic transactions"]
      OB["Observability<br/>logs + /metrics"]
      A --> U --> ST
      A --> OB
    end

    PG[("PostgreSQL<br/>SOURCE OF TRUTH")]
    RD[("Redis<br/>rate limit + cache<br/>NON-authoritative")]

    M -->|"HTTPS JSON · X-User-Id"| A
    ST --> PG
    A -.->|"rate limit (fail-open)"| RD
```

**Talking points**
- Clean layering: `HTTP → usecase → store → Postgres`, with a pure `domain`
  package (rules only, no I/O) so the money logic is testable in isolation.
- **One hard rule: Postgres is the only source of truth for money.** Redis never
  decides money — it only rate-limits and caches, and it *fails open* so a Redis
  outage degrades throttling, never correctness.

---

## 2. Earn cashback — the money-critical path

This is the request to spend the most time on. Everything happens inside **one
Postgres transaction** so it is atomic and crash-safe.

```mermaid
sequenceDiagram
    autonumber
    participant M as Mobile
    participant A as HTTP API
    participant R as Redis
    participant S as Service
    participant DB as PostgreSQL

    M->>A: POST /v1/payments {amount, idempotency_key} + X-User-Id
    A->>A: require user, request-id, validate amount (int only)
    A->>R: rate-limit check
    R-->>A: allow (or fail-open)
    A->>S: MakePayment(user, amount, key)

    S->>DB: BEGIN
    S->>DB: INSERT payment ON CONFLICT (user, key) DO NOTHING

    alt Duplicate key (retry)
        DB-->>S: no row returned
        S->>DB: SELECT stored outcome
        S->>DB: COMMIT
        S-->>A: {replayed: true}  (no second award)
    else New payment
        DB-->>S: payment_id
        Note over S: gross = floor(amount × 5%)
        alt amount < 20,000
            S->>DB: UPDATE payment status = no_cashback
            S->>DB: COMMIT
        else Eligible
            S->>DB: SELECT earned FROM user_daily_cashback ... FOR UPDATE
            S->>DB: SELECT remaining FROM campaign_budget ... FOR UPDATE
            Note over S: award = min(gross, dailyRemaining, budgetRemaining)
            alt award == 0 (cap or budget hit)
                S->>DB: UPDATE payment status = daily_cap_reached / budget_exhausted
                S->>DB: COMMIT
            else award > 0
                S->>DB: UPDATE daily earned += award
                S->>DB: UPDATE budget remaining -= award
                S->>DB: UPSERT user_balance += award
                S->>DB: INSERT ledger (EARN, source=payment)
                S->>DB: UPDATE payment awarded, status
                S->>DB: COMMIT
            end
        end
        S-->>A: {cashback_awarded, status, balance}
    end
    A-->>M: 201 Created (JSON)
```

**Talking points**
- **Idempotency first:** the very first statement tries to insert the payment on
  a unique `(user_id, idempotency_key)`. A retried request finds the conflict and
  returns the *original* outcome — a network retry can never pay cashback twice.
- **Two locks, deliberate order:** the per-user daily row is locked
  `FOR UPDATE`, then the single global budget row. Because the shared budget row
  is always taken *last*, no deadlock cycle can form.
- **Partial award:** `award = min(gross, dailyRemaining, budgetRemaining)` — we
  pay as much as still fits rather than all-or-nothing, so the budget lands
  exactly on 10,000,000.
- Balance, daily tally, budget, ledger, and payment status all move in the **same
  transaction** — all-or-nothing.

---

## 3. Redeem cashback

```mermaid
sequenceDiagram
    autonumber
    participant M as Mobile
    participant A as HTTP API
    participant S as Service
    participant DB as PostgreSQL

    M->>A: POST /v1/cashback/redeem {amount, idempotency_key}
    A->>S: Redeem(user, amount, key)
    S->>DB: BEGIN
    S->>DB: INSERT redemption ON CONFLICT (user, key) DO NOTHING
    alt Duplicate (retry of a success)
        DB-->>S: no row
        S->>DB: SELECT prior result
        S->>DB: COMMIT
        S-->>A: {replayed: true}
    else New
        DB-->>S: redemption_id
        S->>DB: UPDATE user_balance SET balance = balance - amount<br/>WHERE balance >= amount RETURNING balance
        alt Insufficient / no row
            S->>DB: ROLLBACK
            S-->>A: 422 insufficient_balance
        else OK
            S->>DB: INSERT ledger (REDEEM)
            S->>DB: UPDATE redemption balance_after
            S->>DB: COMMIT
            S-->>A: 200 {redeemed, balance}
        end
    end
```

**Talking points**
- The debit is a **single guarded update** (`... WHERE balance >= amount`). It
  can't go negative, and the DB `CHECK (balance >= 0)` is a final backstop.
- A failed redeem **rolls back the whole transaction**, including the redemption
  row — so the idempotency key isn't "burned" and the user can retry after they
  earn more.

---

## 4. Data model

```mermaid
erDiagram
    payments {
        bigserial id PK
        text     user_id
        bigint   amount
        text     idempotency_key "UNIQUE with user_id"
        bigint   cashback_awarded
        text     status
    }
    redemptions {
        bigserial id PK
        text     user_id
        bigint   amount
        text     idempotency_key "UNIQUE with user_id"
        bigint   balance_after
    }
    cashback_ledger {
        bigserial id PK
        text     user_id
        text     entry_type "EARN | REDEEM"
        bigint   amount
        bigint   balance_after
        text     source_type
        text     source_id "UNIQUE with source_type"
    }
    user_balance {
        text   user_id PK
        bigint balance "CHECK >= 0"
    }
    user_daily_cashback {
        text   user_id PK
        date   day PK "Asia/Jakarta"
        bigint earned
    }
    campaign_budget {
        int    id PK "singleton = 1"
        bigint total_budget
        bigint remaining "CHECK >= 0"
    }

    payments        ||--o| cashback_ledger : "produces EARN"
    redemptions     ||--o| cashback_ledger : "produces REDEEM"
```

**Talking points**
- The **append-only ledger** is the backbone: every movement of money is one
  immutable row. `user_balance`, `user_daily_cashback`, and `campaign_budget` are
  *materialized counters* kept consistent with the ledger in the same tx — cheap
  reads and cheap hot-path checks, while the ledger stays auditable and
  reconcilable. Invariant proven by a test: `sum(ledger) == balance`.
- Idempotency is enforced twice: on the source tables (`user_id, key`) and on the
  ledger (`source_type, source_id`) — a payment/redemption yields **at most one**
  ledger entry.

---

## 5. Award decision logic

```mermaid
flowchart TD
    A["Payment amount (IDR)"] --> B{"amount ≥ 20,000?"}
    B -- no --> Z["award = 0<br/>status: no_cashback"]
    B -- yes --> C["gross = floor(amount × 5%)"]
    C --> D["lock daily row → dailyRemaining = 50,000 − earned"]
    D --> E["lock budget row → budgetRemaining"]
    E --> F["award = min(gross, dailyRemaining, budgetRemaining)"]
    F --> G{"award > 0?"}
    G -- no --> Y["status: daily_cap_reached<br/>or budget_exhausted"]
    G -- yes --> H["apply: daily +=, budget −=,<br/>balance +=, ledger EARN"]
    H --> I{"award == gross?"}
    I -- yes --> J["status: earned"]
    I -- no --> K["status: partial_daily_cap<br/>or partial_budget"]
```

**Talking points**
- Money is **integer IDR only**; 5% uses integer math with **floor** rounding
  (conservative for the budget), and the API rejects fractional input outright.
- "Per day" = a calendar day in **Asia/Jakarta (WIB)** — a UTC boundary would
  reset the cap at 07:00 local. tzdata is embedded in the binary.

---

## 6. Concurrency model (why it can't overspend)

```mermaid
flowchart TB
    subgraph "Concurrent payments"
      T1["tx A"]
      T2["tx B"]
      T3["tx C"]
    end
    BR["campaign_budget row (id=1)<br/>SELECT ... FOR UPDATE"]
    T1 -->|holds lock| BR
    T2 -->|waits| BR
    T3 -->|waits| BR
    BR --> OUT["Serialized: each reads the true<br/>remaining, decrements, commits.<br/>CHECK(remaining ≥ 0) = hard backstop"]
```

**Talking points**
- Every award serializes on the single budget row, so concurrent awards **cannot**
  jointly overspend. The same pattern (row lock) protects the per-user daily cap.
- This correctness guarantee **is** the throughput ceiling — it's the honest
  trade-off. At scale I'd shard the budget into N reservation buckets or use a
  reserve/commit pattern. Deliberately out of scope for the MVP, and called out.
- **This isn't asserted, it's proven:** integration tests fire 50–200 goroutines
  and check the daily cap is exactly 50,000, the budget lands exactly on its
  total (never negative), 100 concurrent redeems produce exactly 50 successes,
  and a payment sent 30× concurrently is awarded exactly once.

---

## 7. Failure modes & how the design holds up

| Scenario | What happens | Why it's safe |
|----------|--------------|---------------|
| Client retries a payment | Conflict on unique key → original result returned | No double award |
| Two payments race the daily cap | Serialize on locked daily row | Cap never exceeded |
| Many payments race the budget | Serialize on locked budget row | Budget never overspent |
| Concurrent redemptions | Guarded `WHERE balance >= amount` + row lock | Never negative |
| Process crashes mid-award | Uncommitted tx rolls back | No partial state |
| Redis is down | Rate limiter fails open | Money correctness unaffected |
| Bug tries to overspend | `CHECK (remaining >= 0)` / `CHECK (balance >= 0)` | DB refuses to persist it |

---

## 8. Deliberately out of scope (and why)

Refunds/clawback (ledger is built to support them later), authentication (identity
via `X-User-Id`, set by an auth gateway in prod), fraud/velocity checks, a sharded
budget counter, and an outbox for downstream events. Scope discipline is part of
the design — these are conscious cuts, documented, not gaps.
