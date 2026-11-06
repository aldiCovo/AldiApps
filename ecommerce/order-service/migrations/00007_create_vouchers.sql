-- +goose Up
-- Codes customers type at checkout. A voucher takes a percentage or an amount off the items,
-- or pays some or all of the shipping. Each order keeps a copy of the code and the discount,
-- so changing a voucher later doesn't change orders already made with it.
CREATE TABLE vouchers (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code           TEXT NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9]{3,20}$'),
    description    TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL CHECK (kind IN ('percent', 'amount', 'free_shipping')),
    -- Percent: 1 to 100. Amount: rupiah. Free shipping: 0.
    value          BIGINT NOT NULL CHECK (value >= 0),
    -- The most a percent or free-shipping voucher takes off. NULL takes it all.
    max_discount   BIGINT CHECK (max_discount > 0),
    min_subtotal   BIGINT NOT NULL DEFAULT 0 CHECK (min_subtotal >= 0),
    -- How many orders may hold it at once, counting only orders that didn't fail or get
    -- cancelled. NULL has no limit.
    quota          INTEGER CHECK (quota > 0),
    per_user_limit INTEGER NOT NULL DEFAULT 1 CHECK (per_user_limit > 0),
    starts_at      TIMESTAMPTZ NOT NULL,
    ends_at        TIMESTAMPTZ NOT NULL,
    active         BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);

-- A voucher used by an order can't be deleted, only turned off.
ALTER TABLE orders
    ADD COLUMN voucher_id BIGINT REFERENCES vouchers (id),
    ADD COLUMN voucher_code TEXT NOT NULL DEFAULT '',
    ADD COLUMN discount BIGINT NOT NULL DEFAULT 0 CHECK (discount >= 0);
CREATE INDEX orders_voucher_id ON orders (voucher_id, user_id) WHERE voucher_id IS NOT NULL;

-- +goose Down
DROP INDEX orders_voucher_id;
ALTER TABLE orders DROP COLUMN voucher_id, DROP COLUMN voucher_code, DROP COLUMN discount;
DROP TABLE vouchers;
