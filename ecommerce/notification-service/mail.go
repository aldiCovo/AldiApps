package main

import (
	"bytes"
	"errors"
	"fmt"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

const smtpTimeout = 30 * time.Second

// Email is one message, already in Indonesian for the customer.
type Email struct {
	To      User
	OrderID int64
	Kind    string
	Subject string
	Body    string
}

// mailer is what the notifier needs for sending.
type mailer interface {
	Send(email Email) error
}

// SMTPMailer sends through a server that needs no login, which here is Mailpit.
type SMTPMailer struct {
	addr string
	from mail.Address
}

func newSMTPMailer(addr, from string) (*SMTPMailer, error) {
	parsed, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("MAIL_FROM: %w", err)
	}
	return &SMTPMailer{addr: addr, from: *parsed}, nil
}

// isPermanent reports whether the server refused this message for good. Retrying that would
// block every email behind it.
func isPermanent(err error) bool {
	var refused *textproto.Error
	return errors.As(err, &refused) && refused.Code >= 500
}

func (m *SMTPMailer) Send(email Email) error {
	message, err := m.render(email)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", m.addr, smtpTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(m.addr)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Mail(m.from.Address); err != nil {
		return err
	}
	if err := client.Rcpt(email.To.Email); err != nil {
		return err
	}
	data, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := data.Write(message); err != nil {
		return err
	}
	if err := data.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// render builds the message. Names go through mail.Address and the body through quoted-printable,
// so a customer's name or a product's name can't add headers.
func (m *SMTPMailer) render(email Email) ([]byte, error) {
	to := mail.Address{Name: email.To.Name, Address: email.To.Email}
	domain := m.from.Address[strings.LastIndex(m.from.Address, "@")+1:]
	var message bytes.Buffer
	for _, header := range [][2]string{
		{"From", m.from.String()},
		{"To", to.String()},
		{"Subject", encodeSubject(email.Subject)},
		{"Date", time.Now().Format(time.RFC1123Z)},
		// The same order and kind get the same id, so a mail client can hide a rare repeat.
		{"Message-ID", "<order-" + strconv.FormatInt(email.OrderID, 10) + "-" + email.Kind + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", `text/plain; charset="utf-8"`},
		{"Content-Transfer-Encoding", "quoted-printable"},
	} {
		fmt.Fprintf(&message, "%s: %s\r\n", header[0], header[1])
	}
	message.WriteString("\r\n")
	body := quotedprintable.NewWriter(&message)
	if _, err := body.Write([]byte(email.Body)); err != nil {
		return nil, err
	}
	if err := body.Close(); err != nil {
		return nil, err
	}
	return message.Bytes(), nil
}
