package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Foreign flow per stock over set windows, from broker summaries pasted with the Foreign
// investor filter. A window is exact when every one of its trading days was pasted on its own;
// otherwise a summary pasted for a longer period (Stockbit's 1W, 1M or 3M) counts, joined with
// the days pasted one by one after it. So the user pastes the three periods once, then only
// each day's summary, and the windows keep themselves up to date.

// flowSpan is a window: its name and how many trading days it covers.
type flowSpan struct {
	Key    string
	Name   string
	Days   int
	Months int // for the Stockbit period to paste; 0 is a week, -1 the day
}

var flowSpans = []flowSpan{
	{"hari", "hari ini", 1, -1},
	{"minggu", "1 minggu", 5, 0},
	{"bulan", "1 bulan", 20, 1},
	{"3bulan", "3 bulan", 60, 3},
}

// A pasted period counts for a window when it reaches back between these shares of the window,
// counted to the latest day. Stockbit's presets fall well inside: 1W is 5 trading days, 1M about
// 21 and 3M about 62. Past the upper bound the period is too old and wants pasting again.
const (
	flowMinShare = 0.8
	flowMaxShare = 1.5
)

type flowWindow struct {
	flowSpan
	OK       bool
	Start    time.Time
	End      time.Time
	Net      float64 // rupiah: foreign buying less selling
	Turnover float64 // rupiah: close × volume over the same days
	Source   string
	// Paste is what to paste in Stockbit to complete the window.
	PasteFrom time.Time
}

// Share is the net against what traded, the measure the points use.
func (w flowWindow) Share() float64 {
	if w.Turnover <= 0 {
		return math.NaN()
	}
	return w.Net / w.Turnover
}

type foreignFlow struct {
	Ticker    string
	Day       time.Time
	Windows   []flowWindow // in flowSpans order
	Latest    *brokerSummary
	LastDaily time.Time
	Pasted    int // foreign summaries kept for the stock
}

func (f foreignFlow) Window(key string) flowWindow {
	for _, w := range f.Windows {
		if w.Key == key {
			return w
		}
	}
	return flowWindow{}
}

// Complete reports whether every window has its data.
func (f foreignFlow) Complete() bool {
	for _, w := range f.Windows {
		if !w.OK {
			return false
		}
	}
	return len(f.Windows) > 0
}

// Missing lists the windows still to paste.
func (f foreignFlow) Missing() []flowWindow {
	var out []flowWindow
	for _, w := range f.Windows {
		if !w.OK {
			out = append(out, w)
		}
	}
	return out
}

// HasAny reports whether any window longer than a day has data, which switches the score to
// the windows.
func (f foreignFlow) HasAny() bool {
	for _, w := range f.Windows {
		if w.OK && w.Days > 1 {
			return true
		}
	}
	return false
}

// isFlowSummary reports whether a summary is foreign flow: the Foreign investor filter, net.
func isFlowSummary(b brokerSummary) bool {
	return b.Investor == "foreign" && b.Mode == "net"
}

func summaryNet(b brokerSummary) float64 {
	n := 0.0
	for _, l := range b.Buy {
		n += l.Value
	}
	for _, l := range b.Sell {
		n -= l.Value
	}
	return n
}

// buildFlow works out a stock's windows on its last trading day from the summaries given,
// which may include other stocks' and other filters'.
func buildFlow(s *series, list []brokerSummary) foreignFlow {
	f := foreignFlow{Ticker: s.ticker}
	last := len(s.bars) - 1
	if last < 0 {
		return f
	}
	f.Day = s.bars[last].Date
	key := func(t time.Time) string { return t.Format(time.DateOnly) }

	// The stock's foreign summaries, one per period, the regular market first.
	byPeriod := map[string]*brokerSummary{}
	for i := range list {
		b := &list[i]
		if b.Ticker != s.ticker || !isFlowSummary(*b) || b.End.After(f.Day) {
			continue
		}
		k := key(b.Start) + key(b.End)
		if old, ok := byPeriod[k]; !ok || (old.Market != "regular" && b.Market == "regular") || (old.Market == b.Market && b.ID > old.ID) {
			byPeriod[k] = b
		}
	}
	daily := map[string]float64{}
	var periods []*brokerSummary
	for _, b := range byPeriod {
		f.Pasted++
		if f.Latest == nil || b.End.After(f.Latest.End) || (b.End.Equal(f.Latest.End) && b.Start.After(f.Latest.Start)) {
			f.Latest = b
		}
		if b.Start.Equal(b.End) {
			daily[key(b.End)] = summaryNet(*b)
			if b.End.After(f.LastDaily) {
				f.LastDaily = b.End
			}
		} else {
			periods = append(periods, b)
		}
	}
	sort.Slice(periods, func(a, b int) bool { return periods[a].ID > periods[b].ID })

	// index finds the first bar on or after a date.
	index := func(t time.Time) int {
		return sort.Search(len(s.bars), func(i int) bool { return !s.bars[i].Date.Before(t) })
	}
	turnover := func(from, to int) float64 {
		t := 0.0
		for i := max(from, 0); i <= to; i++ {
			t += s.bars[i].Close * float64(s.bars[i].Volume)
		}
		return t
	}
	dailiesFrom := func(from int) (float64, int, bool) {
		sum, n := 0.0, 0
		for i := from; i <= last; i++ {
			v, ok := daily[key(s.bars[i].Date)]
			if !ok {
				return 0, 0, false
			}
			sum += v
			n++
		}
		return sum, n, true
	}

	for _, span := range flowSpans {
		w := flowWindow{flowSpan: span, End: f.Day}
		first := last - span.Days + 1
		w.PasteFrom = spanFrom(span, f.Day)
		// Every day pasted on its own.
		if first >= 0 {
			if sum, _, ok := dailiesFrom(first); ok {
				w.OK, w.Start, w.Net, w.Turnover = true, s.bars[first].Date, sum, turnover(first, last)
				if span.Days == 1 {
					w.Source = "tempelan hari ini"
				} else {
					w.Source = fmt.Sprintf("%d tempelan harian", span.Days)
				}
			}
		}
		// A longer pasted period, joined with the days after it.
		if !w.OK && span.Days > 1 {
			lo, hi := int(math.Ceil(flowMinShare*float64(span.Days))), int(flowMaxShare*float64(span.Days))
			bestGap := -1
			for _, b := range periods {
				from, end := index(b.Start), index(b.End)
				if end > last || !s.bars[end].Date.Equal(b.End) {
					continue
				}
				length := last - from + 1
				if length < lo || length > hi {
					continue
				}
				after, n, ok := dailiesFrom(end + 1)
				if !ok {
					continue
				}
				gap := abs(length - span.Days)
				if bestGap >= 0 && gap >= bestGap {
					continue
				}
				bestGap = gap
				w.OK, w.Start, w.Net, w.Turnover = true, b.Start, summaryNet(*b)+after, turnover(from, last)
				w.Source = "tempelan " + tanggal(b.Start) + " – " + tanggal(b.End)
				if n > 0 {
					w.Source += fmt.Sprintf(" + %d hari harian", n)
				}
			}
		}
		f.Windows = append(f.Windows, w)
	}
	return f
}

// spanFrom is the start of the Stockbit period to paste for a window ending on day: its 1W, 1M
// and 3M presets.
func spanFrom(span flowSpan, day time.Time) time.Time {
	switch {
	case span.Months < 0:
		return day
	case span.Months == 0:
		return day.AddDate(0, 0, -7)
	}
	return day.AddDate(0, -span.Months, 0)
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// flowPoints scores the foreign flow windows, adding their parts. The week and the month count a
// point each when the net is at least 2% of what traded, and one more when they agree; three
// months is the backdrop, a point either way; the day is shown but not scored, being noisy.
func flowPoints(r *recommendation, f foreignFlow) float64 {
	points := 0.0
	add := func(p float64, format string, args ...any) {
		r.Parts = append(r.Parts, recPart{Group: "Broker", Text: fmt.Sprintf(format, args...), Points: p})
		points += p
	}
	dir := func(w flowWindow) float64 {
		switch sh := w.Share(); {
		case !w.OK || math.IsNaN(sh):
			return 0
		case sh >= 0.02:
			return 1
		case sh <= -0.02:
			return -1
		}
		return 0
	}
	say := func(w flowWindow) string {
		if !w.OK {
			return fmt.Sprintf("Asing %s: belum ada data", w.Name)
		}
		verb := "net beli"
		if w.Net < 0 {
			verb = "net jual"
		}
		return fmt.Sprintf("Asing %s %s %s (%s dari nilai transaksi)", w.Name, verb, nilaiM(w.Net), pct(w.Share()))
	}
	day, week, month, quarter := f.Window("hari"), f.Window("minggu"), f.Window("bulan"), f.Window("3bulan")
	if day.OK {
		add(0, "%s", say(day))
	}
	add(dir(week), "%s", say(week))
	add(dir(month), "%s", say(month))
	if d := dir(week); d != 0 && d == dir(month) {
		if d > 0 {
			add(1, "Asing konsisten akumulasi: 1 minggu dan 1 bulan sama-sama net beli")
		} else {
			add(-1, "Asing konsisten distribusi: 1 minggu dan 1 bulan sama-sama net jual")
		}
	}
	add(dir(quarter), "%s", say(quarter))
	return points
}

// nilaiM writes an amount in billions without a sign, "506,4 M".
func nilaiM(f float64) string {
	return fmt.Sprintf("%s M", decimalComma(math.Abs(f)/1e9))
}

func decimalComma(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	out := []byte{}
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, ',')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}
