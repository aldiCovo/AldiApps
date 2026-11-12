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
)

// fakeGemini answers generateContent from a script, one answer per request; an empty answer
// is the free quota used up. It keeps what it was asked.
type fakeGemini struct {
	mu       sync.Mutex
	answers  []string
	requests []string
}

func (f *fakeGemini) script(answers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, answers...)
}

func (f *fakeGemini) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeGemini) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, string(body))
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/v1beta/models/gemini-3.5-flash-lite:generateContent" || r.Header.Get("x-goog-api-key") != "gemini-key" || len(f.answers) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":400,"message":"not scripted"}}`)
		return
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	if answer == "" {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`)
		return
	}
	fmt.Fprint(w, answer)
}

func geminiAnswer(parts string) string {
	return `{"candidates":[{"content":{"role":"model","parts":` + parts + `},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":20}}`
}

func setAI(t *testing.T, h *harness, ai string) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), "UPDATE bot_settings SET ai = $1", ai); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiAnswers(t *testing.T) {
	h := newHarnessWith(t, true)
	setAI(t, h, aiGemini)
	const sari = 7

	h.gemini.script(
		geminiAnswer(`[{"functionCall":{"id":"c1","name":"get_order","args":{"order_id":9}},"thoughtSignature":"sig-1"}]`),
		geminiAnswer(`[{"text":"memikirkan","thought":true},{"text":"Pesanan #9 sudah dikirim, resinya JNE123 ya Kak."}]`),
	)
	h.customer(t, sari, "POST", "/chat/messages", `{"body":"Pesanan 9 sampai mana?"}`)
	h.bot.Wait()

	page := readPage(t)(h.customer(t, sari, "GET", "/chat", ""))
	if bodies(page) != "c:Pesanan 9 sampai mana?,b:Pesanan #9 sudah dikirim, resinya JNE123 ya Kak." || page.NeedsAdmin {
		t.Fatalf("page: %s %+v", bodies(page), page)
	}
	asked := h.gemini.asked()
	if len(asked) != 2 || len(h.claude.asked()) != 0 {
		t.Fatalf("gemini asked %d, claude %d", len(asked), len(h.claude.asked()))
	}
	var first struct {
		SystemInstruction struct{ Parts []struct{ Text string } }
		Contents          []struct {
			Role  string
			Parts []struct{ Text string }
		}
		Tools []struct {
			FunctionDeclarations []struct {
				Name       string
				Parameters *struct{ Type string }
			}
		}
	}
	if err := json.Unmarshal([]byte(asked[0]), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Contents) != 1 || first.Contents[0].Role != "user" || len(first.Tools) != 1 ||
		len(first.Tools[0].FunctionDeclarations) != len(botTools) || len(first.SystemInstruction.Parts) != 2 ||
		!strings.Contains(first.SystemInstruction.Parts[0].Text, "Asisten Prastika") {
		t.Fatalf("first request: %s", asked[0])
	}
	// Tools without inputs have no parameters, as Gemini wants.
	noInput := map[string]bool{"my_orders": true, "shipping_options": true, "my_vouchers": true}
	for _, d := range first.Tools[0].FunctionDeclarations {
		if noInput[d.Name] != (d.Parameters == nil) {
			t.Errorf("%s parameters: %+v", d.Name, d.Parameters)
		}
	}
	// No names and no street go to the free tier.
	for _, request := range asked {
		if strings.Contains(request, "Sari") || strings.Contains(request, "Melati") {
			t.Fatalf("private data sent: %s", request)
		}
	}
	// The model's turn goes back as it came, with its thought signature, then the result.
	if !strings.Contains(asked[1], `"thoughtSignature":"sig-1"`) || !strings.Contains(asked[1], `"functionResponse":{"id":"c1","name":"get_order","response":{"result":{`) ||
		!strings.Contains(asked[1], `"tracking_number":"JNE123"`) {
		t.Fatalf("second request: %s", asked[1])
	}
	if _, body := h.admin(t, "GET", "/internal/bot-settings", ""); !strings.Contains(string(body), `"today_ai":1`) || !strings.Contains(string(body), `"month_ai":1`) {
		t.Fatalf("counts: %s", body)
	}
}

func TestNamedProductGetsCard(t *testing.T) {
	h := newHarnessWith(t, true)
	setAI(t, h, aiGemini)

	// The AI looks a product up and names it without attaching it: the card comes anyway.
	h.gemini.script(
		geminiAnswer(`[{"functionCall":{"id":"c1","name":"get_product","args":{"product_id":5}}}]`),
		geminiAnswer(`[{"text":"Ada Kak, dress linen midi Rp 289.000, stok M 3. Silakan dicek ya."}]`),
	)
	h.customer(t, 7, "POST", "/chat/messages", `{"body":"Ada dress linen?"}`)
	h.bot.Wait()
	page := readPage(t)(h.customer(t, 7, "GET", "/chat", ""))
	last := page.Messages[len(page.Messages)-1]
	if last.From != "bot" || last.Attachment == nil || last.Attachment.ID != 5 || !strings.HasPrefix(last.Body, "Ada Kak") {
		t.Fatalf("answer: %+v", last)
	}

	// A product it read but didn't name gets no card.
	h.gemini.script(
		geminiAnswer(`[{"functionCall":{"id":"c2","name":"get_product","args":{"product_id":5}}}]`),
		geminiAnswer(`[{"text":"Maaf Kak, yang warna sage belum ada."}]`),
	)
	h.customer(t, 7, "POST", "/chat/messages", `{"body":"Yang sage ada?"}`)
	h.bot.Wait()
	page = readPage(t)(h.customer(t, 7, "GET", "/chat", ""))
	if last := page.Messages[len(page.Messages)-1]; last.Attachment != nil {
		t.Fatalf("card without a name: %+v", last)
	}
}

func TestAIFallsBackToAdmins(t *testing.T) {
	h := newHarnessWith(t, true)
	setAI(t, h, aiGemini)

	// The free quota is used up: the customer hears that an admin will answer.
	h.gemini.script("")
	h.customer(t, 7, "POST", "/chat/messages", `{"body":"Bisa grosir?"}`)
	h.bot.Wait()
	page := readPage(t)(h.customer(t, 7, "GET", "/chat", ""))
	if bodies(page) != "c:Bisa grosir?,b:"+botForwarded || !page.NeedsAdmin || !page.MenuOn || page.BotOn {
		t.Fatalf("quota: %s %+v", bodies(page), page)
	}
	if summary, _ := h.summary(t, filterWaiting, 7); summary.Reason != reasonQuota {
		t.Fatalf("quota reason: %+v", summary)
	}

	// Without an AI, only the menu answers; other messages go to the admins at once.
	setAI(t, h, aiNone)
	h.customer(t, 8, "POST", "/chat/messages", `{"body":"Halo"}`)
	h.bot.Wait()
	if page := readPage(t)(h.customer(t, 8, "GET", "/chat", "")); bodies(page) != "c:Halo,b:"+botForwarded {
		t.Fatalf("no AI: %s", bodies(page))
	}
	if summary, _ := h.summary(t, filterWaiting, 8); summary.Reason != reasonNoAI {
		t.Fatalf("no AI reason: %+v", summary)
	}
	if len(h.gemini.asked()) != 1 || len(h.claude.asked()) != 0 || len(h.telegram.sent()) != 2 {
		t.Fatalf("asked gemini %d, claude %d, notes %d", len(h.gemini.asked()), len(h.claude.asked()), len(h.telegram.sent()))
	}
}

func TestMenu(t *testing.T) {
	h := newHarnessWith(t, true)
	const sari, budi = 7, 8
	menu := func(userID int64, body string) (int, string) {
		t.Helper()
		status, out := h.customer(t, userID, "POST", "/chat/menu", body)
		return status, string(out)
	}
	last := func(userID int64, n int) []Message {
		t.Helper()
		page := readPage(t)(h.customer(t, userID, "GET", "/chat", ""))
		if len(page.Messages) < n {
			t.Fatalf("only %d messages", len(page.Messages))
		}
		return page.Messages[len(page.Messages)-n:]
	}

	// Orders: the question, the tracking numbers, and a card per order.
	if status, out := menu(sari, `{"action":"orders"}`); status != http.StatusNoContent {
		t.Fatalf("orders: %d %s", status, out)
	}
	got := last(sari, 4)
	if got[0].From != "customer" || got[0].Body != "Status pesanan saya" ||
		!strings.Contains(got[1].Body, "Ini 2 pesanan terbaru") || !strings.Contains(got[1].Body, "Resi pesanan #9 (JNE Reguler): JNE123") ||
		got[2].Attachment == nil || got[2].Attachment.ID != 9 || got[2].Attachment.More != 1 || got[3].Attachment.ID != 4 || got[3].From != "bot" {
		t.Fatalf("orders: %+v", got)
	}
	menu(budi, `{"action":"orders"}`)
	if got := last(budi, 1); !strings.Contains(got[0].Body, "belum punya pesanan") {
		t.Fatalf("no orders: %+v", got)
	}

	// Stock: the product card on the question, the stock per size in the answer.
	if status, out := menu(sari, `{"action":"stock","product_id":5}`); status != http.StatusNoContent {
		t.Fatalf("stock: %d %s", status, out)
	}
	got = last(sari, 2)
	if got[0].Attachment == nil || got[0].Attachment.ID != 5 || got[0].From != "customer" ||
		got[1].Body != "Stok Dress Linen Midi · Rp 289.000 (dari Rp 329.000):\nM · Hitam: 3\nL · Hitam: habis" {
		t.Fatalf("stock: %+v", got)
	}
	for _, bad := range []string{`{"action":"stock"}`, `{"action":"tanya"}`, `{"action":"admin","reason":"lain"}`, `{"action":"shipping","province":"Mars"}`} {
		if status, _ := menu(sari, bad); status != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, status)
		}
	}
	if status, _ := menu(sari, `{"action":"stock","product_id":77}`); status != http.StatusNotFound {
		t.Errorf("missing product: %d", status)
	}

	// Shipping: to the saved address, or to a province the customer picks.
	menu(sari, `{"action":"shipping"}`)
	got = last(sari, 2)
	if got[0].Body != "Ongkir ke kotaku" || got[1].Body != "Ongkir ke Jawa Barat (alamat utama Kakak), yang termurah:\n"+
		"J&T Reguler · 2–3 hari · Rp 9.000\nJNE Reguler · 2–3 hari · Rp 10.000\nSiCepat Express · 1 hari · Rp 18.000\n"+
		"Satu ongkir untuk satu pesanan, berapa pun barangnya." {
		t.Fatalf("shipping: %q", got[1].Body)
	}
	before := len(readPage(t)(h.customer(t, budi, "GET", "/chat", "")).Messages)
	if status, out := menu(budi, `{"action":"shipping"}`); status != http.StatusOK || strings.TrimSpace(out) != `{"need":"province"}` {
		t.Fatalf("no address: %d %s", status, out)
	}
	if after := len(readPage(t)(h.customer(t, budi, "GET", "/chat", "")).Messages); after != before {
		t.Fatal("posted without a province")
	}
	menu(budi, `{"action":"shipping","province":"Papua"}`)
	if got := last(budi, 2); got[0].Body != "Ongkir ke Papua" || !strings.HasPrefix(got[1].Body, "Ongkir ke Papua, yang termurah:\nJ&T Reguler · 5–8 hari · Rp 44.000") {
		t.Fatalf("province: %+v", got)
	}

	// Vouchers: only usable ones.
	menu(sari, `{"action":"vouchers"}`)
	if got := last(sari, 1); got[0].Body != "Voucher yang bisa Kakak pakai sekarang:\nHEMAT10: potongan 10% (maks. Rp 50.000), min. belanja Rp 200.000\n"+
		"ONGKIR: potongan Rp 15.000\nMasukkan kodenya di halaman checkout." {
		t.Fatalf("vouchers: %q", got[0].Body)
	}

	// Returns: the store's info, or the admins when there is none.
	if _, err := h.pool.Exec(context.Background(), "UPDATE bot_settings SET store_info = 'Tukar ukuran dalam 3 hari.'"); err != nil {
		t.Fatal(err)
	}
	menu(sari, `{"action":"returns"}`)
	if got := last(sari, 2); got[0].Body != "Cara retur & tukar" || got[1].Body != "Tukar ukuran dalam 3 hari." {
		t.Fatalf("returns: %+v", got)
	}
	if page := readPage(t)(h.customer(t, sari, "GET", "/chat", "")); page.NeedsAdmin {
		t.Fatal("the menu handed over")
	}

	// The buttons under the orders go to the admins with the reason.
	menu(sari, `{"action":"admin","reason":"change_order"}`)
	if got := last(sari, 2); got[0].Body != "Ubah alamat / batal pesanan" || got[1].Body != botAskedAdmin {
		t.Fatalf("admin: %+v", got)
	}
	if summary, _ := h.summary(t, filterWaiting, sari); summary.Reason != "Ubah alamat / batal pesanan" {
		t.Fatalf("admin reason: %+v", summary)
	}
	h.bot.Wait()
	if notes := h.telegram.sent(); len(notes) != 1 || !strings.Contains(notes[0]["text"].(string), "Alasan: Ubah alamat / batal pesanan") {
		t.Fatalf("notes: %v", notes)
	}
	// Waiting for an admin, the menu still answers.
	if status, _ := menu(sari, `{"action":"orders"}`); status != http.StatusNoContent {
		t.Fatalf("menu while waiting: %d", status)
	}

	// The menu never asks an AI, and the settings page counts it.
	h.bot.Wait()
	if len(h.claude.asked()) != 0 || len(h.gemini.asked()) != 0 {
		t.Fatal("the menu asked an AI")
	}
	if _, body := h.admin(t, "GET", "/internal/bot-settings", ""); !strings.Contains(string(body), `"month_menu":9`) {
		t.Fatalf("counts: %s", body)
	}

	// Off, or taken over by an admin, the menu is gone.
	h.admin(t, "POST", "/internal/conversations/7/bot", `{"on":false}`)
	if status, _ := menu(sari, `{"action":"orders"}`); status != http.StatusConflict {
		t.Fatalf("taken over: %d", status)
	}
	if page := readPage(t)(h.customer(t, sari, "GET", "/chat", "")); page.MenuOn {
		t.Fatal("menu on while taken over")
	}
	if _, err := h.pool.Exec(context.Background(), "UPDATE bot_settings SET mode = 'off'"); err != nil {
		t.Fatal(err)
	}
	if status, _ := menu(budi, `{"action":"orders"}`); status != http.StatusConflict {
		t.Fatalf("off: %d", status)
	}
	if status, _ := menu(0, `{"action":"orders"}`); status != http.StatusUnauthorized {
		t.Fatalf("no token: %d", status)
	}
}
