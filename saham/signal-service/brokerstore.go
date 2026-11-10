package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
)

var errNotFound = errors.New("not found")

// SaveBrokerSummary stores a pasted summary, replacing one for the same stock, period and
// filter, and returns its id.
func (s *store) SaveBrokerSummary(ctx context.Context, b brokerSummary) (int64, error) {
	top, err := json.Marshal(b.Top)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO broker_summaries
			(ticker, period_start, period_end, investor, market, mode, buyers, sellers, net_lots, net_value,
			 avg_price, acc_dist, top, pasted)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (ticker, period_start, period_end, investor, market, mode) DO UPDATE SET
			buyers = EXCLUDED.buyers, sellers = EXCLUDED.sellers, net_lots = EXCLUDED.net_lots,
			net_value = EXCLUDED.net_value, avg_price = EXCLUDED.avg_price, acc_dist = EXCLUDED.acc_dist,
			top = EXCLUDED.top, pasted = EXCLUDED.pasted, created_at = now()
			RETURNING id`,
			b.Ticker, b.Start, b.End, b.Investor, b.Market, b.Mode, b.Buyers, b.Sellers, b.NetLots,
			b.NetValue, b.AvgPrice, b.AccDist, top, b.Pasted).Scan(&id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM broker_summary_rows WHERE summary_id = $1`, id); err != nil {
			return err
		}
		var rows [][]any
		for side, lines := range map[string][]brokerLine{"buy": b.Buy, "sell": b.Sell} {
			for rank, l := range lines {
				rows = append(rows, []any{id, side, rank + 1, l.Broker, l.Value, l.Lots, l.Avg})
			}
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"broker_summary_rows"},
			[]string{"summary_id", "side", "rank", "broker", "value", "lots", "avg_price"}, pgx.CopyFromRows(rows))
		return err
	})
	return id, err
}

const brokerSummaryColumns = `id, ticker, period_start, period_end, investor, market, mode, buyers, sellers,
	net_lots, net_value, avg_price, coalesce(acc_dist, ''), top, pasted, created_at`

func scanBrokerSummary(row pgx.Row) (brokerSummary, error) {
	var b brokerSummary
	var top []byte
	err := row.Scan(&b.ID, &b.Ticker, &b.Start, &b.End, &b.Investor, &b.Market, &b.Mode, &b.Buyers,
		&b.Sellers, &b.NetLots, &b.NetValue, &b.AvgPrice, &b.AccDist, &top, &b.Pasted, &b.CreatedAt)
	if err != nil {
		return b, err
	}
	b.Start, b.End = asDay(b.Start), asDay(b.End)
	return b, json.Unmarshal(top, &b.Top)
}

// BrokerSummary returns one summary with its broker lines.
func (s *store) BrokerSummary(ctx context.Context, id int64) (brokerSummary, error) {
	b, err := scanBrokerSummary(s.pool.QueryRow(ctx, `SELECT `+brokerSummaryColumns+` FROM broker_summaries WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, errNotFound
	}
	if err != nil {
		return b, err
	}
	list := []brokerSummary{b}
	if err := s.addBrokerLines(ctx, list); err != nil {
		return b, err
	}
	return list[0], nil
}

// FindBrokerSummary returns the summary already kept for the same stock, period and filter,
// without its lines, or errNotFound.
func (s *store) FindBrokerSummary(ctx context.Context, b brokerSummary) (brokerSummary, error) {
	found, err := scanBrokerSummary(s.pool.QueryRow(ctx, `SELECT `+brokerSummaryColumns+` FROM broker_summaries
		WHERE ticker = $1 AND period_start = $2 AND period_end = $3 AND investor = $4 AND market = $5 AND mode = $6`,
		b.Ticker, b.Start, b.End, b.Investor, b.Market, b.Mode))
	if errors.Is(err, pgx.ErrNoRows) {
		return found, errNotFound
	}
	return found, err
}

// BrokerSummaries lists summaries with their lines, newest period first, of one stock when
// ticker isn't empty.
func (s *store) BrokerSummaries(ctx context.Context, ticker string, limit int) ([]brokerSummary, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+brokerSummaryColumns+` FROM broker_summaries
		WHERE $1 = '' OR ticker = $1 ORDER BY period_end DESC, period_start DESC, ticker, id DESC LIMIT $2`, ticker, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (brokerSummary, error) {
		return scanBrokerSummary(row)
	})
	if err != nil {
		return nil, err
	}
	return list, s.addBrokerLines(ctx, list)
}

func (s *store) addBrokerLines(ctx context.Context, list []brokerSummary) error {
	if len(list) == 0 {
		return nil
	}
	index := map[int64]int{}
	ids := make([]int64, len(list))
	for i, b := range list {
		index[b.ID], ids[i] = i, b.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT summary_id, side, broker, value, lots, avg_price
		FROM broker_summary_rows WHERE summary_id = ANY($1) ORDER BY summary_id, side, rank`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var side string
		var l brokerLine
		if err := rows.Scan(&id, &side, &l.Broker, &l.Value, &l.Lots, &l.Avg); err != nil {
			return err
		}
		b := &list[index[id]]
		if side == "buy" {
			b.Buy = append(b.Buy, l)
		} else {
			b.Sell = append(b.Sell, l)
		}
	}
	return rows.Err()
}

func (s *store) DeleteBrokerSummary(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM broker_summaries WHERE id = $1`, id)
	return err
}

// ForeignBrokers is the set of broker codes counted as foreign.
func (s *store) ForeignBrokers(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT code FROM foreign_brokers`)
	if err != nil {
		return nil, err
	}
	codes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, c := range codes {
		set[c] = true
	}
	return set, nil
}

// SetForeignBrokers replaces the list of foreign brokers.
func (s *store) SetForeignBrokers(ctx context.Context, codes []string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM foreign_brokers`); err != nil {
			return err
		}
		for _, c := range codes {
			if _, err := tx.Exec(ctx, `INSERT INTO foreign_brokers (code) VALUES ($1) ON CONFLICT DO NOTHING`, c); err != nil {
				return err
			}
		}
		return nil
	})
}

func sortedCodes(set map[string]bool) []string {
	var out []string
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
