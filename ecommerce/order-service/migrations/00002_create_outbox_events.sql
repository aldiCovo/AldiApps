-- +goose Up
-- Events wait here until RabbitMQ has confirmed them, so a change and its event commit together.
CREATE TABLE outbox_events (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type       TEXT NOT NULL,
    data       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE outbox_events;
