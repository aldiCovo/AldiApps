-- +goose Up
-- One row per email, so a repeated event doesn't send it again.
CREATE TABLE sent_notifications (
    order_id BIGINT NOT NULL,
    kind     TEXT NOT NULL,
    sent_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, kind)
);

-- +goose Down
DROP TABLE sent_notifications;
