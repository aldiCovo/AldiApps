-- +goose Up
-- One payment per order, so a repeated event can't charge twice.
CREATE TABLE payments (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id   BIGINT NOT NULL UNIQUE,
    amount     BIGINT NOT NULL CHECK (amount >= 0),
    status     TEXT NOT NULL CHECK (status IN ('succeeded', 'failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE payments;
