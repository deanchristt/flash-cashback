-- Flash Cashback schema.
--
-- Money is stored as BIGINT IDR minor units (rupiah). Postgres is the single
-- source of truth for every money decision; Redis is only a cache / rate
-- limiter and is never authoritative.
--
-- The design is an append-only ledger (cashback_ledger) plus materialized
-- counters (user_balance, user_daily_cashback, campaign_budget) that are kept
-- consistent with the ledger inside the same transaction. The ledger makes the
-- system auditable and reconstructable; the counters make reads and the hot
-- concurrency checks cheap.

-- Raw payments. Cashback is derived from these. Idempotency is enforced per
-- (user_id, idempotency_key): a retried payment can never award cashback twice.
CREATE TABLE IF NOT EXISTS payments (
    id               BIGSERIAL PRIMARY KEY,
    user_id          TEXT      NOT NULL,
    amount           BIGINT    NOT NULL CHECK (amount > 0),
    idempotency_key  TEXT      NOT NULL,
    cashback_awarded BIGINT    NOT NULL DEFAULT 0 CHECK (cashback_awarded >= 0),
    status           TEXT      NOT NULL DEFAULT 'pending',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_payments_user_created ON payments (user_id, created_at DESC);

-- Single-row global campaign budget. The remaining column is decremented under
-- a row lock during every award; CHECK (remaining >= 0) is a hard backstop that
-- makes overspend impossible even if application logic is wrong.
CREATE TABLE IF NOT EXISTS campaign_budget (
    id           INT PRIMARY KEY DEFAULT 1,
    total_budget BIGINT NOT NULL CHECK (total_budget >= 0),
    remaining    BIGINT NOT NULL CHECK (remaining >= 0),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT campaign_budget_singleton CHECK (id = 1)
);

-- Per-user, per-day cashback tally (day = Asia/Jakarta calendar date). Locked
-- with SELECT ... FOR UPDATE during an award so concurrent payments for the
-- same user can never jointly exceed the daily cap.
CREATE TABLE IF NOT EXISTS user_daily_cashback (
    user_id TEXT   NOT NULL,
    day     DATE   NOT NULL,
    earned  BIGINT NOT NULL DEFAULT 0 CHECK (earned >= 0),
    PRIMARY KEY (user_id, day)
);

-- Materialized redeemable balance. CHECK (balance >= 0) makes a negative
-- balance impossible at the storage layer.
CREATE TABLE IF NOT EXISTS user_balance (
    user_id    TEXT   PRIMARY KEY,
    balance    BIGINT NOT NULL DEFAULT 0 CHECK (balance >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only double-entry-style ledger. Every movement of cashback (EARN or
-- REDEEM) is one immutable row. balance_after snapshots the running balance for
-- audit. UNIQUE (source_type, source_id) is a second idempotency guard: a given
-- payment or redemption can produce at most one ledger entry.
CREATE TABLE IF NOT EXISTS cashback_ledger (
    id            BIGSERIAL PRIMARY KEY,
    user_id       TEXT      NOT NULL,
    entry_type    TEXT      NOT NULL CHECK (entry_type IN ('EARN', 'REDEEM')),
    amount        BIGINT    NOT NULL CHECK (amount > 0),
    balance_after BIGINT    NOT NULL CHECK (balance_after >= 0),
    source_type   TEXT      NOT NULL,
    source_id     TEXT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_type, source_id)
);
CREATE INDEX IF NOT EXISTS idx_ledger_user_created ON cashback_ledger (user_id, created_at DESC);

-- Redemptions. Idempotent per (user_id, idempotency_key). A failed redemption
-- (insufficient balance) rolls back and leaves no row, so the same key may be
-- retried and succeed later.
CREATE TABLE IF NOT EXISTS redemptions (
    id              BIGSERIAL PRIMARY KEY,
    user_id         TEXT      NOT NULL,
    amount          BIGINT    NOT NULL CHECK (amount > 0),
    idempotency_key TEXT      NOT NULL,
    balance_after   BIGINT    NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);

-- Seed the campaign budget once. ON CONFLICT DO NOTHING makes this idempotent;
-- the 10,000,000 IDR total is the campaign's fixed budget.
INSERT INTO campaign_budget (id, total_budget, remaining)
VALUES (1, 10000000, 10000000)
ON CONFLICT (id) DO NOTHING;
