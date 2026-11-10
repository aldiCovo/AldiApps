package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// tradeRow is a paper trade as the pages show it.
type tradeRow struct {
	ID          int64
	Rule        string
	Ticker      string
	SignalDate  time.Time
	SignalClose float64
	MaxEntry    float64
	Stop        float64
	Target      float64
	Exit        string
	MaxDays     int
	Reasons     []string
	MarketOK    bool
	Status      string
	EntryDate   *time.Time
	EntryPrice  *float64
	ExitDate    *time.Time
	ExitPrice   *float64
	Days        int
	ReturnPct   *float64
}

// Random reports whether the trade is a random entry made to compare a rule with.
func (t tradeRow) Random() bool { return strings.HasPrefix(t.Rule, strategyBasePrefx) }

// Which trades a listing shows.
const (
	tradesUnfinished = "unfinished" // waiting for the next open, or held
	tradesClosed     = "closed"     // sold or skipped, newest first
)

// Trades lists paper trades: unfinished or closed, of one stock when ticker isn't empty, and
// with the random entries only when withRandom.
func (s *store) Trades(ctx context.Context, which, ticker string, withRandom bool, limit int) ([]tradeRow, error) {
	var where []string
	var args []any
	switch which {
	case tradesUnfinished:
		where = append(where, "status IN ('waiting', 'open')")
	case tradesClosed:
		where = append(where, "status NOT IN ('waiting', 'open')")
	}
	if ticker != "" {
		args = append(args, ticker)
		where = append(where, "ticker = $1")
	}
	if !withRandom {
		where = append(where, "rule NOT LIKE 'acak/%'")
	}
	order := "signal_date DESC, rule, ticker"
	if which == tradesClosed {
		order = "coalesce(exit_date, signal_date) DESC, rule, ticker"
	}
	args = append(args, limit)
	query := `SELECT id, rule, ticker, signal_date, signal_close, max_entry, stop, target, exit_rule,
		max_days, reasons, market_ok, status, entry_date, entry_price, exit_date, exit_price, days, return_pct
		FROM paper_trades`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + order + " LIMIT $" + strconv.Itoa(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (tradeRow, error) {
		var t tradeRow
		err := row.Scan(&t.ID, &t.Rule, &t.Ticker, &t.SignalDate, &t.SignalClose, &t.MaxEntry, &t.Stop,
			&t.Target, &t.Exit, &t.MaxDays, &t.Reasons, &t.MarketOK, &t.Status, &t.EntryDate, &t.EntryPrice,
			&t.ExitDate, &t.ExitPrice, &t.Days, &t.ReturnPct)
		t.SignalDate = asDay(t.SignalDate)
		if t.EntryDate != nil {
			d := asDay(*t.EntryDate)
			t.EntryDate = &d
		}
		if t.ExitDate != nil {
			d := asDay(*t.ExitDate)
			t.ExitDate = &d
		}
		return t, err
	})
}
