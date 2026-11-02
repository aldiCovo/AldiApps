package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeUsers struct {
	err error
}

func (f fakeUsers) Find(context.Context, int64) (User, error) {
	return User{Email: "sari@example.com", Name: "Sari"}, f.err
}

type fakeMailer struct {
	sent []Email
	err  error
}

func (f *fakeMailer) Send(email Email) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, email)
	return nil
}

func newTestNotifier(t *testing.T) (*Notifier, *fakeMailer) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE sent_notifications"); err != nil {
		t.Fatal(err)
	}
	sender := &fakeMailer{}
	return &Notifier{pool: pool, users: fakeUsers{}, mailer: sender}, sender
}

func event(kind string, orderID int64, reason string) []byte {
	return fmt.Appendf(nil, `{"id":1,"type":%q,"data":{"order_id":%d,"user_id":7,"reason":%q,"total":1250000,"items":[{"product_name":"Dress Batik","size":"M","color":"Merah","quantity":2}]}}`, kind, orderID, reason)
}

func TestARepeatedEventSendsOneEmail(t *testing.T) {
	n, sender := newTestNotifier(t)
	ctx := context.Background()
	for range 3 {
		if err := n.handle(ctx, event(eventOrderPaid, 1, "")); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d emails", len(sender.sent))
	}
	got := sender.sent[0]
	if got.To.Email != "sari@example.com" || !strings.Contains(got.Subject, "#1") {
		t.Errorf("unexpected email %+v", got)
	}
	for _, want := range []string{"Halo Sari", "Dress Batik, M, Merah x 2", "Rp 1.250.000", "simulasi"} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("the body misses %q:\n%s", want, got.Body)
		}
	}
	// Another order is another email.
	if err := n.handle(ctx, event(eventOrderPaid, 2, "")); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 2 {
		t.Errorf("sent %d emails", len(sender.sent))
	}
}

func TestAFailureIsRetriedAndNotRecorded(t *testing.T) {
	n, sender := newTestNotifier(t)
	ctx := context.Background()
	sender.err = errors.New("the mail server is down")
	if err := n.handle(ctx, event(eventOrderPaid, 1, "")); err == nil {
		t.Fatal("want an error, so the event returns to the queue")
	}
	sender.err = nil
	if err := n.handle(ctx, event(eventOrderPaid, 1, "")); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Errorf("sent %d emails", len(sender.sent))
	}

	n.users = fakeUsers{err: errors.New("user-service is down")}
	if err := n.handle(ctx, event(eventOrderPaid, 2, "")); err == nil {
		t.Error("want an error while user-service is down")
	}
}

func TestPermanentProblemsAreDropped(t *testing.T) {
	n, sender := newTestNotifier(t)
	ctx := context.Background()
	for name, body := range map[string][]byte{
		"not JSON":   []byte("nope"),
		"bad data":   []byte(`{"type":"order.paid","data":"nope"}`),
		"no user id": []byte(`{"type":"order.paid","data":{"order_id":1}}`),
		"other type": []byte(`{"type":"order.created","data":{"order_id":1,"user_id":7}}`),
	} {
		if err := n.handle(ctx, body); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	n.users = fakeUsers{err: errUserGone}
	if err := n.handle(ctx, event(eventOrderPaid, 1, "")); err != nil {
		t.Errorf("deleted user: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Errorf("sent %d emails", len(sender.sent))
	}
	n.users = fakeUsers{}
	sender.err = &textproto.Error{Code: 550, Msg: "no such mailbox"}
	if err := n.handle(ctx, event(eventOrderPaid, 2, "")); err != nil {
		t.Errorf("refused address: %v", err)
	}
	sender.err = &textproto.Error{Code: 451, Msg: "try later"}
	if err := n.handle(ctx, event(eventOrderPaid, 3, "")); err == nil {
		t.Error("a temporary refusal must be retried")
	}
}

func TestEachOutcomeHasItsOwnWords(t *testing.T) {
	order := FinishedOrder{OrderID: 5, Total: 1000}
	user := User{Email: "a@example.com", Name: "A"}
	for _, test := range []struct {
		kind, reason, subject, opening string
	}{
		{eventOrderPaid, "", "sudah dibayar", "sudah kami terima"},
		{eventOrderRejected, "out_of_stock", "tidak bisa diproses", "stok barang"},
		{eventOrderCancelled, "payment_failed", "dibatalkan", "Pembayaran pesanan kamu gagal"},
		{eventOrderCancelled, "expired", "dibatalkan", "waktu pembayaran habis"},
	} {
		order.Reason = test.reason
		got := compose(test.kind, order, user)
		if !strings.Contains(got.Subject, test.subject) || !strings.Contains(got.Body, test.opening) {
			t.Errorf("%s %s: %+v", test.kind, test.reason, got)
		}
	}
}

func TestRupiah(t *testing.T) {
	for amount, want := range map[int64]string{0: "Rp 0", 999: "Rp 999", 1000: "Rp 1.000", 1250000: "Rp 1.250.000"} {
		if got := rupiah(amount); got != want {
			t.Errorf("rupiah(%d) = %q, want %q", amount, got, want)
		}
	}
}

func TestNamesCannotAddHeaders(t *testing.T) {
	m, err := newSMTPMailer("mail:25", "Prastika <no-reply@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.render(Email{
		To:      User{Email: "sari@example.com", Name: "Sari\r\nBcc: evil@example.com"},
		OrderID: 5,
		Kind:    eventOrderPaid,
		Subject: "Pesanan #5",
		Body:    "Produk é\r\nBcc: evil@example.com\r\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if got := message.Header.Get("Bcc"); got != "" {
		t.Errorf("a name added a header: %q", got)
	}
	to, err := mail.ParseAddress(message.Header.Get("To"))
	if err != nil || to.Address != "sari@example.com" {
		t.Errorf("To = %v, %v", to, err)
	}
	if got := message.Header.Get("Message-ID"); got != "<order-5-order.paid@example.com>" {
		t.Errorf("Message-ID = %q", got)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Produk é") {
		t.Errorf("body = %q", body)
	}
}

func TestMailFromMustBeAnAddress(t *testing.T) {
	if _, err := newSMTPMailer("mail:25", "not an address"); err == nil {
		t.Error("want an error")
	}
}

func TestUserClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/internal/users/1":
			io.WriteString(w, `{"id":1,"email":"sari@example.com","name":"Sari"}`)
		case "/internal/users/2":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	user, err := newUserClient(server.URL, "secret").Find(ctx, 1)
	if err != nil || user.Email != "sari@example.com" || user.Name != "Sari" {
		t.Errorf("got %+v, %v", user, err)
	}
	if _, err := newUserClient(server.URL, "secret").Find(ctx, 2); !errors.Is(err, errUserGone) {
		t.Errorf("want errUserGone, got %v", err)
	}
	if _, err := newUserClient(server.URL, "secret").Find(ctx, 3); err == nil || errors.Is(err, errUserGone) {
		t.Errorf("a server error must be retried, got %v", err)
	}
	if _, err := newUserClient(server.URL, "wrong").Find(ctx, 1); err == nil {
		t.Error("a wrong token must be an error")
	}
}
