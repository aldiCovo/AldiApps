package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestReplyEmails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var mu sync.Mutex
	var asked []map[string]any
	status := http.StatusNoContent
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/internal/chat-replies" || r.Header.Get("Authorization") != "Bearer notify-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		asked = append(asked, in)
		w.WriteHeader(status)
	}))
	defer notify.Close()
	store := &Store{pool: h.pool}
	emailer := newReplyEmailer(store, notify.URL, "notify-token")
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(asked)
	}
	age := func(minutes int) {
		t.Helper()
		if _, err := h.pool.Exec(ctx, "UPDATE messages SET created_at = created_at - make_interval(mins => $1)", minutes); err != nil {
			t.Fatal(err)
		}
	}

	h.customer(t, 7, "POST", "/chat/messages", `{"body":"Kapan dikirim?"}`)
	h.customer(t, 8, "POST", "/chat/messages", `{"body":"Halo"}`)
	h.admin(t, "POST", "/internal/conversations/7/messages", `{"body":"Besok pagi, Kak.","admin_id":16}`)
	h.admin(t, "POST", "/internal/conversations/8/messages", `{"body":"Halo juga","admin_id":16}`)
	h.customer(t, 8, "POST", "/chat/read", "")

	// Too new: nothing yet.
	emailer.sendDue(ctx)
	if count() != 0 {
		t.Fatal("emailed a new answer")
	}
	// Unread for 6 minutes: one email, for customer 7 only (8 read it).
	age(6)
	emailer.sendDue(ctx)
	emailer.sendDue(ctx)
	if count() != 1 || asked[0]["user_id"] != float64(7) || asked[0]["preview"] != "Besok pagi, Kak." {
		t.Fatalf("asked: %v", asked)
	}
	// Another answer within the hour waits; after the hour it goes.
	h.admin(t, "POST", "/internal/conversations/7/messages", `{"body":"Sudah dikirim, resi JNE1","admin_id":16}`)
	age(10)
	emailer.sendDue(ctx)
	if count() != 1 {
		t.Fatal("two emails within an hour")
	}
	if _, err := h.pool.Exec(ctx, "UPDATE conversations SET emailed_at = emailed_at - interval '61 minutes'"); err != nil {
		t.Fatal(err)
	}
	emailer.sendDue(ctx)
	if count() != 2 || asked[1]["preview"] != "Sudah dikirim, resi JNE1" {
		t.Fatalf("asked: %v", asked)
	}
	// The bot's answers send nothing; a failure is tried again next time.
	if _, err := h.pool.Exec(ctx, "UPDATE conversations SET emailed_at = NULL"); err != nil {
		t.Fatal(err)
	}
	h.pool.Exec(ctx, "INSERT INTO messages (user_id, admin_id, body, created_at) VALUES (8, 0, 'bot', now() - interval '1 hour')")
	emailer.sendDue(ctx)
	if count() != 2 {
		t.Fatalf("bot answer emailed: %v", asked)
	}
	h.admin(t, "POST", "/internal/conversations/8/messages", `{"body":"Ada lagi?","admin_id":16}`)
	age(6)
	status = http.StatusBadGateway
	emailer.sendDue(ctx)
	status = http.StatusNoContent
	emailer.sendDue(ctx)
	if count() != 4 || asked[3]["user_id"] != float64(8) {
		t.Fatalf("retry: %v", asked)
	}
	emailer.sendDue(ctx)
	if count() != 4 {
		t.Fatal("emailed twice")
	}
}
