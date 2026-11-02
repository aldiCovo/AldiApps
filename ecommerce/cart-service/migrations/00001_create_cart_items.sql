-- +goose Up
-- Only ids and quantities. Names, prices and stock belong to the catalog, and can change.
CREATE TABLE cart_items (
    user_id    BIGINT NOT NULL,
    variant_id BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    quantity   INTEGER NOT NULL CHECK (quantity BETWEEN 1 AND 20),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, variant_id)
);

-- +goose Down
DROP TABLE cart_items;
