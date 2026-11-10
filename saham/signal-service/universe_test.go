package main

import (
	"slices"
	"testing"
)

func TestLQ45HistoryHas45EveryPeriod(t *testing.T) {
	for _, p := range lq45History {
		if len(p.tickers) != 45 {
			t.Errorf("%s has %d stocks", p.from.Format("2006-01-02"), len(p.tickers))
		}
	}
}

func TestEveryPublishedListMatches(t *testing.T) {
	for _, c := range lq45Changes {
		if c.fullList == "" {
			continue
		}
		got := lq45On(c.from)
		want := slices.Sorted(slices.Values(list(c.fullList)))
		if !slices.Equal(got, want) {
			t.Errorf("%s: built %v, published %v", c.from.Format("2006-01-02"), got, want)
		}
	}
}

func TestTodaysListIsTheLastPeriod(t *testing.T) {
	last := lq45History[len(lq45History)-1].tickers
	if !slices.Equal(last, slices.Sorted(slices.Values(lq45))) {
		t.Errorf("lq45 and the history's last period differ: %v vs %v", lq45, last)
	}
}
