package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatReplyEmail(t *testing.T) {
	sender := &fakeMailer{}
	mux := http.NewServeMux()
	addChatRoutes(mux, &Notifier{users: fakeUsers{}, mailer: sender}, "chat-secret", "https://toko.test/")
	post := func(token, body string) int {
		t.Helper()
		req := httptest.NewRequest("POST", "/internal/chat-replies", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	if code := post("wrong", `{"user_id":7,"preview":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	for _, bad := range []string{`{"preview":"x"}`, `nope`} {
		if code := post("chat-secret", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code := post("chat-secret", `{"user_id":7,"preview":"Sudah kami kirim, Kak.\nResinya JNE123."}`); code != http.StatusNoContent {
		t.Fatalf("send: %d", code)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d", len(sender.sent))
	}
	email := sender.sent[0]
	for _, want := range []string{"Halo Sari,", "> Sudah kami kirim, Kak.\r\n> Resinya JNE123.", "https://toko.test/#chat"} {
		if !strings.Contains(email.Body, want) {
			t.Errorf("body lacks %q: %q", want, email.Body)
		}
	}
	if email.Subject != "Admin Prastika membalas chat kamu" || !strings.HasPrefix(email.MessageID, "chat-7-") || email.To.Email != "sari@example.com" {
		t.Errorf("email: %+v", email)
	}

	gone := http.NewServeMux()
	addChatRoutes(gone, &Notifier{users: fakeUsers{err: errUserGone}, mailer: sender}, "chat-secret", "https://toko.test")
	req := httptest.NewRequest("POST", "/internal/chat-replies", strings.NewReader(`{"user_id":9,"preview":"x"}`))
	req.Header.Set("Authorization", "Bearer chat-secret")
	w := httptest.NewRecorder()
	gone.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("gone user: %d", w.Code)
	}
}
