-- +goose Up
-- Customers get an email when an admin's answer stays unread. These say which answers an
-- email already covered and when the last one went, for at most one an hour.
ALTER TABLE conversations
    ADD COLUMN emailed_up_to BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN emailed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE conversations DROP COLUMN emailed_up_to, DROP COLUMN emailed_at;
