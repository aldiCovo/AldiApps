package main

import (
	"net/mail"
	"strings"
	"testing"
)

func TestResetRepliesGoToTheShopMailbox(t *testing.T) {
	m, err := newSMTPMailer("mail:25", "Prastika Fashion <no-reply@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.withReplyTo("Prastika Fashion <toko@example.com>"); err != nil {
		t.Fatal(err)
	}
	raw, err := m.render(Email{To: User{Name: "Budi", Email: "budi@example.com"}, Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.Header.Get("Reply-To"); got != `"Prastika Fashion" <toko@example.com>` {
		t.Fatalf("Reply-To = %q", got)
	}
	if got := msg.Header.Get("From"); got != `"Prastika Fashion" <no-reply@example.com>` {
		t.Fatalf("From = %q", got)
	}
}
