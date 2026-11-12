-- +goose Up
-- Answers the admins send often, shared by every admin. {nama} becomes the customer's first name.
CREATE TABLE quick_replies (
    id         BIGSERIAL PRIMARY KEY,
    body       TEXT NOT NULL CHECK (char_length(body) BETWEEN 1 AND 500),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO quick_replies (body) VALUES
    ('Halo Kak {nama}, terima kasih sudah menghubungi Prastika 🙏 Ada yang bisa kami bantu?'),
    ('Terima kasih sudah menunggu, Kak 🙏'),
    ('Pesanan Kakak sedang kami kemas dan segera dikirim ya.'),
    ('Mohon maaf Kak, stok untuk ukuran/warna itu sudah habis.');

-- +goose Down
DROP TABLE quick_replies;
