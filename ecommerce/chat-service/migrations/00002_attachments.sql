-- +goose Up
-- A product or order card sent with a message, kept as it was when sent. A message with a
-- card may have no text.
ALTER TABLE messages ADD COLUMN attachment JSONB;
ALTER TABLE messages DROP CONSTRAINT messages_body_check;
ALTER TABLE messages ADD CONSTRAINT messages_body_check
    CHECK (char_length(body) <= 1000 AND (char_length(body) >= 1 OR attachment IS NOT NULL));

-- +goose Down
DELETE FROM messages WHERE body = '';
ALTER TABLE messages DROP CONSTRAINT messages_body_check;
ALTER TABLE messages ADD CONSTRAINT messages_body_check CHECK (char_length(body) BETWEEN 1 AND 1000);
ALTER TABLE messages DROP COLUMN attachment;
