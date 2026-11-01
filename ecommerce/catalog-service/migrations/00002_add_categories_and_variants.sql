-- +goose Up
CREATE TABLE categories (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX categories_name_key ON categories (lower(name));

-- Optional, because the products that already exist have no category yet.
ALTER TABLE products ADD COLUMN category_id BIGINT REFERENCES categories (id);
CREATE INDEX products_category_id_idx ON products (category_id);

CREATE TABLE product_variants (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id BIGINT NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    size       TEXT NOT NULL,
    color      TEXT NOT NULL,
    stock      INTEGER NOT NULL DEFAULT 0 CHECK (stock >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX product_variants_key ON product_variants (product_id, lower(size), lower(color));

-- Stock now belongs to a variant, so each existing product keeps its stock in one variant.
INSERT INTO product_variants (product_id, size, color, stock)
SELECT id, 'All Size', 'Default', stock FROM products;

ALTER TABLE products DROP COLUMN stock;

-- +goose Down
ALTER TABLE products ADD COLUMN stock INTEGER NOT NULL DEFAULT 0 CHECK (stock >= 0);

UPDATE products p
SET stock = (SELECT COALESCE(sum(v.stock), 0) FROM product_variants v WHERE v.product_id = p.id);

DROP TABLE product_variants;
ALTER TABLE products DROP COLUMN category_id;
DROP TABLE categories;
