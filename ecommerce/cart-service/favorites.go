package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// maxFavorites keeps the list one page of the shop can show.
const maxFavorites = 100

var errFavoritesFull = fmt.Errorf("at most %d products can be saved", maxFavorites)

type favoritesResponse struct {
	// Newest first.
	ProductIDs []int64 `json:"product_ids"`
	// How many of the products sent didn't fit. Only a merge can skip products.
	Skipped int `json:"skipped"`
}

func favorites(ctx context.Context, q querier, userID int64) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT product_id FROM favorites WHERE user_id = $1
		ORDER BY created_at DESC, product_id DESC`, userID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if ids == nil {
		ids = []int64{}
	}
	return ids, err
}

func (s *Store) Favorites(ctx context.Context, userID int64) ([]int64, error) {
	return favorites(ctx, s.pool, userID)
}

// lockedFavorites runs fn while no other request changes this user's favorites, so two
// browsers can't together pass the limit. The key differs from the cart's lock.
func (s *Store) lockedFavorites(ctx context.Context, userID int64, fn func(pgx.Tx) error) ([]int64, error) {
	var result []int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(1, hashint8($1))", userID); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		var err error
		result, err = favorites(ctx, tx, userID)
		return err
	})
	return result, err
}

// addFavorite saves a product, keeping the date it was first saved. It answers false when
// the list is full and the product isn't on it.
func addFavorite(ctx context.Context, tx pgx.Tx, userID, productID int64) (bool, error) {
	var room bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) < $3 OR coalesce(bool_or(product_id = $2), false) FROM favorites WHERE user_id = $1`,
		userID, productID, maxFavorites).Scan(&room); err != nil || !room {
		return false, err
	}
	// clock_timestamp, not now(), so products merged in one transaction keep their order.
	_, err := tx.Exec(ctx, `INSERT INTO favorites (user_id, product_id, created_at) VALUES ($1, $2, clock_timestamp())
		ON CONFLICT DO NOTHING`, userID, productID)
	return err == nil, err
}

func (s *Store) AddFavorite(ctx context.Context, userID, productID int64) ([]int64, error) {
	return s.lockedFavorites(ctx, userID, func(tx pgx.Tx) error {
		added, err := addFavorite(ctx, tx, userID, productID)
		if err == nil && !added {
			return errFavoritesFull
		}
		return err
	})
}

// MergeFavorites saves a guest's favorites to the account. Products past the limit are
// skipped and counted, so a guest's list can always join.
func (s *Store) MergeFavorites(ctx context.Context, userID int64, productIDs []int64) ([]int64, int, error) {
	skipped := 0
	result, err := s.lockedFavorites(ctx, userID, func(tx pgx.Tx) error {
		// Oldest first, so the guest's newest stays newest.
		for i := len(productIDs) - 1; i >= 0; i-- {
			added, err := addFavorite(ctx, tx, userID, productIDs[i])
			if err != nil {
				return err
			}
			if !added {
				skipped++
			}
		}
		return nil
	})
	return result, skipped, err
}

func (s *Store) RemoveFavorite(ctx context.Context, userID, productID int64) ([]int64, error) {
	return s.lockedFavorites(ctx, userID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "DELETE FROM favorites WHERE user_id = $1 AND product_id = $2", userID, productID)
		return err
	})
}

func validateFavoritesMerge(ids []int64) error {
	if len(ids) == 0 {
		return errors.New("product_ids is required")
	}
	if len(ids) > maxFavorites {
		return fmt.Errorf("at most %d products can be added at once", maxFavorites)
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return errors.New("each product id must be a positive integer")
		}
		if seen[id] {
			return errors.New("each product can only appear once")
		}
		seen[id] = true
	}
	return nil
}

func addFavoriteRoutes(mux *http.ServeMux, store *Store, authenticated func(func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc) {
	mux.HandleFunc("GET /favorites", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		ids, err := store.Favorites(r.Context(), userID)
		writeFavorites(w, ids, 0, err)
	}))

	mux.HandleFunc("POST /favorites/merge", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		var in struct {
			ProductIDs []int64 `json:"product_ids"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if err := validateFavoritesMerge(in.ProductIDs); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ids, skipped, err := store.MergeFavorites(r.Context(), userID, in.ProductIDs)
		writeFavorites(w, ids, skipped, err)
	}))

	mux.HandleFunc("PUT /favorites/{product_id}", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		if id, ok := productPath(w, r); ok {
			ids, err := store.AddFavorite(r.Context(), userID, id)
			writeFavorites(w, ids, 0, err)
		}
	}))

	mux.HandleFunc("DELETE /favorites/{product_id}", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		if id, ok := productPath(w, r); ok {
			ids, err := store.RemoveFavorite(r.Context(), userID, id)
			writeFavorites(w, ids, 0, err)
		}
	}))
}

func productPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("product_id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "product_id must be a positive integer")
		return 0, false
	}
	return id, true
}

func writeFavorites(w http.ResponseWriter, ids []int64, skipped int, err error) {
	if errors.Is(err, errFavoritesFull) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeCart(w, nil, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, favoritesResponse{ProductIDs: ids, Skipped: skipped})
}
