-- +goose Up
-- A product can be sold for less over a period ("harga coret"). price stays the regular
-- price; customers pay sale_price from sale_starts_at until sale_ends_at. A sale that ended
-- stays until it is changed or removed, so the admin can see it.
ALTER TABLE products
    ADD COLUMN sale_price     BIGINT CHECK (sale_price > 0),
    ADD COLUMN sale_starts_at TIMESTAMPTZ,
    ADD COLUMN sale_ends_at   TIMESTAMPTZ,
    -- Whether the last product event was sent with the sale on. When a sale starts or ends
    -- the two differ, and the sale announcer sends the product again with its new price.
    ADD COLUMN sale_announced BOOLEAN NOT NULL DEFAULT false,
    ADD CONSTRAINT products_sale_complete CHECK (
        (sale_price IS NULL) = (sale_starts_at IS NULL) AND (sale_price IS NULL) = (sale_ends_at IS NULL)),
    ADD CONSTRAINT products_sale_period CHECK (sale_ends_at > sale_starts_at);

-- +goose Down
ALTER TABLE products
    DROP CONSTRAINT products_sale_period,
    DROP CONSTRAINT products_sale_complete,
    DROP COLUMN sale_announced,
    DROP COLUMN sale_ends_at,
    DROP COLUMN sale_starts_at,
    DROP COLUMN sale_price;
