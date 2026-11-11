-- +goose Up
-- A voucher the shop shows in the bar above its header, so customers see the code without
-- having to know it. The shop shows only one: the most recently changed that can be used now.
ALTER TABLE vouchers ADD COLUMN banner BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE vouchers DROP COLUMN banner;
