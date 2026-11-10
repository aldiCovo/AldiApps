package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

// risingSeries makes a steady uptrend with a little daily wobble, so RSI stays below 75.
func risingSeries(ticker string, n int, last time.Time) *series {
	var bars []Bar
	d := last.AddDate(0, 0, -n+1)
	for i := range n {
		c := 1000 * math.Pow(1.0015, float64(i)) * (1 + 0.01*math.Sin(float64(i)))
		bars = append(bars, Bar{Date: d.AddDate(0, 0, i), Open: c * 0.995, High: c * 1.01, Low: c * 0.99, Close: c, Volume: 1_000_000})
	}
	return analyze(ticker, bars)
}

func TestRecommend(t *testing.T) {
	last := time.Date(2026, 11, 10, 0, 0, 0, 0, wib)
	s := risingSeries("BBRI", 400, last)
	v := &marketView{Day: last, Stocks: map[string]*series{"BBRI": s}, Tickers: []string{"BBRI"},
		Market: marketState{Healthy: true, MA50: 7500, MA200: 7000}}
	close := s.closes[len(s.closes)-1]

	// Technical only: uptrend +2, momentum +1, IHSG +1.
	r, ok := recommend(v, "BBRI", nil, nil)
	if !ok || r.Technical != 4 || r.Broker != 0 || r.Label != recBuy || !r.HasPlan {
		t.Fatalf("technical only: %+v", r)
	}
	if r.Stop >= close || r.Target <= close || r.RewardRisk() < 1.4 {
		t.Errorf("plan: close %.0f stop %.0f target %.0f", close, r.Stop, r.Target)
	}

	// A foreign summary with heavy net selling and Stockbit's Dist takes it down to Tunggu.
	period := func(b brokerSummary) brokerSummary {
		b.Ticker, b.Start, b.End = "BBRI", last.AddDate(0, 0, -6), last
		return b
	}
	sell := period(brokerSummary{Investor: "foreign", AccDist: "Dist",
		Buy:  []brokerLine{{Broker: "YP", Value: 1e9, Avg: close * 1.2}},
		Sell: []brokerLine{{Broker: "ZP", Value: 1e9 + 0.1*close*1e6*7}}})
	r, _ = recommend(v, "BBRI", &sell, nil)
	if r.Broker != -3 || r.Label != recPantauOr(r.Score) {
		t.Errorf("selling: broker %v score %v label %s", r.Broker, r.Score, r.Label)
	}
	if !strings.Contains(r.Reasons(1)[0].Text, "Tren naik") && !strings.Contains(r.Reasons(1)[0].Text, "Net investor asing") {
		t.Errorf("biggest reason: %+v", r.Reasons(1))
	}

	// Estimated from broker codes it counts at most one point.
	est := sell
	est.Investor = "all"
	r, _ = recommend(v, "BBRI", &est, map[string]bool{"ZP": true})
	if r.Broker != -2 {
		t.Errorf("estimated selling: broker %v, parts %+v", r.Broker, r.Parts)
	}

	// Buying near the top buyer's average adds up.
	buy := period(brokerSummary{Investor: "foreign", AccDist: "Big Acc",
		Buy:  []brokerLine{{Broker: "BK", Value: 1e9 + 0.1*close*1e6*7, Avg: close}},
		Sell: []brokerLine{{Broker: "CC", Value: 1e9}}})
	r, _ = recommend(v, "BBRI", &buy, nil)
	if r.Broker != 4 || r.Score != 8 || r.Label != recBuy {
		t.Errorf("buying: broker %v score %v", r.Broker, r.Score)
	}

	// A falling market and a downtrend: Hindari, no plan.
	down := &marketView{Day: last, Stocks: map[string]*series{"BBRI": s}, Tickers: []string{"BBRI"}}
	falling := risingSeries("TLKM", 400, last)
	for i := range falling.bars {
		falling.bars[i].Close = 2000 - float64(i)*2
	}
	falling = analyze("TLKM", falling.bars)
	down.Stocks["TLKM"] = falling
	down.Tickers = append(down.Tickers, "TLKM")
	r, _ = recommend(down, "TLKM", nil, nil)
	if r.Label != recAvoid || r.HasPlan {
		t.Errorf("downtrend: %+v", r)
	}
	if all := recommendAll(down, nil, nil); len(all) != 2 || all[0].Ticker != "BBRI" {
		t.Errorf("recommendAll order: %v", all)
	}
}

func recPantauOr(score float64) string {
	switch {
	case score >= 4:
		return recBuy
	case score >= 2:
		return recWatch
	case score >= 0:
		return recWait
	}
	return recAvoid
}

func TestPickBroker(t *testing.T) {
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, wib)
	at := func(id int64, end time.Time, investor, market string) brokerSummary {
		return brokerSummary{ID: id, Ticker: "BBRI", Start: end.AddDate(0, 0, -5), End: end, Investor: investor, Market: market}
	}
	list := []brokerSummary{
		at(1, day.AddDate(0, 0, -20), "foreign", "regular"), // too old
		at(2, day, "all", "regular"),
		at(3, day, "foreign", "regular"),
		at(4, day, "domestic", "regular"),
		at(5, day.AddDate(0, 0, -3), "foreign", "regular"),
		{ID: 6, Ticker: "BBCA", End: day, Investor: "foreign", Market: "regular"},
	}
	if b := pickBroker(list, "BBRI", day); b == nil || b.ID != 3 {
		t.Errorf("picked %+v", b)
	}
	if b := pickBroker(list[:1], "BBRI", day); b != nil {
		t.Errorf("an old summary was picked: %+v", b)
	}
}
