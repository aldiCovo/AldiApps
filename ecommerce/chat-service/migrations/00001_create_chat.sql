-- +goose Up
-- One conversation per customer, keyed by their user-service id.
CREATE TABLE conversations (
    user_id          BIGINT PRIMARY KEY,
    last_message_id  BIGINT NOT NULL DEFAULT 0,
    -- Whether the newest message came from the shop, so the admin list knows who waits.
    last_from_admin  BOOLEAN NOT NULL DEFAULT false,
    last_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- The newest message each side has seen.
    customer_read_id BIGINT NOT NULL DEFAULT 0,
    admin_read_id    BIGINT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX conversations_last_at ON conversations (last_at DESC);

CREATE TABLE messages (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES conversations (user_id) ON DELETE CASCADE,
    -- The admin who wrote it, or NULL when the customer did.
    admin_id   BIGINT,
    body       TEXT NOT NULL CHECK (char_length(body) BETWEEN 1 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX messages_user_id ON messages (user_id, id);

-- +goose Down
DROP TABLE messages;
DROP TABLE conversations;
