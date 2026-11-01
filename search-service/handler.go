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
	"unicode/utf8"
)

const (
	maxTextLength = 200
	defaultLimit  = 20
	maxLimit      = 100
	// Elasticsearch refuses to page past 10000 results by default.
	maxOffset = 10000 - maxLimit
)

func newHandler(elastic *Elastic) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Only Elasticsearch counts: searching still works while RabbitMQ or the catalog is down.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := elastic.Ping(ctx); err != nil {
			slog.Error("elasticsearch not ready", "error", err)
			writeError(w, http.StatusServiceUnavailable, "elasticsearch not ready")
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		query, err := parseSearchQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		total, products, err := elastic.Search(r.Context(), query)
		if err != nil {
			if !errors.Is(err, errIndexMissing) {
				slog.Error("search failed", "error", err)
			}
			writeError(w, http.StatusServiceUnavailable, "search is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"total": total, "products": products})
	})

	return mux
}

func parseSearchQuery(r *http.Request) (SearchQuery, error) {
	values := r.URL.Query()
	q := SearchQuery{
		Text:  strings.TrimSpace(values.Get("q")),
		Size:  strings.TrimSpace(values.Get("size")),
		Color: strings.TrimSpace(values.Get("color")),
	}
	for name, value := range map[string]string{"q": q.Text, "size": q.Size, "color": q.Color} {
		if utf8.RuneCountInString(value) > maxTextLength {
			return q, fmt.Errorf("%s must be at most %d characters", name, maxTextLength)
		}
	}
	if raw := values.Get("category_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return q, errors.New("category_id must be a positive integer")
		}
		q.CategoryID = &id
	}
	if raw := values.Get("in_stock"); raw != "" {
		inStock, err := strconv.ParseBool(raw)
		if err != nil {
			return q, errors.New("in_stock must be true or false")
		}
		q.InStock = inStock
	}
	var err error
	if q.Limit, err = queryInt(r, "limit", defaultLimit, 1, maxLimit); err != nil {
		return q, err
	}
	if q.Offset, err = queryInt(r, "offset", 0, 0, maxOffset); err != nil {
		return q, err
	}
	return q, nil
}

func queryInt(r *http.Request, name string, fallback, low, high int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < low || n > high {
		return 0, fmt.Errorf("%s must be between %d and %d", name, low, high)
	}
	return n, nil
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
