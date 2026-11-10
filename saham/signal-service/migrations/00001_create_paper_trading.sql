-- +goose Up
-- Every signal a rule makes, followed as if it were bought: the forward test of the rules.
-- Prices are those of the signal day. Yahoo's prices are adjusted when a dividend is paid,
-- so the job rescales these by the signal day's adjusted close before comparing.
CREATE TABLE paper_trades (
    id           BIGSERIAL PRIMARY KEY,
    rule         TEXT NOT NULL,
    ticker       TEXT NOT NULL,
    signal_date  DATE NOT NULL,
    signal_close DOUBLE PRECISION NOT NULL,
    max_entry    DOUBLE PRECISION NOT NULL,
    stop         DOUBLE PRECISION NOT NULL,
    target       DOUBLE PRECISION NOT NULL,
    exit_rule    TEXT NOT NULL,
    trail_atr    DOUBLE PRECISION NOT NULL,
    max_days     INT NOT NULL,
    reasons      TEXT[] NOT NULL,
    -- Whether IHSG closed above its MA200 that day, to compare healthy and weak markets.
    market_ok    BOOLEAN NOT NULL,
    status       TEXT NOT NULL,
    entry_date   DATE,
    entry_price  DOUBLE PRECISION,
    exit_date    DATE,
    exit_price   DOUBLE PRECISION,
    days         INT NOT NULL DEFAULT 0,
    return_pct   DOUBLE PRECISION,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (rule, ticker, signal_date)
);
CREATE INDEX paper_trades_unfinished ON paper_trades (rule, ticker) WHERE status IN ('waiting', 'open');

-- The momentum rule's five stocks, picked after each month's last close. When IHSG is below
-- its MA200 the rule holds cash instead. return_pct is the month's result after costs, set
-- once the next month's picks are made.
CREATE TABLE momentum_months (
    month_end  DATE PRIMARY KEY,
    picks      TEXT[] NOT NULL,
    market_ok  BOOLEAN NOT NULL,
    return_pct DOUBLE PRECISION
);

-- One row per trading day the job finished, with the message it sent.
CREATE TABLE runs (
    trading_day DATE PRIMARY KEY,
    finished_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    brief       TEXT NOT NULL
);

-- +goose Down
DROP TABLE runs;
DROP TABLE momentum_months;
DROP TABLE paper_trades;
