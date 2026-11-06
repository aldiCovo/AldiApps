package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// saleCheckInterval is how late, at most, the search index learns that a sale started
	// or ended. Product pages and orders read the price from the database, so they never lag.
	saleCheckInterval = 30 * time.Second
	saleCheckBatch    = 100
)

// SetSale sells the product for price from startsAt until endsAt, replacing any sale it had.
func (s *Store) SetSale(ctx context.Context, id, price int64, startsAt, endsAt time.Time) (p Product, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockProduct(ctx, tx, id); err != nil {
			return err
		}
		var regular int64
		if err := tx.QueryRow(ctx, `SELECT price FROM products WHERE id = $1`, id).Scan(&regular); err != nil {
			return err
		}
		if price >= regular {
			return errSaleNotBelow
		}
		// The event below already carries the new price, so the announcer has nothing to send.
		_, err := tx.Exec(ctx,
			`UPDATE products SET sale_price = $2, sale_starts_at = $3, sale_ends_at = $4,
			     sale_announced = ($3 <= now() AND now() < $4), updated_at = now()
			 WHERE id = $1`,
			id, price, startsAt, endsAt)
		if err != nil {
			return err
		}
		if p, err = getProduct(ctx, tx, id); err != nil {
			return err
		}
		return addEvent(ctx, tx, eventProductUpdated, p)
	})
	return p, err
}

// ClearSale removes the product's sale, running or not. A product without one is left as it is.
func (s *Store) ClearSale(ctx context.Context, id int64) (p Product, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockProduct(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE products SET sale_price = NULL, sale_starts_at = NULL, sale_ends_at = NULL,
			     sale_announced = false, updated_at = now()
			 WHERE id = $1 AND sale_price IS NOT NULL`, id)
		if err != nil {
			return err
		}
		if p, err = getProduct(ctx, tx, id); err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return addEvent(ctx, tx, eventProductUpdated, p)
	})
	return p, err
}

// AnnounceSales sends product.updated for each product whose sale started or ended since its
// last event, so the search index shows the price customers pay now. It returns how many.
func (s *Store) AnnounceSales(ctx context.Context) (announced int, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// SKIP LOCKED leaves products being changed right now to the next round.
		rows, err := tx.Query(ctx,
			`UPDATE products SET sale_announced = NOT sale_announced
			 WHERE id IN (SELECT id FROM products WHERE sale_announced <> `+saleOn+`
			              ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED)
			 RETURNING id`, saleCheckBatch)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := addProductUpdated(ctx, tx, id); err != nil {
				return err
			}
		}
		announced = len(ids)
		return nil
	})
	return announced, err
}

// announceSales runs AnnounceSales every saleCheckInterval until ctx is cancelled.
func announceSales(ctx context.Context, store *Store) {
	ticker := time.NewTicker(saleCheckInterval)
	defer ticker.Stop()
	for {
		for {
			n, err := store.AnnounceSales(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("announcing sales failed", "error", err)
				}
				break
			}
			if n > 0 {
				slog.Info("announced sale changes", "products", n)
			}
			if n < saleCheckBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
