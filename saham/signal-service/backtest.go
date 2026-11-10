package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// historyStart is how far back the backtest downloads prices, so that signals from 2019 have
// their MA200.
var historyStart = time.Date(2017, 6, 1, 0, 0, 0, 0, wib)

// The rules the backtest replays, and the random entries each is compared with. Pullback and
// breakout share their exits, so they share their comparison.
var (
	tradeRules = []string{strategyPull, strategyBrk, strategyRSI2, strategyBrkTrail}
	baseRules  = []string{strategyBrk, strategyRSI2, strategyBrkTrail}
)

type trade struct {
	ticker   string
	strategy string
	signal   time.Time
	Outcome
}

// runBacktest replays the rules on past prices and prints how their trades did. Only bars
// up to -to are read, so a backtest of one period never sees the prices after it.
func runBacktest(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("backtest", flag.ContinueOnError)
	fromFlag := flags.String("from", "2019-01-01", "first day a signal may come from")
	toFlag := flags.String("to", time.Now().In(wib).Format(time.DateOnly), "last day of prices to read")
	tickers := flags.String("tickers", strings.Join(lq45, ","), "comma-separated tickers")
	market := flags.String("market", marketAny, "market filter: none or ma200")
	universe := flags.String("universe", "today", "today: today's LQ45 for every day; history: the LQ45 of each day")
	detail := flags.Bool("detail", false, "print each month's momentum picks")
	cache := flags.String("cache", "", "folder to keep downloaded prices in, to rerun without Yahoo")
	if err := flags.Parse(args); err != nil {
		return err
	}
	from, err := time.ParseInLocation(time.DateOnly, *fromFlag, wib)
	if err != nil {
		return err
	}
	to, err := time.ParseInLocation(time.DateOnly, *toFlag, wib)
	if err != nil {
		return err
	}
	yahoo := newYahooClient()
	now := time.Now().In(wib)

	index, err := loadBars(ctx, yahoo, *cache, ihsg, historyStart, now)
	if err != nil {
		return err
	}
	index = until(index, to)
	allowed := marketFilter(index, *market)

	names := strings.Split(*tickers, ",")
	var members func(time.Time) []string
	if *universe == "history" {
		names = everLQ45()
		members = lq45On
	}

	var trades []trade
	all := map[string]*series{}
	var benchmark []float64
	for _, ticker := range names {
		ticker = strings.TrimSpace(strings.ToUpper(ticker))
		bars, err := loadBars(ctx, yahoo, *cache, ticker, historyStart, now)
		if err != nil {
			if members == nil {
				return err
			}
			// A stock delisted since may be gone from Yahoo.
			fmt.Fprintf(out, "%s: tidak ada data (%v), dilewati\n", ticker, err)
			continue
		}
		bars = until(bars, to)
		first := barAtOrBefore(bars, from.Add(-time.Second)) + 1
		if len(bars) < warmupBars+20 || first >= len(bars) {
			fmt.Fprintf(out, "%s: hanya %d hari data sampai %s, dilewati\n", ticker, len(bars), *toFlag)
			continue
		}
		s := analyze(ticker, bars)
		s.market = allowed
		s.members = members
		all[ticker] = s
		benchmark = append(benchmark, bars[len(bars)-1].Close/bars[max(first, warmupBars)].Open-1)
		trades = append(trades, replay(s, max(first, warmupBars))...)
	}
	fmt.Fprintf(out, "Filter pasar: %s, daftar saham: %s, %d saham ada datanya\n", *market, *universe, len(all))
	report(out, trades, benchmark, from, to)
	if *detail {
		momentumDetail = out
	}
	reportMomentum(out, backtestMomentum(all, index, allowed, from, to))
	return nil
}

// until drops the bars after day to.
func until(bars []Bar, to time.Time) []Bar {
	return bars[:barAtOrBefore(bars, to)+1]
}

// replay walks a stock's bars from bar first, holding at most one trade per rule at a time.
// Every 10th day it also enters at random with each rule's exits.
func replay(s *series, first int) []trade {
	var trades []trade
	next := map[string]int{}
	for i := first; i < len(s.bars)-1; i++ {
		signals := s.signalsAt(i)
		if (i-first)%10 == 0 && s.marketAllows(i) && !isNaN(s.atr14[i]) {
			for _, rule := range baseRules {
				signals = append(signals, s.signal(i, strategyBasePrefx+rule))
			}
		}
		if !s.inIndex(i) {
			continue
		}
		for _, sig := range signals {
			if i < next[sig.Strategy] {
				continue
			}
			outcome := simulate(s, i, sig)
			next[sig.Strategy] = outcome.ExitIndex + 1
			if outcome.Status == statusSkipped || outcome.Status == statusWaiting {
				continue
			}
			trades = append(trades, trade{ticker: s.ticker, strategy: sig.Strategy, signal: sig.Date, Outcome: outcome})
		}
	}
	return trades
}

func isNaN(f float64) bool { return f != f }

func report(out io.Writer, trades []trade, benchmark []float64, from, to time.Time) {
	fmt.Fprintf(out, "Sinyal %s s.d. %s, biaya beli %.2f%% dan jual %.2f%% sudah dihitung\n\n",
		from.Format(time.DateOnly), to.Format(time.DateOnly), buyFee*100, sellFee*100)
	fmt.Fprintf(out, "%-22s %6s %7s %8s %8s %6s %6s %8s %s\n",
		"Aturan", "Trade", "Menang", "Rata2", "Median", "PF", "Hari", "Total", "Keluar")
	byStrategy := map[string][]trade{}
	for _, t := range trades {
		byStrategy[t.strategy] = append(byStrategy[t.strategy], t)
	}
	names := slices.Clone(tradeRules)
	for _, r := range baseRules {
		names = append(names, strategyBasePrefx+r)
	}
	for _, name := range names {
		list := byStrategy[name]
		if len(list) == 0 {
			fmt.Fprintf(out, "%-22s %6d\n", name, 0)
			continue
		}
		s := summarize(list)
		var exits []string
		for _, st := range []string{statusTarget, statusStop, statusExit, statusTime, statusOpen} {
			if s.exits[st] > 0 {
				exits = append(exits, fmt.Sprintf("%s %d", st, s.exits[st]))
			}
		}
		fmt.Fprintf(out, "%-22s %6d %6.1f%% %7.2f%% %7.2f%% %6.2f %6.1f %7.0f%% %s\n",
			name, s.n, s.winRate*100, s.mean*100, s.median*100, s.profitFactor, s.days, s.sum*100,
			strings.Join(exits, ", "))
	}
	fmt.Fprintln(out, "\nPer tahun (jumlah trade, menang, rata-rata per trade):")
	for _, name := range names {
		perYear := map[int][]trade{}
		for _, t := range byStrategy[name] {
			perYear[t.EntryDate.Year()] = append(perYear[t.EntryDate.Year()], t)
		}
		var years []int
		for y := range perYear {
			years = append(years, y)
		}
		slices.Sort(years)
		var parts []string
		for _, y := range years {
			s := summarize(perYear[y])
			parts = append(parts, fmt.Sprintf("%d: %d, %.0f%%, %+.2f%%", y, s.n, s.winRate*100, s.mean*100))
		}
		fmt.Fprintf(out, "  %-22s %s\n", name, strings.Join(parts, " | "))
	}
	if len(benchmark) > 0 {
		fmt.Fprintf(out, "\nBeli dan tahan tiap saham selama periode yang sama: rata-rata %+.1f%% per saham\n", mean(benchmark)*100)
	}
	fmt.Fprintln(out, "PF = profit factor (total untung / total rugi). Total = jumlah return semua trade, bukan return portofolio.")
}

func reportMomentum(out io.Writer, curves []curve) {
	if len(curves) == 0 {
		return
	}
	fmt.Fprintf(out, "\nMomentum bulanan, %s s.d. %s (%d bulan):\n",
		curves[0].dates[0].Format(time.DateOnly), curves[0].dates[len(curves[0].dates)-1].Format(time.DateOnly),
		len(curves[0].values)-1)
	fmt.Fprintf(out, "%-24s %8s %9s %9s %10s\n", "Portofolio", "Total", "Per tahun", "Drawdown", "Bulan naik")
	for _, c := range curves {
		fmt.Fprintf(out, "%-24s %7.1f%% %8.1f%% %8.1f%% %9.0f%%\n",
			c.name, c.total()*100, c.cagr()*100, c.maxDrawdown()*100, c.upMonths()*100)
	}
}

type summary struct {
	n                                        int
	winRate, mean, median, profitFactor, sum float64
	days                                     float64
	exits                                    map[string]int
}

func summarize(list []trade) summary {
	s := summary{n: len(list), exits: map[string]int{}}
	returns := make([]float64, 0, len(list))
	var wins, gains, losses, days float64
	for _, t := range list {
		returns = append(returns, t.Return)
		s.sum += t.Return
		days += float64(t.Days)
		s.exits[t.Status]++
		if t.Return > 0 {
			wins++
			gains += t.Return
		} else {
			losses -= t.Return
		}
	}
	slices.Sort(returns)
	s.winRate = wins / float64(s.n)
	s.mean = s.sum / float64(s.n)
	s.median = returns[len(returns)/2]
	s.days = days / float64(s.n)
	if losses > 0 {
		s.profitFactor = gains / losses
	}
	return s
}

// loadBars downloads a stock's bars, keeping them in the cache folder when one is given.
func loadBars(ctx context.Context, yahoo *yahooClient, cache, ticker string, from, to time.Time) ([]Bar, error) {
	var path string
	if cache != "" {
		path = filepath.Join(cache, strings.TrimPrefix(ticker, "^")+".json")
		data, err := os.ReadFile(path)
		if err == nil {
			var bars []Bar
			if err := json.Unmarshal(data, &bars); err != nil {
				return nil, err
			}
			for i := range bars {
				bars[i].Date = bars[i].Date.In(wib)
			}
			return bars, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	bars, err := yahoo.Daily(ctx, ticker, from, to)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.MkdirAll(cache, 0o755); err != nil {
			return nil, err
		}
		data, err := json.Marshal(bars)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
	}
	return bars, nil
}
