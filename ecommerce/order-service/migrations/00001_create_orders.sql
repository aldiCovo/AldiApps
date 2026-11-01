-- +goose Up
CREATE TABLE orders (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('pending', 'awaiting_payment', 'paying', 'paid', 'rejected', 'cancelled')),
    reason     TEXT NOT NULL DEFAULT '',
    -- In whole rupiah, like the catalog's prices.
    total      BIGINT NOT NULL CHECK (total >= 0),
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX orders_user_id_id ON orders (user_id, id DESC);
CREATE INDEX orders_waiting_for_payment ON orders (expires_at) WHERE status = 'awaiting_payment';

-- Copies of the catalog's values, so an order keeps its price after the catalog changes.
CREATE TABLE order_items (
    order_id     BIGINT NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    variant_id   BIGINT NOT NULL,
    product_id   BIGINT NOT NULL,
    product_name TEXT NOT NULL,
    size         TEXT NOT NULL,
    color        TEXT NOT NULL,
    price        BIGINT NOT NULL CHECK (price >= 0),
    quantity     INTEGER NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (order_id, variant_id)
);

-- +goose Down
DROP TABLE order_items;
DROP TABLE orders;
