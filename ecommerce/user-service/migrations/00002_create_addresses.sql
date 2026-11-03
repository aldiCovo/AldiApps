-- +goose Up
-- One shipping address per account, filled in at checkout and offered again next time.
-- Orders keep their own copy, so changing this doesn't change where an old order went.
CREATE TABLE addresses (
    user_id     BIGINT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    recipient   TEXT NOT NULL,
    phone       TEXT NOT NULL,
    street      TEXT NOT NULL,
    district    TEXT NOT NULL,
    city        TEXT NOT NULL,
    province    TEXT NOT NULL,
    postal_code TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE addresses;
