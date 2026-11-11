-- +goose Up
-- A shipped order is marked as arrived (by an admin now, later from the courier's tracking),
-- then the customer confirms it, or it completes on its own after a few days.
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (status IN
    ('pending', 'awaiting_payment', 'paying', 'paid', 'packed', 'shipped', 'arrived', 'completed', 'rejected', 'cancelled'));
ALTER TABLE orders
    ADD COLUMN arrived_at TIMESTAMPTZ,
    -- admin or courier.
    ADD COLUMN arrived_by TEXT NOT NULL DEFAULT '',
    -- admin, customer or system. Empty on orders completed before this existed.
    ADD COLUMN completed_by TEXT NOT NULL DEFAULT '';

-- One review per product per completed order, by the customer who bought it.
CREATE TABLE reviews (
    order_id BIGINT NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    rating SMALLINT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    body TEXT NOT NULL DEFAULT '',
    -- The buyer's name, already masked, and the size and colour they bought.
    author TEXT NOT NULL,
    variant TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, product_id)
);
CREATE INDEX reviews_product_created ON reviews (product_id, created_at DESC);

-- +goose Down
DROP TABLE reviews;
UPDATE orders SET status = 'shipped' WHERE status = 'arrived';
ALTER TABLE orders DROP COLUMN arrived_at, DROP COLUMN arrived_by, DROP COLUMN completed_by;
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (status IN
    ('pending', 'awaiting_payment', 'paying', 'paid', 'packed', 'shipped', 'completed', 'rejected', 'cancelled'));
