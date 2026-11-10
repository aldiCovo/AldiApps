package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The web pages, at saham.aldiab.ai.id behind one password. They are served on their own port,
// so the Ingress never reaches POST /run or /api/latest on the main one.

//go:embed ui
var uiFiles embed.FS

const (
	sessionCookie = "saham_sesi"
	sessionLength = 30 * 24 * time.Hour
	// wrongPasswordDelay slows guessing, on top of the Ingress's limit per visitor.
	wrongPasswordDelay = time.Second
)

type webUI struct {
	job      *job
	store    *store
	password []byte
	key      []byte // signs the session cookie
	runAt    time.Duration
	pages    map[string]*template.Template
	now      func() time.Time
}

func newWebUI(j *job, st *store, password string, runAt time.Duration) (*webUI, error) {
	// The key comes from the password, so changing the password logs every browser out.
	key := sha256.Sum256([]byte("saham-ui-session\x00" + password))
	u := &webUI{job: j, store: st, password: []byte(password), key: key[:], runAt: runAt,
		pages: map[string]*template.Template{}, now: time.Now}
	for _, name := range []string{"login", "loading", "home", "stocks", "stock", "paper", "momentum"} {
		t, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(uiFiles, "ui/layout.html", "ui/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", name, err)
		}
		u.pages[name] = t
	}
	return u, nil
}

func (u *webUI) handler() http.Handler {
	static, _ := fs.Sub(uiFiles, "ui/static")
	files := http.FileServerFS(static)
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	})))
	mux.HandleFunc("GET /login", u.loginPage)
	mux.HandleFunc("POST /login", u.login)
	mux.HandleFunc("POST /logout", u.logout)
	mux.Handle("GET /{$}", u.private(u.home))
	mux.Handle("GET /saham", u.private(u.stocks))
	mux.Handle("GET /saham/{ticker}", u.private(u.stock))
	mux.Handle("GET /paper", u.private(u.paper))
	mux.Handle("GET /momentum", u.private(u.momentum))
	mux.Handle("GET /data/{ticker}", u.private(u.chartData))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Robots-Tag", "noindex")
		next.ServeHTTP(w, r)
	})
}

// --- Login ---

// sessionValue is "<expiry>.<signature>": nothing to look up, and nothing to forge without the key.
func (u *webUI) sessionValue(expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, u.key)
	mac.Write([]byte(exp))
	return exp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (u *webUI) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || u.now().Unix() > unix {
		return false
	}
	want := u.sessionValue(time.Unix(unix, 0))
	return hmac.Equal([]byte(want), []byte(exp+"."+sig))
}

func (u *webUI) private(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !u.loggedIn(r) {
			if strings.HasPrefix(r.URL.Path, "/data/") {
				http.Error(w, "login dulu", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	})
}

func (u *webUI) loginPage(w http.ResponseWriter, r *http.Request) {
	if u.loggedIn(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	u.render(w, r, http.StatusOK, "login", page{Title: "Masuk"})
}

func (u *webUI) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	given := sha256.Sum256([]byte(r.PostFormValue("password")))
	want := sha256.Sum256(u.password)
	if subtle.ConstantTimeCompare(given[:], want[:]) != 1 {
		slog.Warn("wrong password on the saham pages")
		time.Sleep(wrongPasswordDelay)
		u.render(w, r, http.StatusUnauthorized, "login", page{Title: "Masuk", Data: "Password salah."})
		return
	}
	expires := u.now().Add(sessionLength)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: u.sessionValue(expires), Path: "/",
		Expires: expires, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (u *webUI) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// --- Pages ---

// page is what every page's template gets; Data is the page's own.
type page struct {
	Title    string
	Active   string
	LoggedIn bool
	View     *marketView
	LastRun  time.Time
	NextRun  time.Time
	Data     any
}

func (u *webUI) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	p.LoggedIn = name != "login"
	if p.LoggedIn {
		p.NextRun = nextRun(u.now(), u.runAt)
		if _, finished, _, ok, err := u.store.LastRun(r.Context()); err == nil && ok {
			p.LastRun = finished
		}
	}
	var buf bytes.Buffer
	if err := u.pages[name].Execute(&buf, p); err != nil {
		slog.Error("rendering a page", "page", name, "error", err)
		http.Error(w, "halaman gagal dibuat", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// withView renders the loading page until the prices are in.
func (u *webUI) withView(w http.ResponseWriter, r *http.Request, title, active string) (*marketView, bool) {
	v := u.job.view.Load()
	if v == nil {
		u.render(w, r, http.StatusOK, "loading", page{Title: title, Active: active})
		return nil, false
	}
	return v, true
}

func (u *webUI) fail(w http.ResponseWriter, err error) {
	slog.Error("reading the database for a page", "error", err)
	http.Error(w, "database gagal dibaca", http.StatusInternalServerError)
}

type homeData struct {
	Stats    []statsRow
	Momentum momentumState
	Open     int
}

func (u *webUI) home(w http.ResponseWriter, r *http.Request) {
	v, ok := u.withView(w, r, "Ringkasan", "home")
	if !ok {
		return
	}
	stats, err := u.store.Stats(r.Context())
	if err != nil {
		u.fail(w, err)
		return
	}
	mo, err := u.momentumState(r.Context(), v)
	if err != nil {
		u.fail(w, err)
		return
	}
	open, err := u.store.Trades(r.Context(), tradesUnfinished, "", false, 1000)
	if err != nil {
		u.fail(w, err)
		return
	}
	u.render(w, r, http.StatusOK, "home", page{Title: "Ringkasan", Active: "home", View: v,
		Data: homeData{Stats: statsRows(stats), Momentum: mo, Open: len(open)}})
}

type stocksData struct {
	Rows []stockRow
	Sort string
}

func (u *webUI) stocks(w http.ResponseWriter, r *http.Request) {
	v, ok := u.withView(w, r, "Saham LQ45", "stocks")
	if !ok {
		return
	}
	rows := v.rows()
	by := r.URL.Query().Get("urut")
	sortRows(rows, by)
	u.render(w, r, http.StatusOK, "stocks", page{Title: "Saham LQ45", Active: "stocks", View: v,
		Data: stocksData{Rows: rows, Sort: by}})
}

type stockData struct {
	Row      stockRow
	MA50     float64
	MA200    float64
	RSI2     float64
	ATR      float64
	AvgVol   float64
	High52   float64
	Low52    float64
	Signals  []Signal
	Trades   []tradeRow
	LastDate time.Time
}

func (u *webUI) stock(w http.ResponseWriter, r *http.Request) {
	ticker := strings.ToUpper(r.PathValue("ticker"))
	v, ok := u.withView(w, r, ticker, "stocks")
	if !ok {
		return
	}
	s := v.Stocks[ticker]
	if s == nil {
		http.NotFound(w, r)
		return
	}
	i := len(s.bars) - 1
	d := stockData{MA50: s.sma50[i], MA200: s.sma200[i], RSI2: s.rsi2[i], ATR: s.atr14[i],
		AvgVol: s.avgVol20[i], High52: s.bars[i].High, Low52: s.bars[i].Low, LastDate: s.bars[i].Date}
	for _, row := range v.rows() {
		if row.Ticker == ticker {
			d.Row = row
		}
	}
	for k := max(0, i-250); k <= i; k++ {
		d.High52 = math.Max(d.High52, s.bars[k].High)
		d.Low52 = math.Min(d.Low52, s.bars[k].Low)
	}
	for _, sig := range v.Screener {
		if sig.Ticker == ticker {
			d.Signals = append(d.Signals, sig)
		}
	}
	var err error
	if d.Trades, err = u.store.Trades(r.Context(), "", ticker, true, 200); err != nil {
		u.fail(w, err)
		return
	}
	u.render(w, r, http.StatusOK, "stock", page{Title: ticker, Active: "stocks", View: v, Data: d})
}

// heldTrade is an unfinished paper trade with where the price is now.
type heldTrade struct {
	tradeRow
	Now    float64
	Return float64 // after costs, if sold at the last close; NaN while waiting
}

type paperData struct {
	Stats      []statsRow
	Unfinished []heldTrade
	Closed     []tradeRow
	WithRandom bool
	Started    time.Time
	HasStarted bool
}

func (u *webUI) paper(w http.ResponseWriter, r *http.Request) {
	v, ok := u.withView(w, r, "Paper trading", "paper")
	if !ok {
		return
	}
	ctx := r.Context()
	d := paperData{WithRandom: r.URL.Query().Get("acak") == "1"}
	stats, err := u.store.Stats(ctx)
	if err != nil {
		u.fail(w, err)
		return
	}
	d.Stats = statsRows(stats)
	if d.Started, d.HasStarted, err = u.store.FirstDay(ctx); err != nil {
		u.fail(w, err)
		return
	}
	open, err := u.store.Trades(ctx, tradesUnfinished, "", d.WithRandom, 500)
	if err != nil {
		u.fail(w, err)
		return
	}
	for _, t := range open {
		h := heldTrade{tradeRow: t, Return: math.NaN()}
		if s := v.Stocks[t.Ticker]; s != nil {
			h.Now = s.closes[len(s.closes)-1]
			if t.EntryPrice != nil && *t.EntryPrice > 0 {
				h.Return = h.Now*(1-sellFee)/(*t.EntryPrice*(1+buyFee)) - 1
			}
		}
		d.Unfinished = append(d.Unfinished, h)
	}
	if d.Closed, err = u.store.Trades(ctx, tradesClosed, "", d.WithRandom, 200); err != nil {
		u.fail(w, err)
		return
	}
	u.render(w, r, http.StatusOK, "paper", page{Title: "Paper trading", Active: "paper", View: v, Data: d})
}

type momentumData struct {
	State   momentumState
	Months  []momentumMonth
	Ranking []stockRow
}

func (u *webUI) momentum(w http.ResponseWriter, r *http.Request) {
	v, ok := u.withView(w, r, "Momentum", "momentum")
	if !ok {
		return
	}
	months, err := u.store.MomentumMonths(r.Context())
	if err != nil {
		u.fail(w, err)
		return
	}
	state := momentumSummary(months, v)
	// Newest first on the page.
	for a, b := 0, len(months)-1; a < b; a, b = a+1, b-1 {
		months[a], months[b] = months[b], months[a]
	}
	u.render(w, r, http.StatusOK, "momentum", page{Title: "Momentum", Active: "momentum", View: v,
		Data: momentumData{State: state, Months: months, Ranking: v.momentumRanking(10)}})
}

func (u *webUI) momentumState(ctx context.Context, v *marketView) (momentumState, error) {
	months, err := u.store.MomentumMonths(ctx)
	if err != nil {
		return momentumState{}, err
	}
	return momentumSummary(months, v), nil
}

// momentumSummary is how the momentum rule stands, as the job reports it, without writing.
func momentumSummary(months []momentumMonth, v *marketView) momentumState {
	state := momentumState{}
	value := 1.0
	var held []string
	for k, m := range months {
		picks := m.Picks
		if !m.MarketOK {
			picks = nil
		}
		if k+1 < len(months) {
			ret := 0.0
			if m.ReturnPct != nil {
				ret = *m.ReturnPct / 100
			} else {
				ret = monthReturn(v.Stocks, held, picks, m.MonthEnd, months[k+1].MonthEnd)
			}
			value *= 1 + ret
			state.Months++
		} else {
			state.HasMonth = true
			state.Current = m.Picks
			state.MarketOK = m.MarketOK
			state.Since = m.MonthEnd
			state.Running = monthReturn(v.Stocks, held, picks, m.MonthEnd, v.Day)
		}
		held = picks
	}
	state.Total = value*(1+state.Running) - 1
	return state
}

// statsRow lines a rule up with its random entries, for all days and by the market's health.
type statsRow struct {
	Rule, Random                 string
	All, Healthy, Weak           ruleStats
	RandAll, RandHealthy, RandWk ruleStats
}

func statsRows(stats []ruleStats) []statsRow {
	find := func(rule string, ok *bool) ruleStats {
		for _, s := range stats {
			if s.Rule != rule {
				continue
			}
			if (ok == nil) == (s.MarketOK == nil) && (ok == nil || *ok == *s.MarketOK) {
				return s
			}
		}
		return ruleStats{Rule: rule}
	}
	yes, no := true, false
	var out []statsRow
	for _, rule := range []string{strategyPull, strategyBrk, strategyRSI2, strategyBrkTrail} {
		row := statsRow{Rule: rule, All: find(rule, nil), Healthy: find(rule, &yes), Weak: find(rule, &no)}
		// Pullback has no random twin of its own; its exits are breakout's.
		random := strategyBasePrefx + rule
		if rule == strategyPull {
			random = strategyBasePrefx + strategyBrk
		}
		row.Random = random
		row.RandAll, row.RandHealthy, row.RandWk = find(random, nil), find(random, &yes), find(random, &no)
		out = append(out, row)
	}
	return out
}

// --- Chart data ---

type chartPoint struct {
	Time  string  `json:"time"`
	Value float64 `json:"value"`
	Color string  `json:"color,omitempty"`
}

type chartCandle struct {
	Time  string  `json:"time"`
	Open  float64 `json:"open"`
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Close float64 `json:"close"`
}

type chartMarker struct {
	Time     string `json:"time"`
	Position string `json:"position"`
	Shape    string `json:"shape"`
	Kind     string `json:"kind"` // entry or exit, coloured by the page
	Text     string `json:"text"`
}

type chartJSON struct {
	Candles []chartCandle `json:"candles"`
	Volume  []chartPoint  `json:"volume,omitempty"`
	MA50    []chartPoint  `json:"ma50"`
	MA200   []chartPoint  `json:"ma200"`
	RSI     []chartPoint  `json:"rsi"`
	Markers []chartMarker `json:"markers"`
}

func (u *webUI) chartData(w http.ResponseWriter, r *http.Request) {
	v := u.job.view.Load()
	if v == nil {
		http.Error(w, "harga belum dimuat", http.StatusServiceUnavailable)
		return
	}
	ticker := strings.ToUpper(r.PathValue("ticker"))
	s := v.Stocks[ticker]
	if ticker == "IHSG" {
		s = v.Index
	}
	if s == nil {
		http.NotFound(w, r)
		return
	}
	out := chartJSON{Markers: []chartMarker{}}
	round := func(f float64) float64 { return math.Round(f*100) / 100 }
	for i, b := range s.bars {
		day := b.Date.Format(time.DateOnly)
		out.Candles = append(out.Candles, chartCandle{day, round(b.Open), round(b.High), round(b.Low), round(b.Close)})
		if ticker != "IHSG" {
			kind := "up"
			if b.Close < b.Open {
				kind = "down"
			}
			out.Volume = append(out.Volume, chartPoint{Time: day, Value: float64(b.Volume), Color: kind})
		}
		if !math.IsNaN(s.sma50[i]) {
			out.MA50 = append(out.MA50, chartPoint{Time: day, Value: round(s.sma50[i])})
		}
		if !math.IsNaN(s.sma200[i]) {
			out.MA200 = append(out.MA200, chartPoint{Time: day, Value: round(s.sma200[i])})
		}
		if !math.IsNaN(s.rsi14[i]) {
			out.RSI = append(out.RSI, chartPoint{Time: day, Value: round(s.rsi14[i])})
		}
	}
	if ticker != "IHSG" {
		trades, err := u.store.Trades(r.Context(), "", ticker, false, 200)
		if err != nil {
			u.fail(w, err)
			return
		}
		first := s.bars[0].Date
		for _, t := range trades {
			if t.EntryDate != nil && !t.EntryDate.Before(first) {
				out.Markers = append(out.Markers, chartMarker{Time: t.EntryDate.Format(time.DateOnly),
					Position: "belowBar", Shape: "arrowUp", Kind: "entry", Text: "Beli " + ruleLabel(t.Rule)})
			}
			if t.ExitDate != nil && t.EntryDate != nil && t.Status != statusOpen && !t.ExitDate.Before(first) {
				out.Markers = append(out.Markers, chartMarker{Time: t.ExitDate.Format(time.DateOnly),
					Position: "aboveBar", Shape: "arrowDown", Kind: "exit", Text: "Jual " + pctPtr(t.ReturnPct)})
			}
		}
		sortMarkers(out.Markers)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func sortMarkers(m []chartMarker) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && m[j].Time < m[j-1].Time; j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
}

// --- Template helpers ---

var templateFuncs = template.FuncMap{
	"rp":      rp,
	"pct":     pct,
	"pctPtr":  pctPtr,
	"tanggal": tanggal,
	"tanggalPtr": func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return tanggal(*t)
	},
	"rpPtr": func(f *float64) string {
		if f == nil {
			return "-"
		}
		return rp(*f)
	},
	"jam": func(t time.Time) string {
		t = t.In(wib)
		return fmt.Sprintf("%s %02d:%02d WIB", tanggal(t), t.Hour(), t.Minute())
	},
	// arah is the CSS class for a change: up, down or flat.
	"arah": func(f float64) string {
		switch {
		case math.IsNaN(f) || math.Abs(f) < 0.0005:
			return "flat"
		case f > 0:
			return "up"
		}
		return "down"
	},
	"arahPtr": func(f *float64) string {
		switch {
		case f == nil || math.Abs(*f) < 0.05:
			return "flat"
		case *f > 0:
			return "up"
		}
		return "down"
	},
	"angka": func(f float64) string {
		if math.IsNaN(f) {
			return "-"
		}
		return strings.Replace(fmt.Sprintf("%.0f", f), "-", "−", 1)
	},
	"juta": func(f float64) string {
		if math.IsNaN(f) {
			return "-"
		}
		return strings.Replace(fmt.Sprintf("%.1f jt", f/1e6), ".", ",", 1)
	},
	"persen":     func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"inc":        func(i int) int { return i + 1 },
	"winRate":    winRate,
	"meanPct":    meanPct,
	"aturan":     ruleLabel,
	"penjelasan": ruleExplanation,
	"status":     statusLabel,
	"keluar":     exitLabel,
	"join":       strings.Join,
	"isNaN":      math.IsNaN,
	"selisih": func(rule, random ruleStats) string {
		if rule.Closed == 0 || random.Closed == 0 {
			return "-"
		}
		return pct((rule.MeanPct - random.MeanPct) / 100)
	},
	"arahSelisih": func(rule, random ruleStats) string {
		if rule.Closed == 0 || random.Closed == 0 || rule.MeanPct == random.MeanPct {
			return "flat"
		}
		if rule.MeanPct > random.MeanPct {
			return "up"
		}
		return "down"
	},
	"breadth": func(m marketState) float64 {
		if m.Counted == 0 {
			return 0
		}
		return float64(m.Above200) / float64(m.Counted)
	},
	"lebar": func(f float64) string { // a width class in steps of 5%, since inline styles are blocked
		return fmt.Sprintf("w%d", int(math.Round(math.Max(0, math.Min(1, f))*20))*5)
	},
}

func pctPtr(f *float64) string {
	if f == nil {
		return "-"
	}
	return pct(*f / 100)
}

func ruleLabel(rule string) string {
	if r, ok := strings.CutPrefix(rule, strategyBasePrefx); ok {
		return "acak (" + ruleLabel(r) + ")"
	}
	switch rule {
	case strategyPull:
		return "Pullback"
	case strategyBrk:
		return "Breakout"
	case strategyRSI2:
		return "RSI(2)"
	case strategyBrkTrail:
		return "Breakout trailing"
	case strategyMomentum:
		return "Momentum"
	}
	return rule
}

func ruleExplanation(rule string) string {
	switch rule {
	case strategyPull:
		return "Tren naik yang terkoreksi: RSI(14) naik lagi di atas 40. SL 2 ATR, target 3 ATR, maks 20 hari."
	case strategyBrk:
		return "Close menembus high 20 hari dengan volume 1,5x. SL 2 ATR, target 3 ATR, maks 20 hari."
	case strategyRSI2:
		return "Di atas MA200 tapi RSI(2) di bawah 10. Jual saat close di atas MA5, SL 3 ATR, maks 10 hari."
	case strategyBrkTrail:
		return "Breakout yang sama, keluar dengan trailing stop 3 ATR, maks 40 hari."
	}
	return ""
}

func statusLabel(s string) string {
	switch s {
	case statusWaiting:
		return "menunggu open"
	case statusSkipped:
		return "batal beli"
	case statusOpen:
		return "dipegang"
	}
	return exitName(s)
}

func exitLabel(sig Signal) string {
	switch sig.Exit {
	case exitTarget:
		return "Target " + rp(sig.Target)
	case exitMA5:
		return "Close di atas MA5"
	case exitTrail:
		return "Trailing stop"
	}
	return ""
}
