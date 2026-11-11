-- +goose Up
-- A Flash Sale is a sale with a quota: only sale_quota items sell at the sale price. Each
-- reserved order counts its items in sale_sold, and a cancelled one gives them back. Once
-- sale_sold reaches the quota the sale stops, and the regular price applies again.
ALTER TABLE products
    ADD COLUMN sale_flash BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN sale_quota INTEGER CHECK (sale_quota > 0),
    ADD COLUMN sale_sold  INTEGER NOT NULL DEFAULT 0 CHECK (sale_sold >= 0),
    ADD CONSTRAINT products_flash_quota CHECK (sale_flash = (sale_quota IS NOT NULL));

-- +goose Down
ALTER TABLE products
    DROP CONSTRAINT products_flash_quota,
    DROP COLUMN sale_sold,
    DROP COLUMN sale_quota,
    DROP COLUMN sale_flash;
