-- +goose Up
-- How the shop's sign-up popup behaves, chosen by the admins. One row.
CREATE TABLE signup_popup (
    id         INT PRIMARY KEY CHECK (id = 1),
    -- After a visitor closes the popup, the same browser doesn't see it again for this many
    -- hours: from 2 hours to 365 days. A week at first.
    rest_hours INT NOT NULL DEFAULT 168 CHECK (rest_hours BETWEEN 2 AND 8760),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO signup_popup (id) VALUES (1);

-- +goose Down
DROP TABLE signup_popup;
