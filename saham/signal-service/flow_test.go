package main

import (
	"strings"
	"testing"
	"time"
)

func TestBuildFlow(t *testing.T) {
	last := time.Date(2026, 11, 10, 0, 0, 0, 0, wib)
	s := risingSeries("PTBA", 300, last)
	d := func(i int) time.Time { return s.bars[i].Date }
	id := int64(0)
	mk := func(from, to int, net float64) brokerSummary {
		id++
		b := brokerSummary{ID: id, Ticker: "PTBA", Start: d(from), End: d(to), Investor: "foreign", Market: "regular", Mode: "net"}
		if net >= 0 {
			b.Buy = []brokerLine{{Broker: "BK", Value: net}}
		} else {
			b.Sell = []brokerLine{{Broker: "ZP", Value: -net}}
		}
		return b
	}

	f := buildFlow(s, nil)
	if f.Pasted != 0 || f.Complete() || len(pasteNeeds(recommendation{}, last)) != 4 {
		t.Fatalf("nothing pasted: %+v", f)
	}

	// The three periods pasted three days ago, then each day since.
	full := []brokerSummary{
		mk(237, 296, 10e9), mk(279, 296, 4e9), mk(294, 296, 2e9),
		mk(297, 297, 1e9), mk(298, 298, 1e9), mk(299, 299, -0.5e9),
		{ID: 99, Ticker: "PTBA", Start: d(299), End: d(299), Investor: "all", Mode: "net", Buy: []brokerLine{{Value: 9e12}}},
		{ID: 98, Ticker: "BBRI", Start: d(299), End: d(299), Investor: "foreign", Mode: "net", Buy: []brokerLine{{Value: 9e12}}},
	}
	f = buildFlow(s, full)
	if !f.Complete() || f.Pasted != 6 {
		t.Fatalf("complete: %+v", f.Windows)
	}
	for key, want := range map[string]float64{"hari": -0.5e9, "minggu": 3.5e9, "bulan": 5.5e9, "3bulan": 11.5e9} {
		if w := f.Window(key); w.Net != want || w.Turnover <= 0 {
			t.Errorf("%s: net %v turnover %v (%s)", key, w.Net, w.Turnover, w.Source)
		}
	}
	if w := f.Window("3bulan"); !strings.Contains(w.Source, "+ 3 hari harian") {
		t.Errorf("source %q", w.Source)
	}

	// A day missing breaks the joins, but not today's.
	broken := append([]brokerSummary{}, full[:4]...)
	broken = append(broken, full[5])
	f = buildFlow(s, broken)
	if !f.Window("hari").OK || f.Window("minggu").OK || f.Window("bulan").OK || f.Window("3bulan").OK {
		t.Errorf("broken: %+v", f.Windows)
	}
	rec := recommendation{Ticker: "PTBA", Flow: &f}
	if needs := pasteNeeds(rec, last); len(needs) != 3 || !strings.HasPrefix(needs[0], "3 bulan (10 Agu 2026") {
		t.Errorf("needs %v", needs)
	}

	// Five days pasted one by one make the week on their own; a period reaching back too far
	// doesn't count.
	daily := []brokerSummary{mk(200, 296, 50e9)}
	for i := 295; i <= 299; i++ {
		daily = append(daily, mk(i, i, 1e9))
	}
	f = buildFlow(s, daily)
	if w := f.Window("minggu"); !w.OK || w.Net != 5e9 || w.Source != "5 tempelan harian" {
		t.Errorf("week from dailies: %+v", w)
	}
	if f.Window("3bulan").OK {
		t.Errorf("a 100-day period counted as three months")
	}
}

func TestFlowPoints(t *testing.T) {
	last := time.Date(2026, 11, 10, 0, 0, 0, 0, wib)
	s := risingSeries("PTBA", 400, last)
	v := &marketView{Day: last, Stocks: map[string]*series{"PTBA": s}, Tickers: []string{"PTBA"},
		Market: marketState{Healthy: true, MA50: 7500, MA200: 7000}}
	n := len(s.bars) - 1
	turn := func(days int) float64 {
		t := 0.0
		for i := n - days + 1; i <= n; i++ {
			t += s.bars[i].Close * float64(s.bars[i].Volume)
		}
		return t
	}
	sum := func(from, to int, net float64) brokerSummary {
		b := brokerSummary{Ticker: "PTBA", Start: s.bars[from].Date, End: s.bars[to].Date, Investor: "foreign", Mode: "net", Market: "regular"}
		if net >= 0 {
			b.Buy = []brokerLine{{Broker: "BK", Value: net, Avg: s.closes[n] * 2}}
		} else {
			b.Sell = []brokerLine{{Broker: "ZP", Value: -net}}
		}
		return b
	}
	// Buying in the week and the month, selling over three months: +1 +1 +1 −1.
	list := []brokerSummary{sum(n-59, n, -0.05*turn(60)), sum(n-19, n, 0.03*turn(20)), sum(n-4, n, 0.05*turn(5)), sum(n, n, 1e9)}
	for i := range list {
		list[i].ID = int64(i + 1)
	}
	r, _ := recommendFor(v, "PTBA", list, nil)
	if r.Flow == nil || r.Broker != 2 || r.Score != r.Technical+2 {
		t.Fatalf("broker %v, parts %+v", r.Broker, r.Parts)
	}
	text := ""
	for _, p := range r.Parts {
		text += p.Text + "\n"
	}
	for _, want := range []string{"Asing hari ini net beli 1,0 M", "konsisten akumulasi", "Asing 3 bulan net jual"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q in\n%s", want, text)
		}
	}

	// Strong flow can't make a buy candidate of a weak chart.
	bars := append([]Bar{}, s.bars...)
	for i := range bars {
		bars[i].Close = 2000 - float64(i)*2
	}
	falling := analyze("PTBA", bars)
	weak := &marketView{Day: last, Stocks: map[string]*series{"PTBA": falling}, Tickers: []string{"PTBA"}, Market: v.Market}
	r2, _ := recommendFor(weak, "PTBA", list, nil)
	if r2.Broker < 2 || r2.Technical >= minTechnicalForBuy || r2.Label == recBuy {
		t.Errorf("falling stock: technical %v broker %v label %s", r2.Technical, r2.Broker, r2.Label)
	}
	if got := pasteTargets([]recommendation{{Ticker: "A", Technical: 1}, {Ticker: "B", Technical: 2}, {Ticker: "C", Technical: 4}}); len(got) != 2 || got[0].Ticker != "C" {
		t.Errorf("targets %v", got)
	}
}

func TestSplitPastes(t *testing.T) {
	one := "Broker Summary\nTop Broker\nBBRI\n"
	if got := splitPastes(one + "\n" + one + one); len(got) != 3 {
		t.Errorf("got %d chunks", len(got))
	}
	if got := splitPastes("BBRI\n01 Nov 26"); len(got) != 1 {
		t.Errorf("no title: %d chunks", len(got))
	}
}
