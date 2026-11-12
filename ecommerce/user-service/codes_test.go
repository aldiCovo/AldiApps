package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"testing"
)

type sentWhatsApp struct{ phone, text string }

type fakeWhatsApp struct {
	mu   sync.Mutex
	sent []sentWhatsApp
	fail error
}

func (f *fakeWhatsApp) Send(_ context.Context, phone, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, sentWhatsApp{phone, text})
	return nil
}

func (f *fakeWhatsApp) last(t *testing.T) sentWhatsApp {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("no WhatsApp message was sent")
	}
	return f.sent[len(f.sent)-1]
}

var sixDigits = regexp.MustCompile(`\b([0-9]{6})\b`)

func findCode(t *testing.T, text string) string {
	t.Helper()
	match := sixDigits.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("no code in %q", text)
	}
	return match[1]
}

// signUp registers without the test shortcut, so the email still needs its code.
func (s *testServer) signUp(t *testing.T, email, password string) {
	t.Helper()
	status, body := request(t, http.MethodPost, s.URL+"/register",
		fmt.Sprintf(`{"email":%q,"name":"Sari","password":%q}`, email, password), "")
	if status != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", status, body)
	}
	s.codes.Wait()
}

func (s *testServer) lastEmailCode(t *testing.T) string {
	t.Helper()
	sent := s.mail.emails()
	if len(sent) == 0 {
		t.Fatal("no email was sent")
	}
	return findCode(t, sent[len(sent)-1].Body)
}

func (s *testServer) verifyEmail(t *testing.T, email, code string) (int, []byte) {
	t.Helper()
	return request(t, http.MethodPost, s.URL+"/email/verify", fmt.Sprintf(`{"email":%q,"code":%q}`, email, code), "")
}

func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestNewAccountNeedsTheEmailCode(t *testing.T) {
	s := newTestServer(t, nil)
	s.signUp(t, "sari@example.com", "rahasia123")
	sent := s.mail.emails()
	if len(sent) != 1 || sent[0].To.Email != "sari@example.com" {
		t.Fatalf("sent %+v", sent)
	}
	code := s.lastEmailCode(t)

	if status := s.loginStatus(t, "sari@example.com", "rahasia123"); status != http.StatusForbidden {
		t.Fatalf("login before verifying: status %d", status)
	}
	// The refused login sent a fresh code, which ends the first one.
	s.codes.Wait()
	if status, _ := s.verifyEmail(t, "sari@example.com", code); status != http.StatusUnprocessableEntity {
		t.Fatalf("an ended code: status %d", status)
	}
	code = s.lastEmailCode(t)
	if status, _ := s.verifyEmail(t, "sari@example.com", wrongCode(code)); status != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong code: status %d", status)
	}
	status, body := s.verifyEmail(t, "sari@example.com", code)
	if status != http.StatusOK {
		t.Fatalf("the right code: status %d, body %s", status, body)
	}
	var login loginResponse
	if err := json.Unmarshal(body, &login); err != nil || login.Token == "" || !login.User.EmailVerified {
		t.Fatalf("login %+v, error %v", login, err)
	}
	if status := s.loginStatus(t, "sari@example.com", "rahasia123"); status != http.StatusOK {
		t.Fatalf("login after verifying: status %d", status)
	}
	if status, _ := s.verifyEmail(t, "sari@example.com", code); status != http.StatusUnprocessableEntity {
		t.Fatalf("a used code: status %d", status)
	}
}

func TestWrongPasswordDoesNotSendACode(t *testing.T) {
	s := newTestServer(t, nil)
	s.signUp(t, "sari@example.com", "rahasia123")
	s.mail.clear()
	if status := s.loginStatus(t, "sari@example.com", "salah12345"); status != http.StatusUnauthorized {
		t.Fatalf("status %d", status)
	}
	s.codes.Wait()
	if sent := s.mail.emails(); len(sent) != 0 {
		t.Fatalf("sent %d emails", len(sent))
	}
}

func TestEmailCodeTriesAreLimited(t *testing.T) {
	s := newTestServer(t, nil)
	s.signUp(t, "sari@example.com", "rahasia123")
	code := s.lastEmailCode(t)
	for range maxCodeAttempts {
		if status, _ := s.verifyEmail(t, "sari@example.com", wrongCode(code)); status != http.StatusUnprocessableEntity {
			t.Fatalf("status %d", status)
		}
	}
	if status, _ := s.verifyEmail(t, "sari@example.com", code); status != http.StatusUnprocessableEntity {
		t.Fatalf("the right code after too many tries: status %d", status)
	}
}

func TestEmailCodeLooksTheSameForAnyEmail(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "verified@example.com", "rahasia123")
	for _, email := range []string{"nobody@example.com", "verified@example.com"} {
		status, body := request(t, http.MethodPost, s.URL+"/email/code", fmt.Sprintf(`{"email":%q}`, email), "")
		if status != http.StatusNoContent {
			t.Fatalf("%s: status %d, body %s", email, status, body)
		}
	}
	s.codes.Wait()
	if sent := s.mail.emails(); len(sent) != 0 {
		t.Fatalf("sent %d emails", len(sent))
	}
	if status, _ := s.verifyEmail(t, "nobody@example.com", "123456"); status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown email: status %d", status)
	}
	if status, _ := s.verifyEmail(t, "sari", "123456"); status != http.StatusBadRequest {
		t.Fatalf("bad email: status %d", status)
	}
}

func TestEmailCodesPerHourAreLimited(t *testing.T) {
	s := newTestServer(t, nil)
	s.signUp(t, "sari@example.com", "rahasia123")
	for range maxCodesPerHour + 2 {
		request(t, http.MethodPost, s.URL+"/email/code", `{"email":"sari@example.com"}`, "")
		s.codes.Wait()
	}
	if sent := len(s.mail.emails()); sent != maxCodesPerHour {
		t.Fatalf("sent %d emails", sent)
	}
}

func (s *testServer) setPhone(t *testing.T, token, phone string) Profile {
	t.Helper()
	status, body := request(t, http.MethodPut, s.URL+"/me", fmt.Sprintf(`{"name":"Sari","phone":%q}`, phone), token)
	if status != http.StatusOK {
		t.Fatalf("PUT /me: status %d, body %s", status, body)
	}
	return readProfile(t, body)
}

func TestPhoneVerification(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "rahasia123")
	token := s.login(t, "sari@example.com", "rahasia123").Token

	if status, _ := request(t, http.MethodPost, s.URL+"/me/phone/code", "", token); status != http.StatusConflict {
		t.Fatalf("no phone: status %d", status)
	}
	if profile := s.setPhone(t, token, "0812-3456-7890"); profile.PhoneVerified {
		t.Fatal("a new phone is verified")
	}
	if status, body := request(t, http.MethodPost, s.URL+"/me/phone/code", "", token); status != http.StatusNoContent {
		t.Fatalf("send: status %d, body %s", status, body)
	}
	sent := s.wa.last(t)
	if sent.phone != "081234567890" {
		t.Fatalf("sent to %q", sent.phone)
	}
	code := findCode(t, sent.text)

	verify := func(code string) (int, []byte) {
		return request(t, http.MethodPost, s.URL+"/me/phone/verify", fmt.Sprintf(`{"code":%q}`, code), token)
	}
	if status, _ := verify(wrongCode(code)); status != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong code: status %d", status)
	}
	status, body := verify(code)
	if status != http.StatusOK || !readProfile(t, body).PhoneVerified {
		t.Fatalf("the right code: status %d, body %s", status, body)
	}
	if status, _ := request(t, http.MethodPost, s.URL+"/me/phone/code", "", token); status != http.StatusConflict {
		t.Fatalf("already verified: status %d", status)
	}
	// Saving the same number keeps it verified, and a new one isn't.
	if profile := s.setPhone(t, token, "+62 812 3456 7890"); !profile.PhoneVerified {
		t.Fatal("the same phone lost its verification")
	}
	if profile := s.setPhone(t, token, "081299998888"); profile.PhoneVerified {
		t.Fatal("a changed phone is still verified")
	}
}

func TestPhoneCodeEndsWhenThePhoneChanges(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "rahasia123")
	token := s.login(t, "sari@example.com", "rahasia123").Token
	s.setPhone(t, token, "081234567890")
	request(t, http.MethodPost, s.URL+"/me/phone/code", "", token)
	code := findCode(t, s.wa.last(t).text)
	s.setPhone(t, token, "081299998888")
	status, _ := request(t, http.MethodPost, s.URL+"/me/phone/verify", fmt.Sprintf(`{"code":%q}`, code), token)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", status)
	}
	if profile := s.setPhone(t, token, "081299998888"); profile.PhoneVerified {
		t.Fatal("the new phone was verified by the old phone's code")
	}
}

func TestPhoneCodeThatCantBeSent(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "rahasia123")
	token := s.login(t, "sari@example.com", "rahasia123").Token
	s.setPhone(t, token, "081234567890")
	s.wa.fail = errors.New("the number has no WhatsApp")
	if status, _ := request(t, http.MethodPost, s.URL+"/me/phone/code", "", token); status != http.StatusBadGateway {
		t.Fatalf("status %d", status)
	}
	s.codes.whatsApp = nil
	if status, _ := request(t, http.MethodPost, s.URL+"/me/phone/code", "", token); status != http.StatusServiceUnavailable {
		t.Fatalf("without WhatsApp: status %d", status)
	}
	if status, _ := request(t, http.MethodPost, s.URL+"/me/phone/code", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("without a token: status %d", status)
	}
}

func TestCodesLookRandom(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		code, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != codeDigits {
			t.Fatalf("code %q", code)
		}
		seen[code] = true
	}
	if len(seen) < 45 {
		t.Fatalf("only %d different codes in 50", len(seen))
	}
}
