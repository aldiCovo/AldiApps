package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func (s *testServer) tryLogin(t *testing.T, email, password string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.URL+"/login",
		strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

func TestWrongPasswordsLockTheEmailForAWhile(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	for i := range maxLoginFailures {
		if status, _ := s.tryLogin(t, "sari@example.com", "wrong-pass"); status != http.StatusUnauthorized {
			t.Fatalf("try %d: status %d", i+1, status)
		}
	}
	// Even the right password waits now, or guessing would go on.
	status, header := s.tryLogin(t, "Sari@Example.com", "password123")
	if status != http.StatusTooManyRequests {
		t.Fatalf("after the limit: status %d", status)
	}
	retry, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || retry <= 0 || retry > int(loginFailureWindow.Seconds()) {
		t.Fatalf("Retry-After %q", header.Get("Retry-After"))
	}
	// Other emails aren't touched.
	s.register(t, "budi@example.com", "password123")
	s.login(t, "budi@example.com", "password123")

	// Once the tries are old enough, the owner gets in.
	if _, err := s.pool.Exec(context.Background(),
		"UPDATE login_failures SET created_at = now() - interval '16 minutes'"); err != nil {
		t.Fatal(err)
	}
	s.login(t, "sari@example.com", "password123")
}

func TestUnknownEmailsLockTheSame(t *testing.T) {
	s := newTestServer(t, nil)
	for range maxLoginFailures {
		s.tryLogin(t, "nobody@example.com", "wrong-pass")
	}
	if status, _ := s.tryLogin(t, "nobody@example.com", "wrong-pass"); status != http.StatusTooManyRequests {
		t.Fatalf("status %d", status)
	}
}

func TestRightPasswordForgetsWrongOnes(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	for range maxLoginFailures - 1 {
		s.tryLogin(t, "sari@example.com", "wrong-pass")
	}
	s.login(t, "sari@example.com", "password123")
	for i := range maxLoginFailures - 1 {
		if status, _ := s.tryLogin(t, "sari@example.com", "wrong-pass"); status != http.StatusUnauthorized {
			t.Fatalf("try %d: status %d", i+1, status)
		}
	}
}

func TestResetLinkUnlocksTheEmail(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	for range maxLoginFailures {
		s.tryLogin(t, "sari@example.com", "wrong-pass")
	}
	s.forgot(t, "sari@example.com")
	status, body := request(t, http.MethodPost, s.URL+"/password/reset",
		fmt.Sprintf(`{"token":%q,"password":"password456"}`, s.lastToken(t)), "")
	if status != http.StatusNoContent {
		t.Fatalf("reset: status %d, body %s", status, body)
	}
	s.login(t, "sari@example.com", "password456")
}

func TestTriesAtOnceDontPassTheLimit(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	var wg sync.WaitGroup
	var mu sync.Mutex
	wrong := 0
	for range maxLoginFailures * 2 {
		wg.Go(func() {
			if status, _ := s.tryLogin(t, "sari@example.com", "wrong-pass"); status == http.StatusUnauthorized {
				mu.Lock()
				wrong++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wrong > maxLoginFailures {
		t.Fatalf("%d passwords were checked, want at most %d", wrong, maxLoginFailures)
	}
}
