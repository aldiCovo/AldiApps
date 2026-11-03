-- +goose Up
-- When payment succeeded, for the receipt. Kept apart from updated_at, which later statuses
-- such as shipping will move. Orders paid before this column existed take their last change,
-- which was the payment.
ALTER TABLE orders ADD COLUMN paid_at TIMESTAMPTZ;
UPDATE orders SET paid_at = updated_at WHERE status = 'paid';

-- +goose Down
ALTER TABLE orders DROP COLUMN paid_at;
