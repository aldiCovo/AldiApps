package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClaude answers the Messages API from a script, one answer per request, and keeps what
// it was asked.
type fakeClaude struct {
	mu       sync.Mutex
	answers  []string
	requests []string
}

func (f *fakeClaude) script(answers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, answers...)
}

func (f *fakeClaude) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, string(body))
	if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer test-key" || len(f.answers) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"not scripted"}}`)
		return
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, answer)
}

func claudeText(text string) string {
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	return claudeMessage(string(content), "end_turn")
}

func claudeTool(id, name, input string) string {
	return claudeMessage(fmt.Sprintf(`[{"type":"tool_use","id":%q,"name":%q,"input":%s}]`, id, name, input), "tool_use")
}

func claudeMessage(content, stop string) string {
	return fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5","content":%s,"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":5}}`, content, stop)
}

// fakeTelegram keeps the notes the admins would get.
type fakeTelegram struct {
	mu    sync.Mutex
	notes []map[string]any
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var note map[string]any
	json.NewDecoder(r.Body).Decode(&note)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/botbot-token/sendMessage" || note["chat_id"] != "-100" {
		http.Error(w, "wrong", http.StatusBadRequest)
		return
	}
	f.notes = append(f.notes, note)
	fmt.Fprint(w, `{"ok":true}`)
}

func (f *fakeTelegram) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.notes...)
}

// summary finds a customer in the admins' list.
func (h *harness) summary(t *testing.T, filter string, userID int64) (Summary, bool) {
	t.Helper()
	list := readList(t)(h.admin(t, "GET", "/internal/conversations?filter="+filter, ""))
	for _, s := range list.Conversations {
		if s.UserID == userID {
			return s, true
		}
	}
	return Summary{}, false
}

func TestBotAnswers(t *testing.T) {
	h := newHarnessWith(t, true)
	const sari = 7

	// Two messages in a row get one answer, which looks the order up and shows its card.
	h.claude.script(
		claudeTool("t1", "get_order", `{"order_id":9}`),
		claudeTool("t2", "attach_order", `{"order_id":9}`),
		claudeText("Pesanan #9 sudah dikirim dengan resi JNE123, Kak 🙏"),
	)
	h.customer(t, sari, "POST", "/chat/messages", `{"body":"Halo kak"}`)
	h.customer(t, sari, "POST", "/chat/messages", `{"body":"Pesanan 9 sampai mana?"}`)
	h.bot.Wait()

	page := readPage(t)(h.customer(t, sari, "GET", "/chat", ""))
	if bodies(page) != "c:Halo kak,c:Pesanan 9 sampai mana?,b:Pesanan #9 sudah dikirim dengan resi JNE123, Kak 🙏" {
		t.Fatalf("page: %s", bodies(page))
	}
	if card := page.Messages[2].Attachment; card == nil || card.Kind != "order" || card.ID != 9 {
		t.Fatalf("card: %+v", page.Messages[2])
	}
	if page.NeedsAdmin || page.BotWorking || !page.BotOn || page.Unread != 1 {
		t.Fatalf("state: %+v", page)
	}
	asked := h.claude.asked()
	if len(asked) != 3 {
		t.Fatalf("claude asked %d times", len(asked))
	}
	var first struct {
		Model    string `json:"model"`
		System   []struct{ Text string }
		Messages []struct {
			Role    string
			Content []struct{ Text string }
		}
		Tools []struct{ Name string }
	}
	if err := json.Unmarshal([]byte(asked[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Model != botModel || len(first.Tools) != len(botTools) || len(first.Messages) != 1 || len(first.Messages[0].Content) != 2 ||
		!strings.Contains(first.System[1].Text, "Nama pelanggan: Sari.") || !strings.Contains(first.System[0].Text, "Asisten Prastika") {
		t.Fatalf("first request: %s", asked[0])
	}
	// The order is read as the customer, without the street.
	if !strings.Contains(asked[1], `\"tracking_number\":\"JNE123\"`) || strings.Contains(asked[1], "Melati") || !strings.Contains(asked[1], `"tool_use_id":"t1"`) {
		t.Fatalf("tool result: %s", asked[1])
	}
	if len(h.telegram.sent()) != 0 {
		t.Fatalf("telegram without a handoff: %v", h.telegram.sent())
	}
	// The admins see it among the bot's, not among those waiting.
	if _, ok := h.summary(t, filterBot, sari); !ok {
		t.Fatal("not in the bot's list")
	}
	if _, ok := h.summary(t, filterWaiting, sari); ok {
		t.Fatal("waiting without a handoff")
	}
	// The bot reading isn't an admin reading.
	if page := readPage(t)(h.admin(t, "GET", "/internal/conversations/7/messages", "")); page.Unread != 2 {
		t.Fatalf("admin unread: %d", page.Unread)
	}
}

func TestBotHandsOver(t *testing.T) {
	h := newHarnessWith(t, true)
	const sari = 7

	h.claude.script(
		claudeTool("t1", "handoff_to_admin", `{"reason":"ganti alamat pesanan #9"}`),
		claudeText("Ganti alamat harus dibantu admin, Kak. Chat ini sudah saya teruskan 🙏"),
	)
	h.customer(t, sari, "POST", "/chat/messages", `{"body":"Alamatnya salah, ganti ke Jl. Mawar 2","attach":{"kind":"order","id":9}}`)
	h.bot.Wait()
	page := readPage(t)(h.customer(t, sari, "GET", "/chat", ""))
	if !page.NeedsAdmin || page.BotOn || len(page.Messages) != 2 || page.Messages[1].From != "bot" {
		t.Fatalf("page: %+v", page)
	}
	summary, ok := h.summary(t, filterWaiting, sari)
	if !ok || summary.Reason != "ganti alamat pesanan #9" || !summary.Waiting {
		t.Fatalf("waiting: %+v", summary)
	}
	notes := h.telegram.sent()
	if len(notes) != 1 {
		t.Fatalf("notes: %v", notes)
	}
	text := notes[0]["text"].(string)
	for _, want := range []string{"Perlu admin: Sari Maharani", `"Alamatnya salah, ganti ke Jl. Mawar 2"`, "📎 Pesanan #9 · Dikirim · Rp 319.000", "Alasan: ganti alamat pesanan #9", "https://toko.test/admin/#chat-7"} {
		if !strings.Contains(text, want) {
			t.Errorf("note lacks %q: %s", want, text)
		}
	}

	// While it waits for an admin, the bot stays quiet and the admins hear nothing new.
	h.customer(t, sari, "POST", "/chat/messages", `{"body":"Halo?"}`)
	h.bot.Wait()
	if len(h.claude.asked()) != 2 || len(h.telegram.sent()) != 1 {
		t.Fatalf("asked %d, notes %d", len(h.claude.asked()), len(h.telegram.sent()))
	}

	// An admin answers: settled, and the bot leaves the chat to people.
	h.admin(t, "POST", "/internal/conversations/7/messages", `{"body":"Sudah kami ganti, Kak","admin_id":16}`)
	page = readPage(t)(h.admin(t, "GET", "/internal/conversations/7/messages", ""))
	if page.NeedsAdmin || !page.BotOff || page.BotOn {
		t.Fatalf("after admin: %+v", page)
	}
	h.customer(t, sari, "POST", "/chat/messages", `{"body":"Terima kasih"}`)
	h.bot.Wait()
	if summary, _ := h.summary(t, filterWaiting, sari); summary.Reason != reasonTakenOver || len(h.claude.asked()) != 2 || len(h.telegram.sent()) != 2 {
		t.Fatalf("taken over: %+v, asked %d", summary, len(h.claude.asked()))
	}

	// Given back to the bot, it answers again; when Claude fails, the customer hears so and
	// the admins take it.
	if status, _ := h.admin(t, "POST", "/internal/conversations/7/bot", `{"on":true}`); status != http.StatusNoContent {
		t.Fatalf("back to bot: %d", status)
	}
	if _, ok := h.summary(t, filterWaiting, sari); ok {
		t.Fatal("still waiting after going back to the bot")
	}
	h.customer(t, sari, "POST", "/chat/messages", `{"body":"Ada dress hitam?"}`)
	h.bot.Wait()
	page = readPage(t)(h.customer(t, sari, "GET", "/chat", ""))
	if last := page.Messages[len(page.Messages)-1]; last.From != "bot" || last.Body != botFailed || !page.NeedsAdmin {
		t.Fatalf("failure: %+v", page)
	}
	if summary, _ := h.summary(t, filterWaiting, sari); summary.Reason != reasonBotFailed {
		t.Fatalf("failure reason: %+v", summary)
	}

	// Taking it over by hand.
	if status, _ := h.admin(t, "POST", "/internal/conversations/7/bot", `{"on":false}`); status != http.StatusNoContent {
		t.Fatalf("take over: %d", status)
	}
	if summary, _ := h.summary(t, filterAll, sari); !summary.BotOff {
		t.Fatalf("not taken over: %+v", summary)
	}
	for _, c := range []struct{ path, body string }{
		{"/internal/conversations/99/bot", `{"on":true}`},
		{"/internal/conversations/7/bot", `{}`},
	} {
		if status, _ := h.admin(t, "POST", c.path, c.body); status != http.StatusNotFound && status != http.StatusBadRequest {
			t.Errorf("%s %s: %d", c.path, c.body, status)
		}
	}
}

func TestCustomerAsksForAdmin(t *testing.T) {
	h := newHarnessWith(t, true)
	// Someone who never wrote can still ask for a person.
	for range 2 {
		if status, body := h.customer(t, 8, "POST", "/chat/handoff", ""); status != http.StatusNoContent {
			t.Fatalf("handoff: %d %s", status, body)
		}
	}
	h.bot.Wait()
	page := readPage(t)(h.customer(t, 8, "GET", "/chat", ""))
	if bodies(page) != "b:"+botAskedAdmin || !page.NeedsAdmin {
		t.Fatalf("page: %s %+v", bodies(page), page)
	}
	if summary, ok := h.summary(t, filterWaiting, 8); !ok || summary.Reason != reasonCustomer {
		t.Fatalf("summary: %+v", summary)
	}
	if notes := h.telegram.sent(); len(notes) != 1 || !strings.Contains(notes[0]["text"].(string), "Alasan: "+reasonCustomer) {
		t.Fatalf("notes: %v", notes)
	}
	if status, _ := h.customer(t, 0, "POST", "/chat/handoff", ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: %d", status)
	}
}

func TestBotSettings(t *testing.T) {
	h := newHarnessWith(t, true)
	status, body := h.admin(t, "GET", "/internal/bot-settings", "")
	var view botSettingsView
	if status != http.StatusOK || json.Unmarshal(body, &view) != nil || view.Mode != botAlways || !view.Configured || !view.TelegramConfigured || !view.AnsweringNow {
		t.Fatalf("settings: %d %s", status, body)
	}
	status, _ = h.admin(t, "PUT", "/internal/bot-settings", `{"mode":"off","store_info":"  Retur 3 hari\r\nUkuran M 96 cm  ","telegram":true,"quiet_outside_hours":false}`)
	if status != http.StatusNoContent {
		t.Fatalf("save: %d", status)
	}
	if _, body := h.admin(t, "GET", "/internal/bot-settings", ""); !strings.Contains(string(body), `"store_info":"Retur 3 hari\nUkuran M 96 cm"`) ||
		!strings.Contains(string(body), `"answering_now":false`) {
		t.Fatalf("saved: %s", body)
	}
	for _, bad := range []string{`{"mode":"kadang"}`, `{"mode":"off","store_info":"` + strings.Repeat("x", 4001) + `"}`, `{"mode":"off","extra":1}`} {
		if status, _ := h.admin(t, "PUT", "/internal/bot-settings", bad); status != http.StatusBadRequest {
			t.Errorf("%.30s: %d", bad, status)
		}
	}
	if status, _ := h.customer(t, 7, "GET", "/internal/bot-settings", ""); status != http.StatusUnauthorized {
		t.Errorf("customer: %d", status)
	}

	// Turned off, every message goes straight to the admins, loudly in working hours or not.
	h.customer(t, 9, "POST", "/chat/messages", `{"body":"Halo"}`)
	h.bot.Wait()
	if summary, _ := h.summary(t, filterWaiting, 9); summary.Reason != reasonBotOff || len(h.claude.asked()) != 0 {
		t.Fatalf("off: %+v", summary)
	}
	if notes := h.telegram.sent(); len(notes) != 1 || notes[0]["disable_notification"] != false {
		t.Fatalf("notes: %v", notes)
	}
	if page := readPage(t)(h.customer(t, 9, "GET", "/chat", "")); page.BotOn {
		t.Fatal("bot on while off")
	}

	// The month's counts.
	h.customer(t, 8, "POST", "/chat/handoff", "")
	_, body = h.admin(t, "GET", "/internal/bot-settings", "")
	if !strings.Contains(string(body), `"month_replies":1`) || !strings.Contains(string(body), `"month_handoffs":2`) {
		t.Fatalf("counts: %s", body)
	}
}

func TestBotDailyLimit(t *testing.T) {
	h := newHarnessWith(t, true)
	h.customer(t, 7, "POST", "/chat/messages", `{"body":"Halo"}`)
	h.bot.Wait() // not scripted, so it fails and hands over
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, "UPDATE conversations SET needs_admin = false"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx,
		"INSERT INTO messages (user_id, admin_id, body) SELECT 7, 0, 'jawaban' FROM generate_series(1, $1)", maxBotRepliesPerDay); err != nil {
		t.Fatal(err)
	}
	asked := len(h.claude.asked())
	h.customer(t, 7, "POST", "/chat/messages", `{"body":"Satu lagi"}`)
	h.bot.Wait()
	page := readPage(t)(h.customer(t, 7, "GET", "/chat", ""))
	if last := page.Messages[len(page.Messages)-1]; last.Body != botTooBusyToday || !page.NeedsAdmin || len(h.claude.asked()) != asked {
		t.Fatalf("limit: %+v", page.Messages[len(page.Messages)-1])
	}
}

func TestWorkingHours(t *testing.T) {
	for _, c := range []struct {
		at   string
		want bool
	}{
		{"2026-11-12T08:00:00+07:00", true},  // Thursday
		{"2026-11-12T07:59:00+07:00", false}, //
		{"2026-11-12T20:00:00+07:00", false}, //
		{"2026-11-14T12:00:00+07:00", true},  // Saturday
		{"2026-11-15T12:00:00+07:00", false}, // Sunday
		{"2026-11-12T05:00:00Z", true},       // 12.00 WIB
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		if got := duringWorkingHours(at); got != c.want {
			t.Errorf("%s: %v", c.at, got)
		}
	}
	if rupiah(319000) != "Rp 319.000" || rupiah(1250000) != "Rp 1.250.000" || rupiah(500) != "Rp 500" {
		t.Error(rupiah(319000), rupiah(1250000))
	}
}
