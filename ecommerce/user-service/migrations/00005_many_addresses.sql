-- +goose Up
-- An account keeps up to ten addresses, each with a label such as Rumah or Kantor. One is the
-- main address, chosen first at checkout. Orders still keep their own copy.
CREATE TABLE user_addresses (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    recipient   TEXT NOT NULL,
    phone       TEXT NOT NULL,
    street      TEXT NOT NULL,
    district    TEXT NOT NULL,
    city        TEXT NOT NULL,
    province    TEXT NOT NULL,
    postal_code TEXT NOT NULL,
    main        BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX user_addresses_user_id ON user_addresses (user_id, created_at);
CREATE UNIQUE INDEX user_addresses_one_main ON user_addresses (user_id) WHERE main;

-- Each account's one address becomes its main one.
INSERT INTO user_addresses (user_id, label, recipient, phone, street, district, city, province, postal_code, main, created_at, updated_at)
SELECT user_id, 'Rumah', recipient, phone, street, district, city, province, postal_code, true, updated_at, updated_at
FROM addresses;

DROP TABLE addresses;

-- +goose Down
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
INSERT INTO addresses (user_id, recipient, phone, street, district, city, province, postal_code, updated_at)
SELECT user_id, recipient, phone, street, district, city, province, postal_code, updated_at
FROM user_addresses WHERE main;
DROP TABLE user_addresses;
