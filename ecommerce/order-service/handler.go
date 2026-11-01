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

const (
	maxBodyBytes  = 1 << 16
	maxItems      = 20
	maxQuantity   = 20
	defaultLimit  = 20
	maxLimit      = 100
	outcomeOK     = "success"
	outcomeFailed = "failure"
)

type orderInput struct {
	Items []struct {
		VariantID int64 `json:"variant_id"`
		Quantity  int32 `json:"quantity"`
	} `json:"items"`
}

func (in *orderInput) validate() error {
	if len(in.Items) == 0 {
		return errors.New("items is required")
	}
	if len(in.Items) > maxItems {
		return fmt.Errorf("an order can have at most %d items", maxItems)
	}
	seen := make(map[int64]bool, len(in.Items))
	for _, item := range in.Items {
		switch {
		case item.VariantID <= 0:
			return errors.New("variant_id must be a positive integer")
		case item.Quantity < 1 || item.Quantity > maxQuantity:
			return fmt.Errorf("quantity must be between 1 and %d", maxQuantity)
		case seen[item.VariantID]:
			return errors.New("each variant can only appear once")
		}
		seen[item.VariantID] = true
	}
	return nil
}

func newHandler(store *Store, verifier *Verifier, catalog *Catalog) http.Handler {
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

	mux.HandleFunc("POST /orders", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		var in orderInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		items := make([]OrderItem, len(in.Items))
		for i, item := range in.Items {
			var err error
			if items[i], err = catalog.Item(r.Context(), item.VariantID, item.Quantity); err != nil {
				if errors.Is(err, errUnknownVariant) {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				slog.Error("reading the catalog failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "the catalog is not available")
				return
			}
		}
		order, err := store.Create(r.Context(), userID, items)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, order)
	}))

	mux.HandleFunc("GET /orders", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		limit, offset := defaultLimit, 0
		var err error
		if value := r.URL.Query().Get("limit"); value != "" {
			if limit, err = strconv.Atoi(value); err != nil || limit < 1 || limit > maxLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxLimit))
				return
			}
		}
		if value := r.URL.Query().Get("offset"); value != "" {
			if offset, err = strconv.Atoi(value); err != nil || offset < 0 {
				writeError(w, http.StatusBadRequest, "offset must not be negative")
				return
			}
		}
		orders, err := store.List(r.Context(), userID, limit, offset)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"orders": orders})
	}))

	orderID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errNotFound.Error())
			return 0, false
		}
		return id, true
	}

	mux.HandleFunc("GET /orders/{id}", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		id, ok := orderID(w, r)
		if !ok {
			return
		}
		order, err := store.Get(r.Context(), userID, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))

	mux.HandleFunc("POST /orders/{id}/pay", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		id, ok := orderID(w, r)
		if !ok {
			return
		}
		var in struct {
			Simulate string `json:"simulate"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.Simulate != outcomeOK && in.Simulate != outcomeFailed {
			writeError(w, http.StatusBadRequest, "simulate must be success or failure")
			return
		}
		order, err := store.Pay(r.Context(), userID, id, in.Simulate)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, order)
	}))

	return mux
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

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errCannotPay), errors.Is(err, errTooManyOrders):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("database request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
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
