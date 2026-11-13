-- +goose Up
-- A voucher the shop offers in its sign-up popup to visitors without an account. Like the
-- banner, the shop shows only one: the most recently changed that can be used now.
ALTER TABLE vouchers
    ADD COLUMN signup BOOLEAN NOT NULL DEFAULT false,
    ADD CONSTRAINT vouchers_signup_not_personal CHECK (NOT (signup AND audience = 'user'));

-- +goose Down
ALTER TABLE vouchers DROP CONSTRAINT vouchers_signup_not_personal, DROP COLUMN signup;
