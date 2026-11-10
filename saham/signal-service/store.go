package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type store struct {
	pool *pgxpool.Pool
}

// paperTrade is a row of paper_trades.
type paperTrade struct {
	ID       int64
	Signal   Signal
	MarketOK bool
	Outcome
}

// HasUnfinished reports whether a rule still waits on or holds a trade in this stock, since a
// rule holds at most one at a time.
func (s *store) HasUnfinished(ctx context.Context, rule, ticker string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM paper_trades
		WHERE rule = $1 AND ticker = $2 AND status IN ('waiting', 'open'))`, rule, ticker).Scan(&exists)
	return exists, err
}

// AddSignal records a new signal as waiting for the next open. A signal already recorded,
// from a run that is repeated, is left as it is.
func (s *store) AddSignal(ctx context.Context, sig Signal, marketOK bool) error {
	reasons := sig.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO paper_trades
		(rule, ticker, signal_date, signal_close, max_entry, stop, target, exit_rule, trail_atr, max_days, reasons, market_ok, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'waiting')
		ON CONFLICT (rule, ticker, signal_date) DO NOTHING`,
		sig.Strategy, sig.Ticker, sig.Date, sig.Close, sig.MaxEntry, sig.Stop, sig.Target, sig.Exit,
		sig.TrailATR, sig.MaxDays, reasons, marketOK)
	return err
}

// Unfinished returns the trades still waiting or open.
func (s *store) Unfinished(ctx context.Context) ([]paperTrade, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, rule, ticker, signal_date, signal_close, max_entry, stop,
		target, exit_rule, trail_atr, max_days, market_ok
		FROM paper_trades WHERE status IN ('waiting', 'open') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (paperTrade, error) {
		var t paperTrade
		var date time.Time
		err := row.Scan(&t.ID, &t.Signal.Strategy, &t.Signal.Ticker, &date, &t.Signal.Close,
			&t.Signal.MaxEntry, &t.Signal.Stop, &t.Signal.Target, &t.Signal.Exit, &t.Signal.TrailATR,
			&t.Signal.MaxDays, &t.MarketOK)
		t.Signal.Date = asDay(date)
		return t, err
	})
}

// Update saves where a trade stands now.
func (s *store) Update(ctx context.Context, id int64, o Outcome) error {
	var returnPct *float64
	if !o.EntryDate.IsZero() {
		r := o.Return * 100
		returnPct = &r
	}
	_, err := s.pool.Exec(ctx, `UPDATE paper_trades SET status = $2, entry_date = $3, entry_price = $4,
		exit_date = $5, exit_price = $6, days = $7, return_pct = $8, updated_at = now() WHERE id = $1`,
		id, o.Status, nullDate(o.EntryDate), nullFloat(o.EntryPrice), nullDate(o.ExitDate),
		nullFloat(o.ExitPrice), o.Days, returnPct)
	return err
}

// ruleStats is how a rule's paper trades went, over all days or only healthy-market ones.
type ruleStats struct {
	Rule     string
	MarketOK *bool // nil for all days
	Closed   int
	Wins     int
	MeanPct  float64
	Open     int
}

// Stats sums the paper trades per rule, for all days and split by the market's health.
func (s *store) Stats(ctx context.Context) ([]ruleStats, error) {
	rows, err := s.pool.Query(ctx, `SELECT rule, market_ok,
		count(*) FILTER (WHERE status IN ('target', 'stop', 'exit', 'time')),
		count(*) FILTER (WHERE status IN ('target', 'stop', 'exit', 'time') AND return_pct > 0),
		coalesce(avg(return_pct) FILTER (WHERE status IN ('target', 'stop', 'exit', 'time')), 0),
		count(*) FILTER (WHERE status = 'open')
		FROM paper_trades GROUP BY GROUPING SETS ((rule), (rule, market_ok)) ORDER BY rule, market_ok NULLS FIRST`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ruleStats, error) {
		var r ruleStats
		err := row.Scan(&r.Rule, &r.MarketOK, &r.Closed, &r.Wins, &r.MeanPct, &r.Open)
		return r, err
	})
}

// FirstDay is the first trading day the job finished, or ok false when it never has.
func (s *store) FirstDay(ctx context.Context) (day time.Time, ok bool, err error) {
	var d *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT min(trading_day) FROM runs`).Scan(&d); err != nil {
		return time.Time{}, false, err
	}
	if d == nil {
		return time.Time{}, false, nil
	}
	return asDay(*d), true, nil
}

// LastRun is the last finished run, or ok false when there is none.
func (s *store) LastRun(ctx context.Context) (day time.Time, finished time.Time, brief string, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT trading_day, finished_at, brief FROM runs ORDER BY trading_day DESC LIMIT 1`).
		Scan(&day, &finished, &brief)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, time.Time{}, "", false, nil
	}
	return asDay(day), finished, brief, err == nil, err
}

// HasRun reports whether the job already finished this trading day.
func (s *store) HasRun(ctx context.Context, day time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE trading_day = $1)`, day).Scan(&exists)
	return exists, err
}

// SaveRun records the day's message. A forced rerun replaces it.
func (s *store) SaveRun(ctx context.Context, day time.Time, brief string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO runs (trading_day, brief) VALUES ($1, $2)
		ON CONFLICT (trading_day) DO UPDATE SET brief = EXCLUDED.brief, finished_at = now()`, day, brief)
	return err
}

type momentumMonth struct {
	MonthEnd  time.Time
	Picks     []string
	MarketOK  bool
	ReturnPct *float64 // nil until the month is over
}

// AddMomentumMonth records a month's picks, once.
func (s *store) AddMomentumMonth(ctx context.Context, m momentumMonth) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO momentum_months (month_end, picks, market_ok) VALUES ($1, $2, $3)
		ON CONFLICT (month_end) DO NOTHING`, m.MonthEnd, m.Picks, m.MarketOK)
	return err
}

// MomentumMonths returns every recorded month, oldest first.
func (s *store) MomentumMonths(ctx context.Context) ([]momentumMonth, error) {
	rows, err := s.pool.Query(ctx, `SELECT month_end, picks, market_ok, return_pct FROM momentum_months ORDER BY month_end`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (momentumMonth, error) {
		var m momentumMonth
		var d time.Time
		err := row.Scan(&d, &m.Picks, &m.MarketOK, &m.ReturnPct)
		m.MonthEnd = asDay(d)
		return m, err
	})
}

// SetMomentumReturn records how a finished month went.
func (s *store) SetMomentumReturn(ctx context.Context, monthEnd time.Time, pct float64) error {
	_, err := s.pool.Exec(ctx, `UPDATE momentum_months SET return_pct = $2 WHERE month_end = $1`, monthEnd, pct)
	return err
}

// asDay turns a DATE read from PostgreSQL, which comes back at midnight UTC, into midnight WIB.
func asDay(d time.Time) time.Time {
	y, m, dd := d.Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, wib)
}

func nullDate(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

func nullFloat(f float64) *float64 {
	if f == 0 {
		return nil
	}
	return &f
}
