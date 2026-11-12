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

// signals remembers what would have gone to NATS: new messages, and how many other changes.
type signals struct {
	mu      sync.Mutex
	sent    []string
	changes int
}

func (s *signals) Message(userID, messageID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if messageID == 0 {
		s.changes++
		return
	}
	s.sent = append(s.sent, fmt.Sprintf("%d:%d", userID, messageID))
}

func (s *signals) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

type harness struct {
	*httptest.Server
	private  ed25519.PrivateKey
	signals  *signals
	pool     *pgxpool.Pool
	bot      *Assistant
	claude   *fakeClaude
	telegram *fakeTelegram
}

// newHarness runs chat-service without a bot, as before there was one.
func newHarness(t *testing.T) *harness {
	return newHarnessWith(t, false)
}

func newHarnessWith(t *testing.T, withBot bool) *harness {
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
	if _, err := pool.Exec(ctx, "TRUNCATE messages, conversations, handoffs RESTART IDENTITY"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE bot_settings SET mode = 'always', store_info = '', telegram = true, quiet_outside_hours = true"); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me" {
			if tokenSubject(r) == "" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"id":%s,"name":"Sari Maharani","email":"sari@contoh.id"}`, tokenSubject(r))
			return
		}
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":%q,"x":%q}]}`, testKeyID, base64.RawURLEncoding.EncodeToString(public))
	}))
	t.Cleanup(users.Close)
	// The catalog has product 5; product 500 breaks it.
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/products/5":
			fmt.Fprint(w, `{"id":5,"name":"Dress Linen Midi","price":289000,"category_id":3,"variants":[]}`)
		case "/products/500":
			http.Error(w, "broken", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(catalog.Close)
	// Order 9 is customer 7's; anyone else gets 404, as order-service answers.
	orders := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenSubject(r) != "7" || r.URL.Path != "/orders/9" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"id":9,"status":"shipped","total":319000,"tracking_number":"JNE123","shipping_address":{"street":"Jl. Melati 5","city":"Bandung"},"items":[{"product_name":"Dress Linen Midi"},{"product_name":"Hijab Voal"}]}`)
	}))
	t.Cleanup(orders.Close)
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"total":1,"products":[{"id":5,"name":"Dress Linen Midi %s","price":289000,"variants":[{"size":"M","color":"Hitam","stock":3}]}]}`, r.URL.Query().Get("q"))
	}))
	t.Cleanup(search.Close)
	claude := &fakeClaude{}
	claudeServer := httptest.NewServer(claude)
	t.Cleanup(claudeServer.Close)
	telegram := &fakeTelegram{}
	telegramServer := httptest.NewServer(telegram)
	t.Cleanup(telegramServer.Close)

	sig := &signals{}
	store := &Store{pool: pool}
	lookup := newLookup(catalog.URL, orders.URL)
	config := assistantConfig{
		searchURL: search.URL, catalogURL: catalog.URL, orderURL: orders.URL, userURL: users.URL,
		telegramURL: telegramServer.URL, telegramToken: "bot-token", telegramChatID: "-100",
		adminURL: "https://toko.test/admin", debounce: 0,
	}
	if withBot {
		config.anthropicKey, config.anthropicURL, config.debounce = "test-key", claudeServer.URL, 150*time.Millisecond
	}
	bot := newAssistant(store, sig, lookup, config)
	server := httptest.NewServer(newHandler(store, newVerifier(users.URL), sig, lookup, bot, testInternalToken))
	t.Cleanup(server.Close)
	t.Cleanup(bot.Wait)
	return &harness{Server: server, private: private, signals: sig, pool: pool, bot: bot, claude: claude, telegram: telegram}
}

// tokenSubject reads the user from a test token without checking it.
func tokenSubject(r *http.Request) string {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims := jwt.RegisteredClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil {
		return ""
	}
	return claims.Subject
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
	// Without a bot, every message goes to the admins.
	h.bot.Wait()
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

func TestAttachments(t *testing.T) {
	h := newHarness(t)

	// A card alone, without text.
	status, body := h.customer(t, 7, "POST", "/chat/messages", `{"body":"  ","attach":{"kind":"product","id":5}}`)
	if status != http.StatusCreated {
		t.Fatalf("product card: %d %s", status, body)
	}
	var sent struct{ Message Message }
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	card := sent.Message.Attachment
	if sent.Message.Body != "" || card == nil || card.Kind != "product" || card.ID != 5 || card.Name != "Dress Linen Midi" ||
		card.Price != 289000 || card.CategoryID == nil || *card.CategoryID != 3 {
		t.Fatalf("product card = %s", body)
	}

	// The customer's own order, with a question.
	status, body = h.customer(t, 7, "POST", "/chat/messages", `{"body":"Sudah sampai mana?","attach":{"kind":"order","id":9}}`)
	if status != http.StatusCreated || !strings.Contains(string(body), `"attachment":{"kind":"order","id":9,"name":"Dress Linen Midi","status":"shipped","total":319000,"more":1}`) {
		t.Fatalf("order card: %d %s", status, body)
	}

	for _, c := range []struct {
		name, user, body string
		status           int
	}{
		{"someone else's order", "8", `{"body":"x","attach":{"kind":"order","id":9}}`, http.StatusUnprocessableEntity},
		{"missing product", "7", `{"body":"x","attach":{"kind":"product","id":6}}`, http.StatusUnprocessableEntity},
		{"catalog down", "7", `{"body":"x","attach":{"kind":"product","id":500}}`, http.StatusBadGateway},
		{"unknown kind", "7", `{"body":"x","attach":{"kind":"voucher","id":1}}`, http.StatusBadRequest},
		{"bad id", "7", `{"body":"x","attach":{"kind":"product","id":0}}`, http.StatusBadRequest},
		{"no text, no card", "7", `{"body":""}`, http.StatusBadRequest},
	} {
		user, _ := strconv.ParseInt(c.user, 10, 64)
		if status, body := h.customer(t, user, "POST", "/chat/messages", c.body); status != c.status {
			t.Errorf("%s: %d %s, want %d", c.name, status, body, c.status)
		}
	}

	// Admins attach products, never orders.
	if status, body := h.admin(t, "POST", "/internal/conversations/7/messages", `{"body":"","attach":{"kind":"product","id":5},"admin_id":1}`); status != http.StatusCreated {
		t.Errorf("admin product card: %d %s", status, body)
	}
	if status, body := h.admin(t, "POST", "/internal/conversations/7/messages", `{"body":"x","attach":{"kind":"order","id":9},"admin_id":1}`); status != http.StatusBadRequest {
		t.Errorf("admin order card: %d %s", status, body)
	}

	// Both sides read the cards back, and the admins' list shows the last one.
	page := readPage(t)(h.customer(t, 7, "GET", "/chat", ""))
	if len(page.Messages) != 3 || page.Messages[1].Attachment == nil || page.Messages[1].Attachment.Status != "shipped" ||
		page.Messages[2].From != "admin" || page.Messages[2].Attachment == nil {
		t.Fatalf("page = %+v", page)
	}
	_, body = h.admin(t, "GET", "/internal/conversations?filter=all", "")
	if !strings.Contains(string(body), `"last_message":{"id":3,"from":"admin","body":"","attachment":{"kind":"product"`) {
		t.Errorf("list = %s", body)
	}
	// Customer 8, refused above, has no conversation.
	if page := readPage(t)(h.customer(t, 8, "GET", "/chat", "")); len(page.Messages) != 0 {
		t.Errorf("customer 8 = %+v", page)
	}
}

func TestQuickReplies(t *testing.T) {
	h := newHarness(t)
	// Start from the four the migration adds, whatever earlier tests left.
	if _, err := h.pool.Exec(context.Background(), "DELETE FROM quick_replies WHERE id > 4"); err != nil {
		t.Fatal(err)
	}
	status, body := h.admin(t, "GET", "/internal/quick-replies", "")
	if status != http.StatusOK || strings.Count(string(body), `"id"`) != 4 || !strings.Contains(string(body), "{nama}") {
		t.Fatalf("seeded: %d %s", status, body)
	}
	status, body = h.admin(t, "POST", "/internal/quick-replies", `{"body":"  Sudah kami kirim ya Kak\r\nResi menyusul  "}`)
	var added QuickReply
	if status != http.StatusCreated || json.Unmarshal(body, &added) != nil || added.Body != "Sudah kami kirim ya Kak\nResi menyusul" {
		t.Fatalf("add: %d %s", status, body)
	}
	path := "/internal/quick-replies/" + strconv.FormatInt(added.ID, 10)
	if status, body := h.admin(t, "PUT", path, `{"body":"Sudah dikirim"}`); status != http.StatusOK || !strings.Contains(string(body), `"body":"Sudah dikirim"`) {
		t.Fatalf("update: %d %s", status, body)
	}
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/internal/quick-replies", `{"body":"  "}`, http.StatusBadRequest},
		{"POST", "/internal/quick-replies", `{"body":"` + strings.Repeat("x", 501) + `"}`, http.StatusBadRequest},
		{"PUT", "/internal/quick-replies/9999", `{"body":"x"}`, http.StatusNotFound},
		{"PUT", "/internal/quick-replies/abc", `{"body":"x"}`, http.StatusNotFound},
		{"DELETE", "/internal/quick-replies/9999", "", http.StatusNotFound},
	} {
		if status, body := h.admin(t, c.method, c.path, c.body); status != c.status {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, status, body, c.status)
		}
	}
	// Customers can't reach them.
	if status, _ := h.customer(t, 7, "GET", "/internal/quick-replies", ""); status != http.StatusUnauthorized {
		t.Errorf("customer: %d", status)
	}
	if status, _ := h.admin(t, "DELETE", path, ""); status != http.StatusNoContent {
		t.Errorf("delete: %d", status)
	}
	// The limit.
	for i := 4; i < maxQuickReplies; i++ {
		if status, body := h.admin(t, "POST", "/internal/quick-replies", `{"body":"isi"}`); status != http.StatusCreated {
			t.Fatalf("fill %d: %d %s", i, status, body)
		}
	}
	if status, _ := h.admin(t, "POST", "/internal/quick-replies", `{"body":"satu lagi"}`); status != http.StatusConflict {
		t.Errorf("over the limit: %d", status)
	}
	if _, err := h.pool.Exec(context.Background(), "DELETE FROM quick_replies WHERE id > 4"); err != nil {
		t.Fatal(err)
	}
}
