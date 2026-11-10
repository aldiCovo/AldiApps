package main

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// Momentum holds the stocks that rose most over 6 months, leaving out the last month, which
// tends to reverse. It trades once a month: after the month's last close it picks the top
// stocks, and it buys and sells at the next open.
const (
	momentumTop      = 5
	momentumLookback = 126 // trading days, about 6 months
	momentumSkip     = 21  // trading days, about 1 month
)

// momentumDetail, when set, gets each month's picks and returns.
var momentumDetail io.Writer

// momentumPicks ranks the stocks on day d and returns the strongest, best first.
func momentumPicks(all map[string]*series, d time.Time, top int) []string {
	type scored struct {
		ticker string
		score  float64
	}
	var ranked []scored
	for ticker, s := range all {
		i := barAtOrBefore(s.bars, d)
		if i < 0 || !s.inIndex(i) {
			continue
		}
		// A stock that hasn't traded for a week may be suspended.
		if i < momentumLookback || d.Sub(s.bars[i].Date) > 7*24*time.Hour {
			continue
		}
		score := s.closes[i-momentumSkip]/s.closes[i-momentumLookback] - 1
		ranked = append(ranked, scored{ticker, score})
	}
	sort.Slice(ranked, func(a, b int) bool {
		if ranked[a].score != ranked[b].score {
			return ranked[a].score > ranked[b].score
		}
		return ranked[a].ticker < ranked[b].ticker
	})
	picks := []string{}
	for _, r := range ranked[:min(top, len(ranked))] {
		picks = append(picks, r.ticker)
	}
	return picks
}

// curve is a portfolio's value at the end of each month, starting at 1.
type curve struct {
	name   string
	values []float64
	dates  []time.Time
}

func (c curve) total() float64 { return c.values[len(c.values)-1] - 1 }

func (c curve) cagr() float64 {
	years := c.dates[len(c.dates)-1].Sub(c.dates[0]).Hours() / 24 / 365.25
	if years <= 0 {
		return 0
	}
	return math.Pow(c.values[len(c.values)-1], 1/years) - 1
}

func (c curve) maxDrawdown() float64 {
	peak, worst := c.values[0], 0.0
	for _, v := range c.values {
		peak = math.Max(peak, v)
		worst = math.Min(worst, v/peak-1)
	}
	return worst
}

func (c curve) upMonths() float64 {
	up := 0
	for i := 1; i < len(c.values); i++ {
		if c.values[i] > c.values[i-1] {
			up++
		}
	}
	return float64(up) / float64(len(c.values)-1)
}

// backtestMomentum replays the monthly rule from one month end after from until to. It also
// returns two portfolios to beat: all the stocks in equal parts, rebalanced monthly at no
// cost, and IHSG itself.
func backtestMomentum(all map[string]*series, index []Bar, market map[string]bool, from, to time.Time) []curve {
	var monthEnds []time.Time
	for i := 0; i+1 < len(index); i++ {
		d := index[i].Date
		if d.Before(from) || d.After(to) {
			continue
		}
		if index[i+1].Date.Month() != d.Month() {
			monthEnds = append(monthEnds, d)
		}
	}
	if len(monthEnds) < 2 {
		return nil
	}
	momentum := curve{name: "momentum top 5", values: []float64{1}, dates: monthEnds[:1]}
	equal := curve{name: "semua saham bobot rata", values: []float64{1}, dates: monthEnds[:1]}
	ihsgCurve := curve{name: "IHSG", values: []float64{1}, dates: monthEnds[:1]}
	held := map[string]bool{}
	for m := 0; m+1 < len(monthEnds); m++ {
		start, end := monthEnds[m], monthEnds[m+1]
		var picks []string
		if market == nil || market[start.Format(time.DateOnly)] {
			picks = momentumPicks(all, start, momentumTop)
		}
		// Costs: sell what leaves, buy what comes in, each a fifth of the money.
		now := map[string]bool{}
		cost := 0.0
		for _, t := range picks {
			now[t] = true
			if !held[t] {
				cost += buyFee / float64(momentumTop)
			}
		}
		for t := range held {
			if !now[t] {
				cost += sellFee / float64(momentumTop)
			}
		}
		held = now
		ret := 0.0
		for _, t := range picks {
			ret += holdReturn(all[t], start, end) / float64(momentumTop)
		}
		momentum.add(end, ret-cost)
		if momentumDetail != nil {
			var parts []string
			for _, t := range picks {
				parts = append(parts, fmt.Sprintf("%s %+.1f%%", t, holdReturn(all[t], start, end)*100))
			}
			fmt.Fprintf(momentumDetail, "  %s: %+.1f%%  %s\n", start.Format(time.DateOnly), (ret-cost)*100, strings.Join(parts, ", "))
		}

		var everyone []float64
		for _, s := range all {
			if i := barAtOrBefore(s.bars, start); i >= 0 && s.inIndex(i) && start.Sub(s.bars[i].Date) <= 7*24*time.Hour {
				everyone = append(everyone, holdReturn(s, start, end))
			}
		}
		equal.add(end, mean(everyone))

		a, b := barAtOrBefore(index, start), barAtOrBefore(index, end)
		ihsgCurve.add(end, index[b].Close/index[a].Close-1)
	}
	return []curve{momentum, equal, ihsgCurve}
}

func (c *curve) add(d time.Time, ret float64) {
	c.values = append(c.values, c.values[len(c.values)-1]*(1+ret))
	c.dates = append(c.dates, d)
}

// holdReturn buys at the first open after start and sells at the first open after end, or at
// the last close when the data stops first.
func holdReturn(s *series, start, end time.Time) float64 {
	i := barAtOrBefore(s.bars, start) + 1
	j := barAtOrBefore(s.bars, end) + 1
	if i >= len(s.bars) {
		return 0
	}
	buy := s.bars[i].Open
	sell := s.bars[len(s.bars)-1].Close
	if j < len(s.bars) {
		sell = s.bars[j].Open
	}
	return sell/buy - 1
}

// barAtOrBefore is the index of the last bar on or before d, or -1.
func barAtOrBefore(bars []Bar, d time.Time) int {
	return sort.Search(len(bars), func(i int) bool { return bars[i].Date.After(d) }) - 1
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}
