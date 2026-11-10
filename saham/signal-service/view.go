package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"
)

// marketView is the latest prices of IHSG and every stock, with their indicators, for the web
// pages. The job makes a new one each time it reads prices: after each run and once at start.
type marketView struct {
	Day      time.Time // the last trading day in the prices
	Loaded   time.Time
	Index    *series
	Market   marketState
	Stocks   map[string]*series
	Tickers  []string // in the order of the LQ45 list, those with prices
	Screener []Signal // the signals the last close made
	Missing  []string
}

func buildView(tickers []string, index []Bar, all map[string]*series, missing []string, loaded time.Time) *marketView {
	v := &marketView{
		Day:     index[len(index)-1].Date,
		Loaded:  loaded,
		Index:   analyze(ihsg, index),
		Market:  readMarket(index, all),
		Stocks:  all,
		Missing: missing,
	}
	for _, t := range tickers {
		s := all[t]
		if s == nil {
			continue
		}
		v.Tickers = append(v.Tickers, t)
		last := len(s.bars) - 1
		if s.bars[last].Date.Equal(v.Day) {
			v.Screener = append(v.Screener, s.signalsAt(last)...)
		}
	}
	return v
}

// refreshView reads all prices without recording anything, for the pages after a start.
func (j *job) refreshView(ctx context.Context) error {
	now := j.now().In(wib)
	from := now.AddDate(0, -historyMonths, 0)
	index, err := j.yahoo.Daily(ctx, ihsg, from, now)
	if err != nil {
		return err
	}
	if len(index) < warmupBars+21 {
		return fmt.Errorf("only %d days of IHSG", len(index))
	}
	all, missing := j.loadAll(ctx, from, now)
	j.view.Store(buildView(j.tickers, index, all, missing, time.Now()))
	slog.Info("prices loaded for the pages", "day", index[len(index)-1].Date.Format(time.DateOnly), "stocks", len(all))
	return nil
}

// keepViewFresh loads the prices at start, trying again every few minutes until it works.
func (j *job) keepViewFresh(ctx context.Context) {
	for j.view.Load() == nil {
		if err := j.refreshView(ctx); err != nil {
			slog.Warn("loading prices for the pages", "error", err)
		} else {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// stockRow is one line of the stock list.
type stockRow struct {
	Ticker              string
	Close               float64
	Change1d, Change20d float64
	VsMA200             float64 // NaN before 200 days
	RSI14               float64
	Momentum            float64 // the 6-month return without the last month, NaN when too short
	Signals             []string
}

func (v *marketView) rows() []stockRow {
	signals := map[string][]string{}
	for _, s := range v.Screener {
		signals[s.Ticker] = append(signals[s.Ticker], s.Strategy)
	}
	var out []stockRow
	for _, t := range v.Tickers {
		s := v.Stocks[t]
		i := len(s.bars) - 1
		r := stockRow{
			Ticker:    t,
			Close:     s.closes[i],
			Change1d:  changeOver(s.closes, i, 1),
			Change20d: changeOver(s.closes, i, 20),
			VsMA200:   s.closes[i]/s.sma200[i] - 1,
			RSI14:     s.rsi14[i],
			Momentum:  math.NaN(),
			Signals:   signals[t],
		}
		if i >= momentumLookback {
			r.Momentum = s.closes[i-momentumSkip]/s.closes[i-momentumLookback] - 1
		}
		out = append(out, r)
	}
	return out
}

func changeOver(closes []float64, i, days int) float64 {
	if i < days {
		return math.NaN()
	}
	return closes[i]/closes[i-days] - 1
}

// sortRows orders the stock list by a column, the biggest first, NaN last. Anything else keeps
// the LQ45 order, which is by code.
func sortRows(rows []stockRow, by string) {
	key := map[string]func(stockRow) float64{
		"hari":     func(r stockRow) float64 { return r.Change1d },
		"20hari":   func(r stockRow) float64 { return r.Change20d },
		"ma200":    func(r stockRow) float64 { return r.VsMA200 },
		"rsi":      func(r stockRow) float64 { return r.RSI14 },
		"momentum": func(r stockRow) float64 { return r.Momentum },
	}[by]
	if key == nil {
		return
	}
	sort.SliceStable(rows, func(a, b int) bool {
		x, y := key(rows[a]), key(rows[b])
		if math.IsNaN(y) {
			return !math.IsNaN(x)
		}
		return x > y
	})
}

// momentumRanking is today's momentum order, as the rule would pick if the month ended now.
func (v *marketView) momentumRanking(n int) []stockRow {
	rows := v.rows()
	sortRows(rows, "momentum")
	var out []stockRow
	for _, r := range rows {
		if len(out) == n || math.IsNaN(r.Momentum) {
			break
		}
		out = append(out, r)
	}
	return out
}
