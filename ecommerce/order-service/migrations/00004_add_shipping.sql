-- +goose Up
-- The courier, service, estimate and price chosen at checkout. The order's total includes the
-- price. Orders made before shipping was charged have none.
ALTER TABLE orders ADD COLUMN shipping JSONB;

-- +goose Down
ALTER TABLE orders DROP COLUMN shipping;
