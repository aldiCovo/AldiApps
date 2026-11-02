package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxBodyBytes = 1 << 16

type cartResponse struct {
	Items []Item `json:"items"`
	// How many of the items sent didn't fit. Only a merge can skip items.
	Skipped int `json:"skipped"`
}

func validateItem(item Item) error {
	switch {
	case item.ProductID <= 0:
		return errors.New("product_id must be a positive integer")
	case item.VariantID <= 0:
		return errors.New("variant_id must be a positive integer")
	case item.Quantity < 1 || item.Quantity > maxQuantity:
		return fmt.Errorf("quantity must be between 1 and %d", maxQuantity)
	}
	return nil
}

func validateMerge(added []Item) error {
	if len(added) == 0 {
		return errors.New("items is required")
	}
	if len(added) > maxItems {
		return fmt.Errorf("at most %d items can be added at once", maxItems)
	}
	seen := make(map[int64]bool, len(added))
	for _, item := range added {
		if err := validateItem(item); err != nil {
			return err
		}
		if seen[item.VariantID] {
			return errors.New("each variant can only appear once")
		}
		seen[item.VariantID] = true
	}
	return nil
}

func newHandler(store *Store, verifier *Verifier) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.pool.Ping(ctx); err != nil {
			slog.Error("database not ready", "error", err)
			writeError(w, http.StatusServiceUnavailable, "database not ready")
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	authenticated := func(next func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !found {
				writeUnauthorized(w, "a bearer token is required")
				return
			}
			userID, err := verifier.UserID(r.Context(), token)
			if errors.Is(err, errKeysUnavailable) {
				slog.Error("checking a token failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "login can't be checked now")
				return
			}
			if err != nil {
				writeUnauthorized(w, "the token is not valid")
				return
			}
			next(w, r, userID)
		}
	}

	mux.HandleFunc("GET /cart", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		found, err := store.Items(r.Context(), userID)
		writeCart(w, found, 0, err)
	}))

	mux.HandleFunc("POST /cart/merge", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		var in struct {
			Items []Item `json:"items"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if err := validateMerge(in.Items); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		found, skipped, err := store.Merge(r.Context(), userID, in.Items)
		writeCart(w, found, skipped, err)
	}))

	mux.HandleFunc("PUT /cart/items/{variant_id}", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		variantID, ok := variantPath(w, r)
		if !ok {
			return
		}
		var in struct {
			ProductID int64 `json:"product_id"`
			Quantity  int32 `json:"quantity"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		item := Item{ProductID: in.ProductID, VariantID: variantID, Quantity: in.Quantity}
		if err := validateItem(item); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		found, err := store.Set(r.Context(), userID, item)
		writeCart(w, found, 0, err)
	}))

	mux.HandleFunc("DELETE /cart/items/{variant_id}", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		variantID, ok := variantPath(w, r)
		if !ok {
			return
		}
		found, err := store.Remove(r.Context(), userID, variantID)
		writeCart(w, found, 0, err)
	}))

	mux.HandleFunc("DELETE /cart", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		writeCart(w, []Item{}, 0, store.Clear(r.Context(), userID))
	}))

	return mux
}

func variantPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("variant_id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "variant_id must be a positive integer")
		return 0, false
	}
	return id, true
}

func writeCart(w http.ResponseWriter, found []Item, skipped int, err error) {
	switch {
	case errors.Is(err, errCartFull):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.Error("database request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	default:
		writeJSON(w, http.StatusOK, cartResponse{Items: found, Skipped: skipped})
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, message)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}
