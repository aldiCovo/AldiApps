-- +goose Up
-- One row per order, so a repeated event can't take or return its stock twice.
CREATE TABLE stock_reservations (
    order_id   BIGINT PRIMARY KEY,
    status     TEXT NOT NULL CHECK (status IN ('reserved', 'rejected', 'released')),
    items      JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE stock_reservations;
