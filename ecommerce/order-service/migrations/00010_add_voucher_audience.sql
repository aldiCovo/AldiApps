-- +goose Up
-- Who sees a voucher on their account page. hidden: nobody, the customer must know the code.
-- all: every customer who can still use it. user: only that customer, who is also the only
-- one who can use it.
ALTER TABLE vouchers
    ADD COLUMN audience TEXT NOT NULL DEFAULT 'hidden' CHECK (audience IN ('hidden', 'all', 'user')),
    ADD COLUMN user_id BIGINT,
    ADD CONSTRAINT vouchers_audience_user CHECK ((audience = 'user') = (user_id IS NOT NULL)),
    ADD CONSTRAINT vouchers_banner_not_personal CHECK (NOT (banner AND audience = 'user'));
CREATE INDEX vouchers_user_id ON vouchers (user_id) WHERE user_id IS NOT NULL;

-- A banner voucher is already shown to everyone, so customers see it on their page too.
UPDATE vouchers SET audience = 'all' WHERE banner;

-- +goose Down
DROP INDEX vouchers_user_id;
ALTER TABLE vouchers DROP CONSTRAINT vouchers_banner_not_personal, DROP CONSTRAINT vouchers_audience_user,
    DROP COLUMN user_id, DROP COLUMN audience;
