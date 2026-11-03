-- +goose Up
-- After payment the shop packs, ships and completes an order from the admin pages. Each step
-- keeps its time, and shipping keeps the courier's tracking number.
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (status IN
    ('pending', 'awaiting_payment', 'paying', 'paid', 'packed', 'shipped', 'completed', 'rejected', 'cancelled'));
ALTER TABLE orders
    ADD COLUMN tracking_number TEXT NOT NULL DEFAULT '',
    ADD COLUMN packed_at TIMESTAMPTZ,
    ADD COLUMN shipped_at TIMESTAMPTZ,
    ADD COLUMN completed_at TIMESTAMPTZ;
-- The admin pages list orders by status, newest first.
CREATE INDEX orders_status_id ON orders (status, id DESC);

-- +goose Down
DROP INDEX orders_status_id;
UPDATE orders SET status = 'paid' WHERE status IN ('packed', 'shipped', 'completed');
ALTER TABLE orders DROP COLUMN tracking_number, DROP COLUMN packed_at, DROP COLUMN shipped_at, DROP COLUMN completed_at;
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (status IN
    ('pending', 'awaiting_payment', 'paying', 'paid', 'rejected', 'cancelled'));
