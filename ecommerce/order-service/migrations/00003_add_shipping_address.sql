-- +goose Up
-- A copy of where the order goes, so changing the account's address doesn't move it.
-- Orders made before addresses existed have none.
ALTER TABLE orders ADD COLUMN shipping_address JSONB;

-- +goose Down
ALTER TABLE orders DROP COLUMN shipping_address;
