package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The daily job runs after each trading day's close. It reads prices, moves the paper trades
// on, records today's signals and the month's momentum picks, and sends the brief.

// historyMonths of prices are read each run: enough for MA200 and momentum's 6 months, and for
// every paper trade still open, which last 40 trading days at most.
const historyMonths = 18

// Results of a run, for the metrics.
const (
	resultOK      = "ok"
	resultSkipped = "skipped" // no trading today, or already done
	resultError   = "error"
)

var errBusy = errors.New("a run is already going")

type sender interface {
	Send(ctx context.Context, text string) error
}

type job struct {
	store    *store
	yahoo    *yahooClient
	telegram sender
	tickers  []string
	now      func() time.Time
	mu       sync.Mutex
	// view is the latest prices for the web pages, nil until first read.
	view atomic.Pointer[marketView]
}

// event is a paper trade that was bought or sold today, for the brief.
type event struct {
	Signal  Signal
	Outcome Outcome
}

// dayReport is everything the brief shows.
type dayReport struct {
	Day       time.Time
	Market    marketState
	Screener  []Signal
	Bought    []event
	Sold      []event
	Stats     []ruleStats
	Momentum  momentumState
	Started   time.Time
	Missing   []string // stocks Yahoo had no prices for today
	Suspended []string
}

type marketState struct {
	Close, Change1d, Change20d, MA50, MA200 float64
	Healthy                                 bool
	Above200, Counted                       int
}

type momentumState struct {
	Total    float64 // since the first month, after costs
	Months   int     // finished months
	Current  []string
	Since    time.Time
	Running  float64
	MarketOK bool
	HasMonth bool
}

// Run does the day's work. Unless forced, it does nothing on a day without trading or a day
// already done.
func (j *job) Run(ctx context.Context, force bool) (string, error) {
	if !j.mu.TryLock() {
		return "", errBusy
	}
	defer j.mu.Unlock()

	now := j.now().In(wib)
	from := now.AddDate(0, -historyMonths, 0)
	index, err := j.yahoo.Daily(ctx, ihsg, from, now)
	if err != nil {
		return "", err
	}
	if len(index) < warmupBars+21 {
		return "", fmt.Errorf("only %d days of IHSG", len(index))
	}
	day := index[len(index)-1].Date
	if !force {
		if !day.Equal(dayOf(now)) {
			return resultSkipped, nil
		}
		done, err := j.store.HasRun(ctx, day)
		if err != nil {
			return "", err
		}
		if done {
			return resultSkipped, nil
		}
	}

	all, missing := j.loadAll(ctx, from, now)
	if len(missing) > len(j.tickers)/5 {
		return "", fmt.Errorf("no prices for %d of %d stocks: %s", len(missing), len(j.tickers), strings.Join(missing, ", "))
	}
	j.view.Store(buildView(j.tickers, index, all, missing, time.Now()))
	healthy := marketFilter(index, marketMA200)
	r := dayReport{Day: day, Missing: missing, Market: readMarket(index, all)}

	if err := j.moveTrades(ctx, all, day, &r); err != nil {
		return "", err
	}
	if err := j.addSignals(ctx, all, day, healthy, &r); err != nil {
		return "", err
	}
	started, ok, err := j.store.FirstDay(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		started = day
	}
	r.Started = started
	if r.Momentum, err = j.momentum(ctx, all, index, healthy, started, day); err != nil {
		return "", err
	}
	if r.Stats, err = j.store.Stats(ctx); err != nil {
		return "", err
	}

	brief := composeBrief(r)
	if err := j.telegram.Send(ctx, brief); err != nil {
		return "", fmt.Errorf("sending the brief: %w", err)
	}
	if err := j.store.SaveRun(ctx, day, brief); err != nil {
		return "", err
	}
	return resultOK, nil
}

// loadAll reads every stock's prices, four at a time, and lists the ones Yahoo didn't give.
func (j *job) loadAll(ctx context.Context, from, to time.Time) (map[string]*series, []string) {
	type result struct {
		ticker string
		bars   []Bar
		err    error
	}
	work := make(chan string)
	results := make(chan result)
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for ticker := range work {
				bars, err := j.yahoo.Daily(ctx, ticker, from, to)
				results <- result{ticker, bars, err}
			}
		})
	}
	go func() {
		for _, t := range j.tickers {
			work <- t
		}
		close(work)
		workers.Wait()
		close(results)
	}()
	all := map[string]*series{}
	var missing []string
	for r := range results {
		if r.err != nil || len(r.bars) == 0 {
			slog.Warn("no prices", "ticker", r.ticker, "error", r.err)
			missing = append(missing, r.ticker)
			continue
		}
		all[r.ticker] = analyze(r.ticker, r.bars)
	}
	return all, missing
}

func readMarket(index []Bar, all map[string]*series) marketState {
	s := analyze(ihsg, index)
	last := len(index) - 1
	m := marketState{
		Close:     index[last].Close,
		Change1d:  index[last].Close/index[last-1].Close - 1,
		Change20d: index[last].Close/index[last-20].Close - 1,
		MA50:      s.sma50[last],
		MA200:     s.sma200[last],
	}
	m.Healthy = m.Close > m.MA200
	for _, st := range all {
		i := len(st.bars) - 1
		if math.IsNaN(st.sma200[i]) {
			continue
		}
		m.Counted++
		if st.bars[i].Close > st.sma200[i] {
			m.Above200++
		}
	}
	return m
}

// moveTrades follows each waiting or open trade through the bars since its signal.
func (j *job) moveTrades(ctx context.Context, all map[string]*series, day time.Time, r *dayReport) error {
	trades, err := j.store.Unfinished(ctx)
	if err != nil {
		return err
	}
	for _, t := range trades {
		s := all[t.Signal.Ticker]
		if s == nil {
			continue
		}
		i := barAtOrBefore(s.bars, t.Signal.Date)
		if i < 0 || !s.bars[i].Date.Equal(t.Signal.Date) {
			slog.Warn("signal day missing from prices", "ticker", t.Signal.Ticker, "day", t.Signal.Date)
			continue
		}
		// A dividend since the signal scales the older prices down; scale the levels with them.
		sig := t.Signal
		scale := s.bars[i].Close / sig.Close
		sig.MaxEntry *= scale
		sig.Stop *= scale
		sig.Target *= scale
		o := simulate(s, i, sig)
		if err := j.store.Update(ctx, t.ID, o); err != nil {
			return err
		}
		if strings.HasPrefix(sig.Strategy, strategyBasePrefx) {
			continue
		}
		if o.EntryDate.Equal(day) {
			r.Bought = append(r.Bought, event{sig, o})
		}
		if o.Closed() && o.Status != statusSkipped && o.ExitDate.Equal(day) {
			r.Sold = append(r.Sold, event{sig, o})
		}
	}
	return nil
}

// addSignals records the signals today's close makes, and the random entries to compare them
// with, one trade per rule and stock at a time.
func (j *job) addSignals(ctx context.Context, all map[string]*series, day time.Time, healthy map[string]bool, r *dayReport) error {
	marketOK := healthy[day.Format(time.DateOnly)]
	for _, ticker := range j.tickers {
		s := all[ticker]
		if s == nil {
			continue
		}
		last := len(s.bars) - 1
		if !s.bars[last].Date.Equal(day) {
			r.Suspended = append(r.Suspended, ticker)
			continue
		}
		signals := s.signalsAt(last)
		for _, sig := range signals {
			r.Screener = append(r.Screener, sig)
		}
		if randomDay(ticker, day) && last >= warmupBars && !math.IsNaN(s.atr14[last]) {
			for _, rule := range baseRules {
				signals = append(signals, s.signal(last, strategyBasePrefx+rule))
			}
		}
		for _, sig := range signals {
			busy, err := j.store.HasUnfinished(ctx, sig.Strategy, ticker)
			if err != nil {
				return err
			}
			if busy {
				continue
			}
			if err := j.store.AddSignal(ctx, sig, marketOK); err != nil {
				return err
			}
		}
	}
	return nil
}

// randomDay picks about one day in ten for each stock, spread so that not every stock enters
// on the same day, to give the rules something to beat.
func randomDay(ticker string, day time.Time) bool {
	h := fnv.New32a()
	h.Write([]byte(ticker))
	days := day.Unix() / 86400
	return (days+int64(h.Sum32()%10))%10 == 0
}

// momentum records the picks after each month's last close since the job started, settles the
// months that are over and reports how the rule stands.
func (j *job) momentum(ctx context.Context, all map[string]*series, index []Bar, healthy map[string]bool, started, day time.Time) (momentumState, error) {
	for i := 0; i+1 < len(index); i++ {
		d := index[i].Date
		if d.Before(started) || index[i+1].Date.Month() == d.Month() {
			continue
		}
		m := momentumMonth{MonthEnd: d, Picks: momentumPicks(all, d, momentumTop), MarketOK: healthy[d.Format(time.DateOnly)]}
		if err := j.store.AddMomentumMonth(ctx, m); err != nil {
			return momentumState{}, err
		}
	}
	months, err := j.store.MomentumMonths(ctx)
	if err != nil {
		return momentumState{}, err
	}
	state := momentumState{}
	value := 1.0
	var held []string
	for k, m := range months {
		picks := m.Picks
		if !m.MarketOK {
			picks = nil
		}
		ret := 0.0
		if k+1 < len(months) {
			if m.ReturnPct == nil {
				ret = monthReturn(all, held, picks, m.MonthEnd, months[k+1].MonthEnd)
				if err := j.store.SetMomentumReturn(ctx, m.MonthEnd, ret*100); err != nil {
					return momentumState{}, err
				}
			} else {
				ret = *m.ReturnPct / 100
			}
			value *= 1 + ret
			state.Months++
		} else {
			state.HasMonth = true
			state.Current = m.Picks
			state.MarketOK = m.MarketOK
			state.Since = m.MonthEnd
			state.Running = monthReturn(all, held, picks, m.MonthEnd, day)
		}
		held = picks
	}
	state.Total = value*(1+state.Running) - 1
	return state, nil
}

// monthReturn is what holding picks from one month end to the next made after costs, having
// held the previous month's picks before.
func monthReturn(all map[string]*series, held, picks []string, start, end time.Time) float64 {
	ret := 0.0
	for _, t := range picks {
		if s := all[t]; s != nil {
			ret += holdReturn(s, start, end) / momentumTop
		}
		if !contains(held, t) {
			ret -= buyFee / momentumTop
		}
	}
	for _, t := range held {
		if !contains(picks, t) {
			ret -= sellFee / momentumTop
		}
	}
	return ret
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// schedule runs the job each weekday at the given time of day, WIB, and once at start when
// today's run is due but missing, after a restart or an outage. A failed run is tried again
// every 15 minutes, four times.
func (j *job) schedule(ctx context.Context, at time.Duration) {
	now := j.now().In(wib)
	if isWeekday(now) && now.Sub(dayOf(now)) >= at {
		j.runWithRetries(ctx)
	}
	for {
		next := nextRun(j.now(), at)
		slog.Info("next run", "at", next)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		j.runWithRetries(ctx)
	}
}

func (j *job) runWithRetries(ctx context.Context) {
	for attempt := range 5 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Minute):
			}
		}
		if err := j.runAndRecord(ctx, false); err == nil || errors.Is(err, errBusy) {
			return
		}
	}
}

// runAndRecord runs the job and updates its metrics and log.
func (j *job) runAndRecord(ctx context.Context, force bool) error {
	start := time.Now()
	result, err := j.Run(ctx, force)
	if err != nil {
		if !errors.Is(err, errBusy) {
			jobRuns.WithLabelValues(resultError).Inc()
			slog.Error("run failed", "error", err)
		}
		return err
	}
	jobRuns.WithLabelValues(result).Inc()
	jobLastOK.SetToCurrentTime()
	jobDuration.Set(time.Since(start).Seconds())
	slog.Info("run finished", "result", result, "seconds", time.Since(start).Seconds())
	return nil
}

func isWeekday(t time.Time) bool {
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

// nextRun is the next weekday, at time of day at in WIB, after now.
func nextRun(now time.Time, at time.Duration) time.Time {
	next := dayOf(now).Add(at)
	for !next.After(now) || !isWeekday(next) {
		next = dayOf(next.AddDate(0, 0, 1)).Add(at)
	}
	return next
}
