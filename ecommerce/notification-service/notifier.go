package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	eventOrderPaid      = "order.paid"
	eventOrderRejected  = "order.rejected"
	eventOrderCancelled = "order.cancelled"

	reasonExpired = "expired"
)

// Event is the message body.
type Event struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// FinishedOrder says how an order ended.
type FinishedOrder struct {
	OrderID int64  `json:"order_id"`
	UserID  int64  `json:"user_id"`
	Reason  string `json:"reason"`
	Total   int64  `json:"total"`
	Items   []struct {
		ProductName string `json:"product_name"`
		Size        string `json:"size"`
		Color       string `json:"color"`
		Quantity    int32  `json:"quantity"`
	} `json:"items"`
}

// Notifier turns finished orders into emails.
type Notifier struct {
	pool   *pgxpool.Pool
	users  userFinder
	mailer mailer
}

// handle only returns errors that retrying can fix, and everything else is logged and dropped,
// so one bad message can't block the emails behind it.
//
// The email goes out before its row is saved. A crash between the two sends it twice, because
// a customer is better off with a repeat than with no email about their money.
func (n *Notifier) handle(ctx context.Context, body []byte) error {
	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("dropping unreadable event", "error", err)
		return nil
	}
	switch event.Type {
	case eventOrderPaid, eventOrderRejected, eventOrderCancelled:
	default:
		slog.Warn("ignoring unknown event type", "event_id", event.ID, "type", event.Type)
		return nil
	}
	var order FinishedOrder
	if err := json.Unmarshal(event.Data, &order); err != nil || order.OrderID <= 0 || order.UserID <= 0 {
		slog.Error("dropping unreadable event", "event_id", event.ID, "type", event.Type, "error", err)
		return nil
	}

	var sent bool
	if err := n.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM sent_notifications WHERE order_id = $1 AND kind = $2)`,
		order.OrderID, event.Type).Scan(&sent); err != nil {
		return err
	}
	if sent {
		return nil
	}

	user, err := n.users.Find(ctx, order.UserID)
	if errors.Is(err, errUserGone) {
		slog.Warn("skipping an email for a deleted user", "order_id", order.OrderID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("finding the user: %w", err)
	}
	err = n.mailer.Send(compose(event.Type, order, user))
	if isPermanent(err) {
		slog.Error("the mail server refused an email", "order_id", order.OrderID, "kind", event.Type, "error", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("sending the email: %w", err)
	}
	if _, err := n.pool.Exec(ctx,
		`INSERT INTO sent_notifications (order_id, kind) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		order.OrderID, event.Type); err != nil {
		return err
	}
	slog.Info("sent an email", "order_id", order.OrderID, "kind", event.Type)
	return nil
}

func compose(kind string, order FinishedOrder, user User) Email {
	subject, opening := "", ""
	switch {
	case kind == eventOrderPaid:
		subject = "Pesanan #%d sudah dibayar"
		opening = "Terima kasih, pembayaran pesanan kamu sudah kami terima."
	case kind == eventOrderRejected:
		subject = "Pesanan #%d tidak bisa diproses"
		opening = "Maaf, stok barang yang kamu pesan tidak cukup. Tidak ada pembayaran yang ditagih."
	case order.Reason == reasonExpired:
		subject = "Pesanan #%d dibatalkan"
		opening = "Pesanan kamu dibatalkan karena waktu pembayaran habis, dan stoknya sudah dikembalikan."
	default:
		subject = "Pesanan #%d dibatalkan"
		opening = "Pembayaran pesanan kamu gagal, jadi pesanan dibatalkan dan stoknya sudah dikembalikan."
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Halo %s,\r\n\r\n%s\r\n\r\nPesanan #%d\r\n", user.Name, opening, order.OrderID)
	for _, item := range order.Items {
		fmt.Fprintf(&body, "- %s, %s, %s x %d\r\n", item.ProductName, item.Size, item.Color, item.Quantity)
	}
	fmt.Fprintf(&body, "Total: %s\r\n\r\n", rupiah(order.Total))
	body.WriteString("Prastika masih toko latihan. Pembayarannya simulasi, tidak ada uang yang ditarik dan tidak ada barang yang dikirim.\r\n")
	return Email{
		To:      user,
		OrderID: order.OrderID,
		Kind:    kind,
		Subject: fmt.Sprintf(subject, order.OrderID),
		Body:    body.String(),
	}
}

func encodeSubject(subject string) string {
	return mime.QEncoding.Encode("utf-8", subject)
}

// rupiah writes 1250000 as Rp 1.250.000.
func rupiah(amount int64) string {
	digits := strconv.FormatInt(amount, 10)
	var out strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte('.')
		}
		out.WriteRune(digit)
	}
	return "Rp " + out.String()
}
