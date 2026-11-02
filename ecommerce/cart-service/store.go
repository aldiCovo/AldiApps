package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The same limits as an order, so everything in a cart can be ordered at once.
const (
	maxItems    = 20
	maxQuantity = 20
)

var errCartFull = errors.New("the cart is full")

type Item struct {
	ProductID int64 `json:"product_id"`
	VariantID int64 `json:"variant_id"`
	Quantity  int32 `json:"quantity"`
}

type Store struct {
	pool *pgxpool.Pool
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func items(ctx context.Context, q querier, userID int64) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT product_id, variant_id, quantity FROM cart_items
		WHERE user_id = $1 ORDER BY created_at, variant_id`, userID)
	if err != nil {
		return nil, err
	}
	found := []Item{}
	var item Item
	_, err = pgx.ForEachRow(rows, []any{&item.ProductID, &item.VariantID, &item.Quantity}, func() error {
		found = append(found, item)
		return nil
	})
	return found, err
}

func (s *Store) Items(ctx context.Context, userID int64) ([]Item, error) {
	return items(ctx, s.pool, userID)
}

// locked runs fn while no other request can change this user's cart, so two browsers can't
// together pass the limit on items.
func (s *Store) locked(ctx context.Context, userID int64, fn func(pgx.Tx) error) ([]Item, error) {
	var result []Item
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", userID); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		var err error
		result, err = items(ctx, tx, userID)
		return err
	})
	return result, err
}

func hasRoom(ctx context.Context, tx pgx.Tx, userID, variantID int64) (bool, error) {
	var room bool
	err := tx.QueryRow(ctx, `SELECT count(*) < $3 OR bool_or(variant_id = $2) FROM cart_items WHERE user_id = $1`,
		userID, variantID, maxItems).Scan(&room)
	return room, err
}

// Merge adds items to the cart, summing a variant that's already there up to the limit.
// Items beyond the limit on different variants are skipped and counted, so a guest's cart
// can always join an account's.
func (s *Store) Merge(ctx context.Context, userID int64, added []Item) ([]Item, int, error) {
	skipped := 0
	result, err := s.locked(ctx, userID, func(tx pgx.Tx) error {
		for _, item := range added {
			room, err := hasRoom(ctx, tx, userID, item.VariantID)
			if err != nil {
				return err
			}
			if !room {
				skipped++
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO cart_items (user_id, variant_id, product_id, quantity)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (user_id, variant_id) DO UPDATE
				SET quantity = LEAST(cart_items.quantity + EXCLUDED.quantity, $5), updated_at = now()`,
				userID, item.VariantID, item.ProductID, item.Quantity, maxQuantity); err != nil {
				return err
			}
		}
		return nil
	})
	return result, skipped, err
}

// Set replaces one variant's quantity, and adds it if another browser has removed it.
func (s *Store) Set(ctx context.Context, userID int64, item Item) ([]Item, error) {
	return s.locked(ctx, userID, func(tx pgx.Tx) error {
		room, err := hasRoom(ctx, tx, userID, item.VariantID)
		if err != nil {
			return err
		}
		if !room {
			return errCartFull
		}
		_, err = tx.Exec(ctx, `INSERT INTO cart_items (user_id, variant_id, product_id, quantity)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, variant_id) DO UPDATE SET quantity = EXCLUDED.quantity, updated_at = now()`,
			userID, item.VariantID, item.ProductID, item.Quantity)
		return err
	})
}

func (s *Store) Remove(ctx context.Context, userID, variantID int64) ([]Item, error) {
	return s.locked(ctx, userID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "DELETE FROM cart_items WHERE user_id = $1 AND variant_id = $2", userID, variantID)
		return err
	})
}

func (s *Store) Clear(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM cart_items WHERE user_id = $1", userID)
	return err
}
