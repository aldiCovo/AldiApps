-- +goose Up
-- Each wrong password for an email, kept for a short while, so guessing an account's password
-- stops after a few tries. Keyed by the email, not the account, so an email without an account
-- answers the same.
CREATE TABLE login_failures (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX login_failures_email ON login_failures (email, created_at);
CREATE INDEX login_failures_created ON login_failures (created_at);

-- +goose Down
DROP TABLE login_failures;
