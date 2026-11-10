package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// The combined view of a stock: its technical state and, when a recent broker summary was
// pasted, what the brokers did. Each part adds or takes points for a written reason, and the
// total gives a label. The points are judgement, not fitted to the past; the daily job records
// every "Kandidat beli" as a paper trade, so whether they help shows against random entries.

// brokerMaxAge is how old a broker summary's period may end and still count.
const brokerMaxAge = 14 * 24 * time.Hour

// Labels, from the total.
const (
	recBuy   = "Kandidat beli"
	recWatch = "Pantau"
	recWait  = "Tunggu"
	recAvoid = "Hindari"
)

type recPart struct {
	Group  string // Teknikal, Pasar or Broker
	Text   string
	Points float64
}

type recommendation struct {
	Ticker    string
	Close     float64
	Score     float64
	Technical float64 // the technical and market points
	Broker    float64 // the broker points
	Label     string
	Class     string // good, info, warn or bad
	Parts     []recPart
	Summary   *brokerSummary // the summary used, nil when none is recent
	// The trade plan, the same exits as the breakout rule, so its paper trades compare
	// with acak/breakout.
	HasPlan  bool
	MaxEntry float64
	Stop     float64
	Target   float64
}

// RewardRisk is the target's gain over the stop's loss, from the close.
func (r recommendation) RewardRisk() float64 {
	if r.Close <= r.Stop {
		return math.NaN()
	}
	return (r.Target - r.Close) / (r.Close - r.Stop)
}

// Reasons lists the parts that moved the score, biggest first.
func (r recommendation) Reasons(n int) []recPart {
	var parts []recPart
	for _, p := range r.Parts {
		if p.Points != 0 {
			parts = append(parts, p)
		}
	}
	sort.SliceStable(parts, func(a, b int) bool { return math.Abs(parts[a].Points) > math.Abs(parts[b].Points) })
	return parts[:min(n, len(parts))]
}

// pickBroker chooses the summary to use for a stock on a day: one whose period ended within
// brokerMaxAge, preferring the Foreign investor filter, which is the real foreign flow, then the
// latest period.
func pickBroker(list []brokerSummary, ticker string, day time.Time) *brokerSummary {
	var best *brokerSummary
	rank := func(b *brokerSummary) int {
		r := 0
		if b.Investor == "foreign" {
			r += 2
		}
		if b.Market == "regular" {
			r++
		}
		return r
	}
	for i := range list {
		b := &list[i]
		if b.Ticker != ticker || b.End.After(day) || day.Sub(b.End) > brokerMaxAge || b.Investor == "domestic" {
			continue
		}
		if best == nil || b.End.After(best.End) || (b.End.Equal(best.End) && rank(b) > rank(best)) {
			best = b
		}
	}
	return best
}

// recommend scores one stock from the view and, when given, a broker summary.
func recommend(v *marketView, ticker string, b *brokerSummary, foreign map[string]bool) (recommendation, bool) {
	s := v.Stocks[ticker]
	if s == nil {
		return recommendation{}, false
	}
	i := len(s.bars) - 1
	close, ma50, ma200, a := s.closes[i], s.sma50[i], s.sma200[i], s.atr14[i]
	r := recommendation{Ticker: ticker, Close: close}
	add := func(group string, points float64, format string, args ...any) {
		r.Parts = append(r.Parts, recPart{Group: group, Text: fmt.Sprintf(format, args...), Points: points})
	}

	// Trend.
	switch {
	case math.IsNaN(ma200):
		add("Teknikal", 0, "Data belum cukup untuk MA200")
	case close > ma200 && ma50 > ma200:
		add("Teknikal", 2, "Tren naik: harga di atas MA200 (%s) dan MA50 di atas MA200", rp(ma200))
	case close > ma200:
		add("Teknikal", 1, "Harga di atas MA200 (%s), tapi MA50 masih di bawahnya", rp(ma200))
	case close > ma50:
		add("Teknikal", 0, "Di bawah MA200 (%s) tapi di atas MA50: mulai pulih, tren belum berbalik", rp(ma200))
	default:
		add("Teknikal", -2, "Tren turun: harga di bawah MA50 (%s) dan MA200 (%s)", rp(ma50), rp(ma200))
	}

	// Six-month momentum, without the last month.
	if i >= momentumLookback {
		m := s.closes[i-momentumSkip]/s.closes[i-momentumLookback] - 1
		switch {
		case m > 0.10:
			add("Teknikal", 1, "Momentum 6 bulan kuat (%s)", pct(m))
		case m < -0.10:
			add("Teknikal", -1, "Momentum 6 bulan lemah (%s)", pct(m))
		default:
			add("Teknikal", 0, "Momentum 6 bulan datar (%s)", pct(m))
		}
	}

	// RSI(14).
	switch rsi := s.rsi14[i]; {
	case math.IsNaN(rsi):
	case rsi > 75:
		add("Teknikal", -1, "RSI(14) %.0f: jenuh beli, rawan koreksi", rsi)
	case rsi < 30:
		add("Teknikal", 0, "RSI(14) %.0f: jenuh jual, bisa memantul tapi belum tanda berbalik", rsi)
	default:
		add("Teknikal", 0, "RSI(14) %.0f: netral", rsi)
	}

	// The screener's signals at the last close.
	var rules []string
	for _, sig := range v.Screener {
		if sig.Ticker == ticker {
			rules = append(rules, ruleLabel(sig.Strategy))
		}
	}
	if len(rules) > 0 {
		add("Teknikal", 1, "Sinyal screener: %s", strings.Join(rules, ", "))
	}

	// The market.
	m := v.Market
	switch {
	case m.Healthy && m.MA50 > m.MA200:
		add("Pasar", 1, "IHSG SEHAT: di atas MA200")
	case m.Healthy:
		add("Pasar", 0, "IHSG WASPADA: di atas MA200, MA50 masih di bawahnya")
	default:
		add("Pasar", -1, "IHSG LEMAH: di bawah MA200, kebanyakan saham ikut tertekan")
	}
	for _, p := range r.Parts {
		r.Technical += p.Points
	}

	// The brokers.
	if b == nil {
		add("Broker", 0, "Belum ada broker summary 14 hari terakhir: tempel dari Stockbit di halaman Broker")
	} else {
		r.Summary = b
		r.Broker = brokerPoints(&r, s, b, foreign, close)
	}

	r.Score = r.Technical + r.Broker
	switch {
	case r.Score >= 4:
		r.Label, r.Class = recBuy, "good"
	case r.Score >= 2:
		r.Label, r.Class = recWatch, "info"
	case r.Score >= 0:
		r.Label, r.Class = recWait, "warn"
	default:
		r.Label, r.Class = recAvoid, "bad"
	}
	if (r.Label == recBuy || r.Label == recWatch) && !math.IsNaN(a) && a > 0 {
		r.HasPlan = true
		r.MaxEntry = roundDown(close * maxChase)
		r.Stop = roundDown(close - 2*a)
		r.Target = roundDown(close + 3*a)
	}
	return r, true
}

// brokerPoints adds the broker parts and returns their points.
func brokerPoints(r *recommendation, s *series, b *brokerSummary, foreign map[string]bool, close float64) float64 {
	before := len(r.Parts)
	add := func(points float64, format string, args ...any) {
		r.Parts = append(r.Parts, recPart{Group: "Broker", Text: fmt.Sprintf(format, args...), Points: points})
	}
	a := analyzeBrokers(*b, foreign)
	period := tanggal(b.Start) + " – " + tanggal(b.End)

	// Foreign flow against what traded in the period. The Foreign filter gives the real flow;
	// otherwise it's estimated from the foreign brokers, which also serve local clients, so it
	// counts for at most one point.
	net, what, limit := a.ForeignNet(), "Net broker asing (perkiraan)", 1.0
	if b.Investor == "foreign" {
		net, what, limit = a.TotalBuy-a.TotalSell, "Net investor asing", 2
	}
	turnover := 0.0
	for _, bar := range s.bars {
		if !bar.Date.Before(b.Start) && !bar.Date.After(b.End) {
			turnover += bar.Close * float64(bar.Volume)
		}
	}
	if turnover > 0 {
		share := net / turnover
		points := 0.0
		switch {
		case share >= 0.05:
			points = 2
		case share >= 0.01:
			points = 1
		case share <= -0.05:
			points = -2
		case share <= -0.01:
			points = -1
		}
		points = math.Max(-limit, math.Min(limit, points))
		add(points, "%s %s, %s dari nilai transaksi %s", what, miliar(net), pct(share), period)
	} else {
		points := math.Max(-limit, math.Min(limit, math.Copysign(1, net)))
		add(points, "%s %s (%s)", what, miliar(net), period)
	}

	// Stockbit's own reading of the top brokers.
	label := b.AccDist
	if len(b.Top) > 0 && strings.Contains(b.Top[0].Label, "Big") {
		label = b.Top[0].Label
	}
	switch {
	case strings.Contains(strings.ToLower(label), "acc"):
		add(1, "Stockbit: %s (pembeli besar dominan)", label)
	case strings.Contains(strings.ToLower(label), "dist"):
		add(-1, "Stockbit: %s (penjual besar dominan)", label)
	case label != "":
		add(0, "Stockbit: %s", label)
	}

	// Where the price sits against the biggest buyer's average.
	if len(b.Buy) > 0 && b.Buy[0].Avg > 0 {
		top := b.Buy[0]
		gap := close/top.Avg - 1
		switch {
		case gap >= -0.02 && gap <= 0.03:
			add(1, "Harga dekat rata-rata pembeli terbesar %s (%s): area yang mungkin dijaga", top.Broker, rp(top.Avg))
		case gap > 0.10:
			add(0, "Harga sudah %s di atas rata-rata pembeli terbesar %s (%s)", pct(gap), top.Broker, rp(top.Avg))
		default:
			add(0, "Harga %s dari rata-rata pembeli terbesar %s (%s)", pct(gap), top.Broker, rp(top.Avg))
		}
	}

	points := 0.0
	for _, p := range r.Parts[before:] {
		points += p.Points
	}
	return points
}

// recommendAll scores every stock in the view, best first.
func recommendAll(v *marketView, summaries []brokerSummary, foreign map[string]bool) []recommendation {
	var out []recommendation
	for _, t := range v.Tickers {
		if r, ok := recommend(v, t, pickBroker(summaries, t, v.Day), foreign); ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out
}
