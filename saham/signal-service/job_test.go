package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNextRun(t *testing.T) {
	at := 17 * time.Hour
	cases := []struct{ now, want string }{
		{"2026-11-10 09:00", "2026-11-10 17:00"}, // Tuesday morning: today
		{"2026-11-10 17:00", "2026-11-11 17:00"}, // exactly at the time: tomorrow
		{"2026-11-13 18:00", "2026-11-16 17:00"}, // Friday evening: Monday
		{"2026-11-14 10:00", "2026-11-16 17:00"}, // Saturday: Monday
	}
	for _, c := range cases {
		now, _ := time.ParseInLocation("2006-01-02 15:04", c.now, wib)
		if got := nextRun(now, at).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("nextRun(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[float64]string{7123.4: "7.123", 950: "950", 1234567: "1.234.567", 0: "0"} {
		if got := rp(in); got != want {
			t.Errorf("rp(%v) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[float64]string{0.0123: "+1,2%", -0.031: "−3,1%", -0.0001: "+0,0%"} {
		if got := pct(in); got != want {
			t.Errorf("pct(%v) = %s, want %s", in, got, want)
		}
	}
	if got := tanggal(time.Date(2026, 11, 10, 0, 0, 0, 0, wib)); got != "10 Nov 2026" {
		t.Errorf("tanggal = %s", got)
	}
}

func TestRandomDayIsAboutOneInTen(t *testing.T) {
	hits := 0
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, wib)
	for d := range 1000 {
		if randomDay("BBCA", start.AddDate(0, 0, d)) {
			hits++
		}
	}
	if hits != 100 {
		t.Errorf("BBCA picked on %d of 1000 days, want 100", hits)
	}
}

func TestSimulateTrailingStopRises(t *testing.T) {
	// A stock that rises 10 a day for 30 days, then falls hard.
	var bars []Bar
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, wib)
	for i := range 40 {
		price := 1000.0 + float64(i)*10
		if i >= 30 {
			price = 1290 - float64(i-29)*60
		}
		bars = append(bars, Bar{Date: day.AddDate(0, 0, i), Open: price, High: price + 5, Low: price - 5, Close: price, Volume: 1000})
	}
	s := analyze("TEST", bars)
	sig := Signal{Ticker: "TEST", Strategy: strategyBrkTrail, Close: 1000, MaxEntry: 1020, Stop: 950, Exit: exitTrail, TrailATR: 3, MaxDays: 40}
	o := simulate(s, 0, sig)
	if o.Status != statusStop {
		t.Fatalf("status %s, want stop", o.Status)
	}
	if o.ExitPrice <= 1100 {
		t.Errorf("sold at %v: the trailing stop should have risen well above the first stop", o.ExitPrice)
	}
	if o.Return <= 0 {
		t.Errorf("return %v, want a profit", o.Return)
	}
}

func TestBriefFitsTelegram(t *testing.T) {
	r := dayReport{
		Day:     time.Date(2026, 11, 11, 0, 0, 0, 0, wib),
		Started: time.Date(2026, 11, 11, 0, 0, 0, 0, wib),
		Market:  marketState{Close: 7123, Change1d: 0.01, Change20d: -0.03, MA50: 7200, MA200: 7400, Above200: 18, Counted: 45},
	}
	for i := range 40 {
		r.Screener = append(r.Screener, Signal{Ticker: fmt.Sprintf("T%02d", i), Strategy: strategyBrk,
			Reasons: []string{"Close 1000 > high", "Volume 2,1x"}, MaxEntry: 1020, Stop: 950, Target: 1100, Exit: exitTarget})
	}
	ok := true
	r.Stats = []ruleStats{{Rule: strategyBrk, Closed: 3, Wins: 1, MeanPct: -0.8, Open: 2}, {Rule: strategyBrk, MarketOK: &ok, Closed: 1, Wins: 1, MeanPct: 2.5}}
	for i := range 8 {
		r.Recommended = append(r.Recommended, recommendation{Ticker: fmt.Sprintf("R%02d", i), Score: 5, MaxEntry: 1020, Stop: 950, Target: 1100,
			Parts: []recPart{{Group: "Teknikal", Text: "Tren naik: harga di atas MA200 (Rp 9.000) dan MA50 di atas MA200", Points: 2},
				{Group: "Broker", Text: "Net investor asing +262,3 M, +6,1% dari nilai transaksi 3 Nov – 7 Nov 2026", Points: 2}}})
	}
	for i := range 7 {
		r.Watching = append(r.Watching, recommendation{Ticker: fmt.Sprintf("W%02d", i), Score: 2, HasPlan: true, MaxEntry: 1020, Stop: 950, Target: 1100,
			Parts: []recPart{{Group: "Teknikal", Text: "Tren naik: harga di atas MA200 (Rp 9.000) dan MA50 di atas MA200", Points: 2}}})
	}
	r.WithBroker = []recommendation{{Ticker: "BBRI", Label: recAvoid, Score: -5, Technical: -3, Broker: -2,
		Parts: []recPart{{Group: "Teknikal", Text: "Tren turun", Points: -2},
			{Group: "Broker", Text: "Net investor asing −506,4 M, −13,2% dari nilai transaksi", Points: -2}}}}
	r.ToPaste = append(r.Recommended[:2:2], r.Watching[:3]...)
	brief := composeBrief(r)
	if n := len([]rune(brief)); n > 4096 {
		t.Errorf("brief has %d characters, Telegram takes 4096", n)
	}
	if strings.Contains(brief, "Close 1000 > high") {
		t.Error("a reason's > wasn't escaped for Telegram's HTML")
	}
	for _, want := range []string{"LEMAH", "18 dari 45", "lainnya di halaman Ringkasan", "−0,8%", "Rekomendasi", "<b>R00</b> skor +5", "…dan 4 lainnya di halaman", "<b>W00</b> skor +2", "…dan 4 lainnya di halaman", "Tempel broker summary Asing", "• <b>W02</b>: 3 bulan, 1 bulan, 1 minggu, hari ini",
		"<b>BBRI</b> Hindari, skor −5 (teknikal −3, broker −2): Net investor asing"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
}

// fakeTelegram keeps what the job sends.
type fakeTelegram struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeTelegram) Send(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}

// fakeYahoo serves a year and a half of made-up daily prices up to the given last day.
func fakeYahoo(t *testing.T, last *time.Time) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		symbol := strings.TrimPrefix(r.URL.Path, "/v8/finance/chart/")
		seed := float64(len(symbol)*7 + int(symbol[1]))
		var ts []int64
		var o, h, l, c, v []float64
		day := last.AddDate(0, -historyMonths, 0)
		for i := 0; !day.After(*last); day = day.AddDate(0, 0, 1) {
			if !isWeekday(day) {
				continue
			}
			price := 1000 + 200*math.Sin(float64(i)/(15+seed/10)) + float64(i)
			ts = append(ts, day.Add(9*time.Hour).Unix())
			o, h, l, c, v = append(o, price), append(h, price*1.02), append(l, price*0.98), append(c, price*1.005), append(v, 1e6+float64(i%7)*4e5)
			i++
		}
		type quote struct {
			Open   []float64 `json:"open"`
			High   []float64 `json:"high"`
			Low    []float64 `json:"low"`
			Close  []float64 `json:"close"`
			Volume []float64 `json:"volume"`
		}
		resp := map[string]any{"chart": map[string]any{"result": []any{map[string]any{
			"timestamp":  ts,
			"indicators": map[string]any{"quote": []quote{{o, h, l, c, v}}},
		}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// The whole day: prices in, paper trades recorded and moved on, brief out, and a second run
// the same day doing nothing.
func TestJobRunsADay(t *testing.T) {
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
	if _, err := pool.Exec(ctx, "TRUNCATE paper_trades, momentum_months, runs"); err != nil {
		t.Fatal(err)
	}

	last := time.Date(2026, 11, 27, 0, 0, 0, 0, wib) // a Friday
	yahoo := fakeYahoo(t, &last)
	t.Cleanup(yahoo.Close)
	tg := &fakeTelegram{}
	clock := last.Add(17 * time.Hour)
	j := &job{
		store:    &store{pool: pool},
		yahoo:    &yahooClient{http: yahoo.Client(), base: yahoo.URL},
		telegram: tg,
		tickers:  []string{"BBCA", "BBRI", "TLKM", "ASII", "ANTM", "MDKA", "PTBA"},
		now:      func() time.Time { return clock },
	}

	// Run each trading day for two weeks, across a month end.
	days := 0
	first := last
	for d := first; d.Before(first.AddDate(0, 0, 14)); d = d.AddDate(0, 0, 1) {
		if !isWeekday(d) {
			continue
		}
		last, clock = d, d.Add(17*time.Hour)
		result, err := j.Run(ctx, false)
		if err != nil {
			t.Fatalf("%s: %v", d.Format(time.DateOnly), err)
		}
		if result != resultOK {
			t.Fatalf("%s: result %s", d.Format(time.DateOnly), result)
		}
		days++
	}
	if len(tg.sent) != days {
		t.Fatalf("sent %d briefs over %d days", len(tg.sent), days)
	}
	if result, err := j.Run(ctx, false); err != nil || result != resultSkipped {
		t.Errorf("second run the same day: %s, %v; want skipped", result, err)
	}

	var trades, entered int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(entry_date) FROM paper_trades`).Scan(&trades, &entered); err != nil {
		t.Fatal(err)
	}
	if trades == 0 || entered == 0 {
		t.Errorf("%d paper trades, %d bought; want some of each", trades, entered)
	}
	var months int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM momentum_months`).Scan(&months); err != nil {
		t.Fatal(err)
	}
	if months != 1 {
		t.Errorf("%d momentum months recorded, want 1 (November's end)", months)
	}
	t.Logf("%d trades, %d bought; last brief:\n%s", trades, entered, tg.sent[len(tg.sent)-1])
}
