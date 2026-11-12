-- +goose Up
-- When the owner proved they read the email, and the WhatsApp of the phone, with a code.
-- Changing the phone clears its time. google_sub is the Google account that can log in here.
ALTER TABLE users
    ADD COLUMN email_verified_at TIMESTAMPTZ,
    ADD COLUMN phone_verified_at TIMESTAMPTZ,
    ADD COLUMN google_sub        TEXT UNIQUE;

-- Accounts from before codes existed already get reset links at their email, so it counts as theirs.
UPDATE users SET email_verified_at = created_at;

-- An account made with Google has an empty password_hash until a reset link sets one. No
-- password matches it.

-- Codes sent to prove an email or a phone. Only a hash is kept, and a code is tried at most a
-- few times, so its 6 digits can't be guessed.
CREATE TABLE verification_codes (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel    TEXT NOT NULL CHECK (channel IN ('email', 'whatsapp')),
    -- The email or phone it went to, so a code stops working when that changes.
    target     TEXT NOT NULL,
    code_hash  BYTEA NOT NULL,
    attempts   INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE INDEX verification_codes_user ON verification_codes (user_id, channel, created_at);

-- +goose Down
DROP TABLE verification_codes;
ALTER TABLE users DROP COLUMN email_verified_at, DROP COLUMN phone_verified_at, DROP COLUMN google_sub;
