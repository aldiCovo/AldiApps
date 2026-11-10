package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionCookie(t *testing.T) {
	u := &webUI{key: []byte("k"), now: time.Now}
	req := func(value string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
		return r
	}
	good := u.sessionValue(time.Now().Add(time.Hour))
	if !u.loggedIn(req(good)) {
		t.Error("a fresh session isn't accepted")
	}
	if u.loggedIn(req(u.sessionValue(time.Now().Add(-time.Minute)))) {
		t.Error("an expired session is accepted")
	}
	exp, sig, _ := strings.Cut(good, ".")
	if u.loggedIn(req(exp+"9."+sig)) || u.loggedIn(req("x")) || u.loggedIn(req("")) {
		t.Error("a changed session is accepted")
	}
	other := &webUI{key: []byte("other"), now: time.Now}
	if other.loggedIn(req(good)) {
		t.Error("a session from another password is accepted")
	}
}

// The pages after a few days of the job: login, every page, the chart data, and no way in
// without the password.
func TestPages(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE paper_trades, momentum_months, runs, broker_summaries CASCADE"); err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 11, 23, 0, 0, 0, 0, wib)
	yahoo := fakeYahoo(t, &last)
	t.Cleanup(yahoo.Close)
	clock := last.Add(17 * time.Hour)
	st := &store{pool: pool}
	j := &job{store: st, yahoo: &yahooClient{http: yahoo.Client(), base: yahoo.URL}, telegram: &fakeTelegram{},
		tickers: []string{"BBCA", "BBRI", "TLKM", "ASII", "ANTM"}, now: func() time.Time { return clock }}
	for d := last; d.Before(time.Date(2026, 12, 5, 0, 0, 0, 0, wib)); d = d.AddDate(0, 0, 1) {
		if !isWeekday(d) {
			continue
		}
		last, clock = d, d.Add(17*time.Hour)
		if _, err := j.Run(ctx, false); err != nil {
			t.Fatal(err)
		}
	}

	u, err := newWebUI(j, st, "rahasia", 17*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u.now = func() time.Time { return clock }
	h := u.handler()
	do := func(method, path, cookie string, form url.Values) *httptest.ResponseRecorder {
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		r := httptest.NewRequest(method, path, body)
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do("GET", "/paper", "", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("without login /paper gave %d to %q", w.Code, w.Header().Get("Location"))
	}
	if w := do("GET", "/data/BBCA", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("without login /data gave %d", w.Code)
	}
	if w := do("POST", "/login", "", url.Values{"password": {"salah"}}); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Password salah") {
		t.Errorf("a wrong password gave %d", w.Code)
	}
	w := do("POST", "/login", "", url.Values{"password": {"rahasia"}})
	var session string
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.HttpOnly && c.Secure {
			session = c.Value
		}
	}
	if w.Code != http.StatusSeeOther || session == "" {
		t.Fatalf("login gave %d and no session cookie", w.Code)
	}

	for path, want := range map[string]string{
		"/":                 "Pasar 4 Des 2026",
		"/saham":            "BBRI",
		"/saham?urut=rsi":   "RSI(14)",
		"/saham/bbca":       "Paper trading BBCA",
		"/paper":            "Saat IHSG di atas MA200",
		"/paper?acak=1":     "Sembunyikan acak",
		"/momentum":         "Peringkat sekarang",
		"/rekomendasi":      "Dengan data broker",
		"/saham/BBRI":       "Analisa gabungan",
		"/static/style.css": "--accent",
		"/static/app.js":    "LightweightCharts",
	} {
		w := do("GET", path, session, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d, want %q in:\n%.2000s", path, w.Code, want, w.Body.String())
		}
	}
	if w := do("GET", "/saham/XXXX", session, nil); w.Code != http.StatusNotFound {
		t.Errorf("an unknown stock gave %d", w.Code)
	}
	for _, path := range []string{"/data/IHSG", "/data/BBCA"} {
		w := do("GET", path, session, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"candles":[{"time":"`) {
			t.Errorf("%s: %d %.300s", path, w.Code, w.Body.String())
		}
	}
	// Paste a broker summary, see it, find it on the stock's page, and delete it.
	fixture, err := os.ReadFile("testdata/broker_summary.txt")
	if err != nil {
		t.Fatal(err)
	}
	pasted := strings.Replace(string(fixture), "TEST", "BBCA", 1)
	if w := do("POST", "/broker", session, url.Values{"text": {"tidak ada apa-apa"}}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Teks tidak terbaca") {
		t.Errorf("a bad paste gave %d", w.Code)
	}
	w = do("POST", "/broker", session, url.Values{"text": {pasted}, "investor": {"all"}, "market": {"regular"}, "mode": {"net"}})
	detail := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(detail, "/broker/") {
		t.Fatalf("paste gave %d to %q: %.500s", w.Code, detail, w.Body.String())
	}
	// The same stock, period and filter again replaces it rather than adding a second.
	if w := do("POST", "/broker", session, url.Values{"text": {pasted}}); w.Header().Get("Location") != detail {
		t.Errorf("a repeated paste went to %q, want %q", w.Header().Get("Location"), detail)
	}
	// A different paste for the same stock, period and filter asks first, and replaces only
	// with the old one's id confirmed.
	changed := strings.Replace(pasted, "ZP\n\n120B", "ZP\n\n121B", 1)
	if changed == pasted {
		t.Fatal("the fixture didn't change")
	}
	id := strings.TrimPrefix(detail, "/broker/")
	if w := do("POST", "/broker", session, url.Values{"text": {changed}}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Ya, timpa") {
		t.Errorf("a different paste gave %d, want a question: %.800s", w.Code, w.Body.String())
	}
	if w := do("POST", "/broker", session, url.Values{"text": {changed}, "replace": {"999999"}}); w.Code != http.StatusConflict {
		t.Errorf("confirming another summary gave %d", w.Code)
	}
	if w := do("POST", "/broker", session, url.Values{"text": {changed}, "investor": {"foreign"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") == detail {
		t.Errorf("the same text as Asing gave %d to %q, want a new summary", w.Code, w.Header().Get("Location"))
	}
	if w := do("POST", "/broker", session, url.Values{"text": {changed}, "replace": {id}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != detail {
		t.Errorf("a confirmed replace gave %d to %q", w.Code, w.Header().Get("Location"))
	}
	for path, want := range map[string]string{
		detail:          "Net broker asing",
		"/broker":       "BBCA",
		"/saham/BBCA":   "Broker summary BBCA",
		"/broker/asing": "ZP",
	} {
		w := do("GET", path, session, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d, want %q in:\n%.1500s", path, w.Code, want, w.Body.String())
		}
	}
	if w := do("GET", detail, session, nil); !strings.Contains(w.Body.String(), "−") {
		t.Errorf("detail lacks a negative foreign net")
	}
	r := httptest.NewRequest("POST", "/broker/asing", strings.NewReader("codes=zp+KZ"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a post from another site gave %d", rec.Code)
	}
	if w := do("POST", "/broker/asing", session, url.Values{"codes": {"zp, kz YU"}}); w.Code != http.StatusSeeOther {
		t.Errorf("saving foreign brokers gave %d", w.Code)
	}
	if w := do("GET", "/broker/asing", session, nil); !strings.Contains(w.Body.String(), "KZ YU ZP") {
		t.Errorf("foreign brokers not saved: %.800s", w.Body.String())
	}
	if w := do("POST", detail+"/hapus", session, url.Values{}); w.Code != http.StatusSeeOther {
		t.Errorf("delete gave %d", w.Code)
	}
	if w := do("GET", detail, session, nil); w.Code != http.StatusNotFound {
		t.Errorf("after delete the detail gave %d", w.Code)
	}

	// Several foreign summaries pasted at once, then the recommendation sent again.
	before := strings.NewReplacer("01 Nov 26", "13 Nov 26", "09 Nov 26", "26 Nov 26").Replace(pasted)
	week := strings.NewReplacer("01 Nov 26", "27 Nov 26", "09 Nov 26", "04 Dec 26").Replace(pasted)
	if w := do("POST", "/broker", session, url.Values{"text": {before + "\n" + week}, "investor": {"foreign"}, "market": {"regular"}, "mode": {"net"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/rekomendasi?tersimpan=2#asing" {
		t.Errorf("two pastes gave %d to %q: %.1500s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if w := do("POST", "/broker", session, url.Values{"text": {pasted + "\nBroker Summary\nrusak"}, "investor": {"foreign"}}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Tempelan ke-2 dari 2") {
		t.Errorf("a bad second paste gave %d", w.Code)
	}
	w = do("GET", "/rekomendasi?tersimpan=2", session, nil)
	for _, want := range []string{"2 broker summary tersimpan", "Data broker asing", "tempelan 27 Nov 2026 – 4 Des 2026", "Kirim ringkasan ulang"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("/rekomendasi lacks %q", want)
		}
	}
	if w := do("GET", "/saham/BBCA", session, nil); !strings.Contains(w.Body.String(), "Net asing") {
		t.Errorf("the stock page lacks the flow table")
	}
	tg := j.telegram.(*fakeTelegram)
	sent := len(tg.sent)
	if w := do("POST", "/rekomendasi/kirim", session, url.Values{}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/rekomendasi?kirim=ok" {
		t.Errorf("resend gave %d to %q", w.Code, w.Header().Get("Location"))
	}
	if len(tg.sent) != sent+1 || !strings.Contains(tg.sent[sent], "Rekomendasi diperbarui 4 Des 2026") {
		t.Errorf("resend sent %d messages", len(tg.sent)-sent)
	} else {
		t.Logf("update:\n%s", tg.sent[sent])
	}

	if csp := do("GET", "/", session, nil).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP %q", csp)
	}
}
