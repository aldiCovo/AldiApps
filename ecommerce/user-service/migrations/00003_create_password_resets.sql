-- +goose Up
-- Links for setting a new password. Only a hash of each link's token is kept, so someone who
-- reads this table still can't use a link.
CREATE TABLE password_resets (
    token_hash BYTEA PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE INDEX password_resets_user_id ON password_resets (user_id, created_at);

-- +goose Down
DROP TABLE password_resets;
