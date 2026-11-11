-- +goose Up
-- Products a customer saved to look at again. Only ids: names, prices and photos belong to
-- the catalog. A product that's no longer sold drops out when the shop finds it gone.
CREATE TABLE favorites (
    user_id    BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, product_id)
);

-- +goose Down
DROP TABLE favorites;
