package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

const smtpTimeout = 30 * time.Second

// Email is one message to an account.
type Email struct {
	To      User
	Subject string
	Body    string
}

// mailer is what the reset links need for sending.
type mailer interface {
	Send(email Email) error
}

// SMTPMailer sends through Mailpit, which needs no login, or through a relay such as Brevo,
// which needs STARTTLS and a login.
type SMTPMailer struct {
	addr string
	from mail.Address
	// Empty for Mailpit.
	username, password string
}

// withLogin makes the mailer switch to TLS and log in before sending.
func (m *SMTPMailer) withLogin(username, password string) *SMTPMailer {
	m.username, m.password = username, password
	return m
}

func newSMTPMailer(addr, from string) (*SMTPMailer, error) {
	parsed, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("MAIL_FROM: %w", err)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("SMTP_ADDR: %w", err)
	}
	return &SMTPMailer{addr: addr, from: *parsed}, nil
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
	host, _, _ := net.SplitHostPort(m.addr)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if m.username != "" {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starting TLS: %w", err)
		}
		if err := client.Auth(smtp.PlainAuth("", m.username, m.password, host)); err != nil {
			return fmt.Errorf("logging in to the mail server: %w", err)
		}
	}
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

// render builds the message. The name goes through mail.Address and the body through
// quoted-printable, so a customer's name can't add headers.
func (m *SMTPMailer) render(email Email) ([]byte, error) {
	to := mail.Address{Name: email.To.Name, Address: email.To.Email}
	domain := m.from.Address[strings.LastIndex(m.from.Address, "@")+1:]
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	var message bytes.Buffer
	for _, header := range [][2]string{
		{"From", m.from.String()},
		{"To", to.String()},
		{"Subject", mime.QEncoding.Encode("utf-8", email.Subject)},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", "<reset-" + hex.EncodeToString(id) + "@" + domain + ">"},
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
