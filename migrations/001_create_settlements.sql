-- 001_create_settlements.sql
--
-- Schema for the FX settlement service.
--
-- Monetary values are stored as NUMERIC (arbitrary precision), never as
-- floating point — the database mirrors the decimal.Decimal discipline used
-- in the Go code. Rates use higher scale than amounts because rate precision
-- compounds across large notional values.

CREATE TABLE IF NOT EXISTS settlements (
    id              UUID         PRIMARY KEY,
    partner_id      TEXT         NOT NULL,
    user_id         TEXT         NOT NULL,

    source_amount   NUMERIC(20, 8) NOT NULL,
    source_currency CHAR(3)        NOT NULL,
    target_amount   NUMERIC(20, 8) NOT NULL,
    target_currency CHAR(3)        NOT NULL,

    applied_rate    NUMERIC(20, 10) NOT NULL,
    mid_market_rate NUMERIC(20, 10) NOT NULL,
    spread_cost     NUMERIC(20, 8)  NOT NULL,

    status          TEXT         NOT NULL
                        CHECK (status IN ('PENDING', 'PROCESSING', 'SETTLED', 'FAILED')),
    failure_reason  TEXT         NOT NULL DEFAULT '',

    created_at      TIMESTAMPTZ  NOT NULL,
    updated_at      TIMESTAMPTZ  NOT NULL,
    settled_at      TIMESTAMPTZ
);

-- Partners list their own settlements, newest first — back this query with an index.
CREATE INDEX IF NOT EXISTS idx_settlements_partner_created
    ON settlements (partner_id, created_at DESC);

-- Operational queries often filter by status (e.g. "show stuck PROCESSING rows").
CREATE INDEX IF NOT EXISTS idx_settlements_status
    ON settlements (status);
