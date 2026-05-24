-- migrations/001_init.sql
-- Read-projection schema for the ledger query service.
-- All tables are materialised by the Kafka consumer — never written by HTTP.
-- Every write uses ON CONFLICT (upsert) so replaying events is always safe.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ── balance_views ─────────────────────────────────────────────────────────────
-- One row per account. Updated on every transaction.posted event.
-- Seeded with balance=0 on account.created.

CREATE TABLE IF NOT EXISTS balance_views (
    account_id  UUID        PRIMARY KEY,
    owner_id    UUID        NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    currency    CHAR(3)     NOT NULL,
    balance     NUMERIC(20,8) NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── transaction_views ─────────────────────────────────────────────────────────
-- One row per transaction. Status can be updated (POSTED → REVERSED).

CREATE TABLE IF NOT EXISTS transaction_views (
    id                 UUID        PRIMARY KEY,
    idempotency_key    TEXT        NOT NULL DEFAULT '',
    debit_account_id   UUID        NOT NULL,
    credit_account_id  UUID        NOT NULL,
    amount             NUMERIC(20,8) NOT NULL,
    currency           CHAR(3)     NOT NULL,
    description        TEXT,
    status             TEXT        NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Queries: "show me all transactions for account X, newest first"
CREATE INDEX IF NOT EXISTS idx_tv_debit_created
    ON transaction_views (debit_account_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_tv_credit_created
    ON transaction_views (credit_account_id, created_at DESC);

-- ── entry_views ───────────────────────────────────────────────────────────────
-- One row per ledger entry (two per transaction: DEBIT + CREDIT).
-- Immutable — upserts use ON CONFLICT DO NOTHING.

CREATE TABLE IF NOT EXISTS entry_views (
    id              UUID        PRIMARY KEY,
    account_id      UUID        NOT NULL,
    transaction_id  UUID        NOT NULL,
    type            TEXT        NOT NULL,
    amount          NUMERIC(20,8) NOT NULL,
    balance_before  NUMERIC(20,8) NOT NULL,
    balance_after   NUMERIC(20,8) NOT NULL,
    currency        CHAR(3)     NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL
);

-- Query: "show ledger history for account X, newest first"
CREATE INDEX IF NOT EXISTS idx_ev_account_created
    ON entry_views (account_id, created_at DESC);

-- Lookup by transaction (useful for audit)
CREATE INDEX IF NOT EXISTS idx_ev_transaction
    ON entry_views (transaction_id);
