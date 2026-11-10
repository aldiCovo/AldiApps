-- +goose Up
-- Broker summaries pasted from Stockbit by hand: one per stock, period and filter. A paste for
-- the same stock, period and filter replaces the earlier one.
CREATE TABLE broker_summaries (
    id            BIGSERIAL PRIMARY KEY,
    ticker        TEXT NOT NULL,
    period_start  DATE NOT NULL,
    period_end    DATE NOT NULL,
    investor      TEXT NOT NULL,  -- all, foreign or domestic, as chosen in Stockbit
    market        TEXT NOT NULL,  -- regular, negotiated or cash
    mode          TEXT NOT NULL,  -- net or gross
    -- Stockbit's own figures above the table. Rupiah and lots as numbers.
    buyers        INT,
    sellers       INT,
    net_lots      DOUBLE PRECISION,
    net_value     DOUBLE PRECISION,
    avg_price     DOUBLE PRECISION,
    acc_dist      TEXT,           -- Stockbit's label, such as "Dist"
    top           JSONB NOT NULL DEFAULT '[]', -- Top 1, 3, 5 and Average rows
    pasted        TEXT NOT NULL,  -- the text as pasted, to read it again if the reader improves
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticker, period_start, period_end, investor, market, mode)
);
CREATE INDEX broker_summaries_ticker ON broker_summaries (ticker, period_end DESC);

-- Each broker's line: what it bought (side buy) or sold (side sell), in the order shown.
CREATE TABLE broker_summary_rows (
    summary_id BIGINT NOT NULL REFERENCES broker_summaries (id) ON DELETE CASCADE,
    side       TEXT NOT NULL,
    rank       INT NOT NULL,
    broker     TEXT NOT NULL,
    value      DOUBLE PRECISION NOT NULL,
    lots       DOUBLE PRECISION NOT NULL,
    avg_price  DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (summary_id, side, rank)
);

-- The brokers counted as foreign. Editable in the pages, since ownership changes and a foreign
-- broker also serves local clients.
CREATE TABLE foreign_brokers (
    code TEXT PRIMARY KEY
);
INSERT INTO foreign_brokers (code) VALUES
    ('AK'), ('BK'), ('CG'), ('CS'), ('DB'), ('DP'), ('GW'), ('KZ'), ('ML'), ('MS'), ('RX'), ('YU'), ('ZP');

-- +goose Down
DROP TABLE foreign_brokers;
DROP TABLE broker_summary_rows;
DROP TABLE broker_summaries;
