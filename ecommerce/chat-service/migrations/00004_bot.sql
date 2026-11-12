-- +goose Up
-- The shop's AI assistant answers first. Its messages have admin_id 0, so everything that
-- treats "admin_id IS NOT NULL" as the shop's side counts them as the shop's.
ALTER TABLE conversations
    -- A person must answer: the bot handed over, the customer asked, or the bot is off.
    ADD COLUMN needs_admin BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN needs_reason TEXT NOT NULL DEFAULT '',
    -- After an admin answers, the bot stays quiet in this conversation until then.
    ADD COLUMN bot_off_until TIMESTAMPTZ,
    -- The bot is writing an answer now, for "sedang mengetik".
    ADD COLUMN bot_busy BOOLEAN NOT NULL DEFAULT false;

-- Conversations waiting before the bot existed still wait for an admin.
UPDATE conversations SET needs_admin = true WHERE last_message_id > 0 AND NOT last_from_admin;
CREATE INDEX conversations_needs_admin ON conversations (last_at DESC) WHERE needs_admin;

CREATE TABLE bot_settings (
    id                  INT PRIMARY KEY CHECK (id = 1),
    -- always: the bot answers every message first; outside_hours: only outside working
    -- hours; off: every message goes to the admins.
    mode                TEXT NOT NULL DEFAULT 'always' CHECK (mode IN ('always', 'outside_hours', 'off')),
    -- What the bot can't read from the shop's systems: returns, sizes, materials.
    store_info          TEXT NOT NULL DEFAULT '' CHECK (char_length(store_info) <= 4000),
    telegram            BOOLEAN NOT NULL DEFAULT true,
    quiet_outside_hours BOOLEAN NOT NULL DEFAULT true,
    -- Which AI answers what the menu doesn't: gemini (free, with a daily limit), claude (paid
    -- from API credit) or none (only the menu; everything else goes to the admins).
    ai                  TEXT NOT NULL DEFAULT 'gemini' CHECK (ai IN ('gemini', 'claude', 'none')),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO bot_settings (id) VALUES (1);

-- Each time a conversation started needing an admin, for the settings page's count.
CREATE TABLE handoffs (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES conversations (user_id) ON DELETE CASCADE,
    reason     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX handoffs_created_at ON handoffs (created_at);

-- How often each day the AI answered and the menu was used, in Jakarta's days.
CREATE TABLE bot_daily (
    day  DATE PRIMARY KEY,
    ai   INT NOT NULL DEFAULT 0,
    menu INT NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE bot_daily;
DROP TABLE handoffs;
DROP TABLE bot_settings;
DROP INDEX conversations_needs_admin;
DELETE FROM messages WHERE admin_id = 0;
ALTER TABLE conversations DROP COLUMN needs_admin, DROP COLUMN needs_reason, DROP COLUMN bot_off_until, DROP COLUMN bot_busy;
