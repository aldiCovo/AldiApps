package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	testKeyID         = "test-key"
	testInternalToken = "internal-token-for-tests-0123456789abcdef"
)

// signals remembers what would have gone to NATS.
type signals struct {
	mu   sync.Mutex
	sent []string
}

func (s *signals) Message(userID, messageID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, fmt.Sprintf("%d:%d", userID, messageID))
}

func (s *signals) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

type harness struct {
	*httptest.Server
	private ed25519.PrivateKey
	signals *signals
	pool    *pgxpool.Pool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE messages, conversations RESTART IDENTITY"); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":%q,"x":%q}]}`, testKeyID, base64.RawURLEncoding.EncodeToString(public))
	}))
	t.Cleanup(users.Close)
	sig := &signals{}
	server := httptest.NewServer(newHandler(&Store{pool: pool}, newVerifier(users.URL), sig, testInternalToken))
	t.Cleanup(server.Close)
	return &harness{Server: server, private: private, signals: sig, pool: pool}
}

func (h *harness) token(t *testing.T, userID int64) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{
		Issuer:    tokenIssuer,
		Audience:  jwt.ClaimStrings{tokenAudience},
		Subject:   strconv.FormatInt(userID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	token.Header["kid"] = testKeyID
	signed, err := token.SignedString(h.private)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// customer calls a customer route as userID (0 for no token).
func (h *harness) customer(t *testing.T, userID int64, method, path, body string) (int, []byte) {
	t.Helper()
	bearer := ""
	if userID != 0 {
		bearer = h.token(t, userID)
	}
	return h.call(t, bearer, method, path, body)
}

func (h *harness) admin(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	return h.call(t, testInternalToken, method, path, body)
}

func (h *harness) call(t *testing.T, bearer, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

// readPage(t)(status, body) decodes a page and fails the test on anything but 200.
func readPage(t *testing.T) func(int, []byte) Page {
	return func(status int, body []byte) Page {
		t.Helper()
		var page Page
		if status != http.StatusOK || json.Unmarshal(body, &page) != nil {
			t.Fatalf("status %d, body %s", status, body)
		}
		return page
	}
}

func bodies(page Page) string {
	var out []string
	for _, m := range page.Messages {
		out = append(out, m.From[:1]+":"+m.Body)
	}
	return strings.Join(out, ",")
}

type conversationList struct {
	Conversations []Summary `json:"conversations"`
	Total         int       `json:"total"`
	Waiting       int       `json:"waiting"`
}

func readList(t *testing.T) func(int, []byte) conversationList {
	return func(status int, body []byte) conversationList {
		t.Helper()
		var list conversationList
		if status != http.StatusOK || json.Unmarshal(body, &list) != nil {
			t.Fatalf("status %d, body %s", status, body)
		}
		return list
	}
}

func TestConversation(t *testing.T) {
	h := newHarness(t)
	const sari, budi = 7, 8

	// No conversation yet: an empty page, nothing unread, and nothing for the admins.
	if page := readPage(t)(h.customer(t, sari, "GET", "/chat", "")); len(page.Messages) != 0 || page.Unread != 0 {
		t.Fatalf("empty: %+v", page)
	}
	if list := readList(t)(h.admin(t, "GET", "/internal/conversations", "")); list.Total != 0 || list.Waiting != 0 {
		t.Fatalf("empty list: %+v", list)
	}
	// Admins can't start a conversation.
	if status, _ := h.admin(t, "POST", fmt.Sprintf("/internal/conversations/%d/messages", sari), `{"body":"Halo","admin_id":16}`); status != http.StatusNotFound {
		t.Fatalf("admin first: status %d", status)
	}

	status, body := h.customer(t, sari, "POST", "/chat/messages", `{"body":"  Kak, ukuran M\r\nberapa?  "}`)
	if status != http.StatusCreated || !strings.Contains(string(body), `"body":"Kak, ukuran M\nberapa?"`) || !strings.Contains(string(body), `"from":"customer"`) {
		t.Fatalf("send: status %d, body %s", status, body)
	}
	h.customer(t, budi, "POST", "/chat/messages", `{"body":"Pesanan saya?"}`)
	if h.signals.count() != 2 {
		t.Fatalf("signals: %v", h.signals.sent)
	}

	list := readList(t)(h.admin(t, "GET", "/internal/conversations?filter=waiting", ""))
	if list.Total != 2 || list.Waiting != 2 || list.Conversations[0].UserID != budi || !list.Conversations[1].Waiting || list.Conversations[1].Unread != 1 {
		t.Fatalf("waiting list: %+v", list)
	}
	if _, body := h.admin(t, "GET", "/internal/chat/waiting", ""); strings.TrimSpace(string(body)) != `{"waiting":2}` {
		t.Fatalf("waiting count: %s", body)
	}

	// The admin opens Sari's chat and answers.
	page := readPage(t)(h.admin(t, "GET", fmt.Sprintf("/internal/conversations/%d/messages", sari), ""))
	if bodies(page) != "c:Kak, ukuran M\nberapa?" || page.Unread != 1 || page.ReadUpTo != page.Messages[0].ID {
		t.Fatalf("admin page: %+v", page)
	}
	if status, _ := h.admin(t, "POST", fmt.Sprintf("/internal/conversations/%d/read", sari), ""); status != http.StatusNoContent {
		t.Fatalf("admin read: status %d", status)
	}
	status, body = h.admin(t, "POST", fmt.Sprintf("/internal/conversations/%d/messages", sari), `{"body":"Lingkar dada 96 cm","admin_id":16}`)
	if status != http.StatusCreated || !strings.Contains(string(body), `"from":"admin"`) {
		t.Fatalf("admin send: status %d, body %s", status, body)
	}
	if h.signals.sent[2] != "7:3" {
		t.Fatalf("signal to sari: %v", h.signals.sent)
	}
	list = readList(t)(h.admin(t, "GET", "/internal/conversations?filter=waiting", ""))
	if list.Total != 1 || list.Waiting != 1 || list.Conversations[0].UserID != budi {
		t.Fatalf("after answer: %+v", list)
	}
	list = readList(t)(h.admin(t, "GET", "/internal/conversations?filter=all", ""))
	if list.Total != 2 || list.Conversations[0].UserID != sari || list.Conversations[0].Waiting || list.Conversations[0].LastMessage.From != "admin" {
		t.Fatalf("all: %+v", list)
	}

	// Sari sees the answer as unread until she opens it, and that the admin read hers.
	if _, body := h.customer(t, sari, "GET", "/chat/unread", ""); strings.TrimSpace(string(body)) != `{"unread":1}` {
		t.Fatalf("unread: %s", body)
	}
	page = readPage(t)(h.customer(t, sari, "GET", "/chat", ""))
	if bodies(page) != "c:Kak, ukuran M\nberapa?,a:Lingkar dada 96 cm" || page.Unread != 1 || page.ReadUpTo != 3 {
		t.Fatalf("sari page: %+v", page)
	}
	h.customer(t, sari, "POST", "/chat/read", "")
	if _, body := h.customer(t, sari, "GET", "/chat/unread", ""); strings.TrimSpace(string(body)) != `{"unread":0}` {
		t.Fatalf("after read: %s", body)
	}
	// Budi only ever sees his own conversation.
	if page := readPage(t)(h.customer(t, budi, "GET", "/chat", "")); bodies(page) != "c:Pesanan saya?" {
		t.Fatalf("budi: %s", bodies(page))
	}

	// Older messages come a page at a time.
	for i := range 4 {
		h.customer(t, sari, "POST", "/chat/messages", fmt.Sprintf(`{"body":"pesan %d"}`, i))
	}
	page = readPage(t)(h.customer(t, sari, "GET", "/chat?limit=3", ""))
	if bodies(page) != "c:pesan 1,c:pesan 2,c:pesan 3" || !page.HasMore {
		t.Fatalf("newest 3: %s %v", bodies(page), page.HasMore)
	}
	page = readPage(t)(h.customer(t, sari, "GET", fmt.Sprintf("/chat?limit=3&before=%d", page.Messages[0].ID), ""))
	if bodies(page) != "c:Kak, ukuran M\nberapa?,a:Lingkar dada 96 cm,c:pesan 0" || page.HasMore {
		t.Fatalf("older: %s %v", bodies(page), page.HasMore)
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/chat", "", http.StatusUnauthorized},
		{"POST", "/chat/messages", `{"body":"x"}`, http.StatusUnauthorized},
		{"GET", "/chat/unread", "", http.StatusUnauthorized},
	} {
		if status, body := h.customer(t, 0, c.method, c.path, c.body); status != c.status {
			t.Errorf("%s %s: status %d, body %s", c.method, c.path, status, body)
		}
	}
	for _, bad := range []string{`{"body":"   "}`, `{"body":""}`, fmt.Sprintf(`{"body":%q}`, strings.Repeat("a", maxBodyLength+1)), `{"body":"x","admin_id":1}`, `{"body":"a\u0000b"}`} {
		if status, body := h.customer(t, 7, "POST", "/chat/messages", bad); status != http.StatusBadRequest {
			t.Errorf("%.40s: status %d, body %s", bad, status, body)
		}
	}
	// A customer token can't reach the admin routes, and the internal token isn't a customer.
	if status, _ := h.customer(t, 7, "GET", "/internal/conversations", ""); status != http.StatusUnauthorized {
		t.Errorf("customer on internal: %d", status)
	}
	if status, _ := h.call(t, "wrong", "GET", "/internal/chat/waiting", ""); status != http.StatusUnauthorized {
		t.Errorf("wrong internal token: %d", status)
	}
	h.customer(t, 7, "POST", "/chat/messages", `{"body":"hai"}`)
	for _, c := range []struct{ path, body string }{
		{"/internal/conversations/7/messages", `{"body":"x"}`},
		{"/internal/conversations/7/messages", `{"body":"","admin_id":16}`},
	} {
		if status, _ := h.admin(t, "POST", c.path, c.body); status != http.StatusBadRequest {
			t.Errorf("%s %s: %d", c.path, c.body, status)
		}
	}
	for _, path := range []string{"/internal/conversations/abc/messages", "/internal/conversations/0/messages", "/internal/conversations/99/messages"} {
		if status, _ := h.admin(t, "GET", path, ""); status != http.StatusNotFound {
			t.Errorf("%s: %d", path, status)
		}
	}
	if status, _ := h.admin(t, "GET", "/internal/conversations?filter=x", ""); status != http.StatusBadRequest {
		t.Errorf("bad filter")
	}
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t)
	// Sent at once, the limit still holds.
	var wg sync.WaitGroup
	statuses := make(chan int, maxCustomerMessagesPerMinute+5)
	for i := range maxCustomerMessagesPerMinute + 5 {
		wg.Go(func() {
			status, _ := h.customer(t, 7, "POST", "/chat/messages", fmt.Sprintf(`{"body":"pesan %d"}`, i))
			statuses <- status
		})
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusCreated] != maxCustomerMessagesPerMinute || counts[http.StatusTooManyRequests] != 5 {
		t.Fatalf("statuses: %v", counts)
	}
	// Admins aren't limited.
	if status, _ := h.admin(t, "POST", "/internal/conversations/7/messages", `{"body":"Sabar ya kak","admin_id":16}`); status != http.StatusCreated {
		t.Fatalf("admin after limit: %d", status)
	}
}
