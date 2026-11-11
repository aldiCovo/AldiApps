-- +goose Up
-- What a customer fills in on their account page. All optional: an account made before
-- these existed, or by someone who never filled them in, has none.
ALTER TABLE users
    ADD COLUMN phone      TEXT NOT NULL DEFAULT '',
    ADD COLUMN birth_date DATE,
    ADD COLUMN gender     TEXT NOT NULL DEFAULT '' CHECK (gender IN ('', 'female', 'male'));

-- A small square picture, made in the browser, so it never needs resizing here. Kept apart
-- from users so reading an account doesn't read the picture.
CREATE TABLE user_photos (
    user_id    BIGINT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    image      BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE user_photos;
ALTER TABLE users DROP COLUMN phone, DROP COLUMN birth_date, DROP COLUMN gender;
