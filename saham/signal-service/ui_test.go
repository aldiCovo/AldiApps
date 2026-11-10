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
	if _, err := pool.Exec(ctx, "TRUNCATE paper_trades, momentum_months, runs"); err != nil {
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
	if csp := do("GET", "/", session, nil).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP %q", csp)
	}
}
