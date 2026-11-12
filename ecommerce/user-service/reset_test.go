package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
)

type fakeMailer struct {
	mu   sync.Mutex
	sent []Email
	fail error
}

func (m *fakeMailer) Send(email Email) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, email)
	return nil
}

func (m *fakeMailer) emails() []Email {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Email(nil), m.sent...)
}

func (m *fakeMailer) clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = nil
}

var resetLink = regexp.MustCompile(`https://shop\.test/#reset=([A-Za-z0-9_-]+)`)

// forgot asks for a link and waits until it would have been sent.
func (s *testServer) forgot(t *testing.T, email string) {
	t.Helper()
	status, body := request(t, http.MethodPost, s.URL+"/password/forgot", fmt.Sprintf(`{"email":%q}`, email), "")
	if status != http.StatusNoContent {
		t.Fatalf("forgot: status %d, body %s", status, body)
	}
	s.resets.Wait()
}

func (s *testServer) lastToken(t *testing.T) string {
	t.Helper()
	sent := s.mail.emails()
	if len(sent) == 0 {
		t.Fatal("no email was sent")
	}
	match := resetLink.FindStringSubmatch(sent[len(sent)-1].Body)
	if match == nil {
		t.Fatalf("the email has no reset link: %s", sent[len(sent)-1].Body)
	}
	return match[1]
}

func (s *testServer) reset(t *testing.T, token, password string) int {
	t.Helper()
	status, _ := request(t, http.MethodPost, s.URL+"/password/reset",
		fmt.Sprintf(`{"token":%q,"password":%q}`, token, password), "")
	return status
}

func (s *testServer) loginStatus(t *testing.T, email, password string) int {
	t.Helper()
	status, _ := request(t, http.MethodPost, s.URL+"/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password), "")
	return status
}

func TestResetSetsANewPassword(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password-lama")

	s.forgot(t, "  Sari@Example.com ")
	sent := s.mail.emails()
	if len(sent) != 1 || sent[0].To.Email != user.Email || sent[0].To.Name != "Sari" {
		t.Fatalf("sent %+v, want one email to %s", sent, user.Email)
	}
	token := s.lastToken(t)

	// Only the hash is kept, so the table alone can't give a working link.
	tokenHash, _ := hashResetToken(token)
	var stored int
	if err := s.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM password_resets WHERE token_hash = $1", tokenHash).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("found %d rows by the token's hash, want 1", stored)
	}

	if status := s.reset(t, token, "password-baru"); status != http.StatusNoContent {
		t.Fatalf("reset: status %d", status)
	}
	if status := s.loginStatus(t, user.Email, "password-lama"); status != http.StatusUnauthorized {
		t.Fatalf("old password: status %d, want 401", status)
	}
	s.login(t, user.Email, "password-baru")

	if status := s.reset(t, token, "password-lagi"); status != http.StatusGone {
		t.Fatalf("second use: status %d, want 410", status)
	}
}

func TestForgotLooksTheSameForUnknownEmails(t *testing.T) {
	s := newTestServer(t, nil)
	s.forgot(t, "tidak-ada@example.com")
	if sent := s.mail.emails(); len(sent) != 0 {
		t.Fatalf("sent %d emails for an unknown address", len(sent))
	}
	status, _ := request(t, http.MethodPost, s.URL+"/password/forgot", `{"email":"bukan email"}`, "")
	if status != http.StatusBadRequest {
		t.Fatalf("bad email: status %d, want 400", status)
	}
}

func TestUsingOneLinkEndsTheOthers(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password-lama")
	s.forgot(t, user.Email)
	first := s.lastToken(t)
	s.forgot(t, user.Email)
	second := s.lastToken(t)
	if first == second {
		t.Fatal("two links have the same token")
	}
	if status := s.reset(t, second, "password-baru"); status != http.StatusNoContent {
		t.Fatalf("reset: status %d", status)
	}
	if status := s.reset(t, first, "password-lain"); status != http.StatusGone {
		t.Fatalf("older link: status %d, want 410", status)
	}
}

func TestExpiredAndMadeUpLinksAreRefused(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password-lama")
	s.forgot(t, user.Email)
	token := s.lastToken(t)
	if _, err := s.pool.Exec(context.Background(),
		"UPDATE password_resets SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"expired":    token,
		"made up":    strings.Repeat("A", 43),
		"too short":  "abc",
		"not base64": strings.Repeat("!", 43),
		"empty":      "",
	} {
		if status := s.reset(t, bad, "password-baru"); status != http.StatusGone {
			t.Errorf("%s: status %d, want 410", name, status)
		}
	}
	s.login(t, user.Email, "password-lama")
}

func TestResetChecksTheNewPassword(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password-lama")
	s.forgot(t, user.Email)
	token := s.lastToken(t)
	if status := s.reset(t, token, "pendek"); status != http.StatusBadRequest {
		t.Fatalf("short password: status %d, want 400", status)
	}
	// A refused password doesn't use up the link.
	if status := s.reset(t, token, "password-baru"); status != http.StatusNoContent {
		t.Fatalf("reset: status %d", status)
	}
}

func TestResetLinksPerHourAreLimited(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password-lama")
	for range maxResetsPerHour + 2 {
		s.forgot(t, user.Email)
	}
	if sent := len(s.mail.emails()); sent != maxResetsPerHour {
		t.Fatalf("sent %d emails, want %d", sent, maxResetsPerHour)
	}
}

func TestResetLinksAtOnceStayUnderTheLimit(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password-lama")
	var wg sync.WaitGroup
	for range maxResetsSending {
		wg.Go(func() { s.resets.Request(user.Email) })
	}
	wg.Wait()
	s.resets.Wait()
	if sent := len(s.mail.emails()); sent > maxResetsPerHour {
		t.Fatalf("sent %d emails, want at most %d", sent, maxResetsPerHour)
	}
}

func TestAFailedEmailIsOnlyLogged(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password-lama")
	s.mail.fail = errors.New("mail server down")
	s.forgot(t, user.Email)
	s.mail.fail = nil
	s.forgot(t, user.Email)
	s.lastToken(t)
}
