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
	maxBodyBytes     = 1 << 20
	maxNameLength    = 200
	maxVariantLength = 50
	defaultLimit     = 20
	maxLimit         = 100
)

// cleanText trims a required text field and rejects what Postgres or the shop can't use.
func cleanText(field, value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return "", fmt.Errorf("%s is required", field)
	case utf8.RuneCountInString(value) > maxLength:
		return "", fmt.Errorf("%s must be at most %d characters", field, maxLength)
	// Postgres text can't hold a NUL byte.
	case strings.ContainsRune(value, 0):
		return "", fmt.Errorf("%s must not contain NUL characters", field)
	}
	return value, nil
}

type categoryInput struct {
	Name string `json:"name"`
}

func (in *categoryInput) validate() (err error) {
	in.Name, err = cleanText("name", in.Name, maxNameLength)
	return err
}

type productInput struct {
	CategoryID  *int64 `json:"category_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       *int64 `json:"price"`
}

func (in *productInput) validate() (err error) {
	if in.Name, err = cleanText("name", in.Name, maxNameLength); err != nil {
		return err
	}
	switch {
	case strings.ContainsRune(in.Description, 0):
		return errors.New("description must not contain NUL characters")
	case in.CategoryID != nil && *in.CategoryID <= 0:
		return errors.New("category_id must be a positive integer")
	case in.Price == nil:
		return errors.New("price is required")
	case *in.Price < 0:
		return errors.New("price must not be negative")
	}
	return nil
}

type variantInput struct {
	Size  string `json:"size"`
	Color string `json:"color"`
	Stock *int32 `json:"stock"`
}

func (in *variantInput) validate() (err error) {
	if in.Size, err = cleanText("size", in.Size, maxVariantLength); err != nil {
		return err
	}
	in.Color, err = cleanText("color", in.Color, maxVariantLength)
	return err
}

func newHandler(store *Store) http.Handler {
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

	mux.HandleFunc("GET /categories", func(w http.ResponseWriter, r *http.Request) {
		categories, err := store.ListCategories(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"categories": categories})
	})

	mux.HandleFunc("POST /categories", func(w http.ResponseWriter, r *http.Request) {
		var in categoryInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		c, err := store.CreateCategory(r.Context(), in.Name)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/categories/%d", c.ID))
		writeJSON(w, http.StatusCreated, c)
	})

	mux.HandleFunc("GET /categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		c, err := store.GetCategory(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})

	mux.HandleFunc("PUT /categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in categoryInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		c, err := store.UpdateCategory(r.Context(), id, in.Name)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})

	mux.HandleFunc("DELETE /categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := store.DeleteCategory(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /products", func(w http.ResponseWriter, r *http.Request) {
		limit, err := queryInt(r, "limit", defaultLimit, 1, maxLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		offset, err := queryInt(r, "offset", 0, 0, 1<<31-1)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var categoryID *int64
		if raw := r.URL.Query().Get("category_id"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				writeError(w, http.StatusBadRequest, "category_id must be a positive integer")
				return
			}
			categoryID = &id
		}
		products, err := store.List(r.Context(), categoryID, limit, offset)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"products": products})
	})

	mux.HandleFunc("POST /products", func(w http.ResponseWriter, r *http.Request) {
		var in productInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		p, err := store.Create(r.Context(), in.CategoryID, in.Name, in.Description, *in.Price)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/products/%d", p.ID))
		writeJSON(w, http.StatusCreated, p)
	})

	mux.HandleFunc("GET /products/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		p, err := store.Get(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("PUT /products/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in productInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		p, err := store.Update(r.Context(), id, in.CategoryID, in.Name, in.Description, *in.Price)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("DELETE /products/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := store.Delete(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /products/{id}/variants", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in variantInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var stock int32
		if in.Stock != nil {
			stock = *in.Stock
		}
		if stock < 0 || stock > maxStock {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("stock must be between 0 and %d", maxStock))
			return
		}
		v, err := store.CreateVariant(r.Context(), id, in.Size, in.Color, stock)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/variants/%d", v.ID))
		writeJSON(w, http.StatusCreated, v)
	})

	mux.HandleFunc("GET /variants/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		v, err := store.GetVariant(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	mux.HandleFunc("PUT /variants/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in variantInput
		if !readJSON(w, r, &in) {
			return
		}
		if in.Stock != nil {
			writeError(w, http.StatusBadRequest, "change stock through PATCH /variants/{id}/stock")
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		v, err := store.UpdateVariant(r.Context(), id, in.Size, in.Color)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	mux.HandleFunc("DELETE /variants/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := store.DeleteVariant(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("PATCH /variants/{id}/stock", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			Delta int32 `json:"delta"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.Delta == 0 || in.Delta < -maxStock || in.Delta > maxStock {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("delta must be non-zero and between -%d and %d", maxStock, maxStock))
			return
		}
		v, err := store.AdjustStock(r.Context(), id, in.Delta)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	return mux
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
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
	case errors.Is(err, errNotFound), errors.Is(err, errCategoryNotFound), errors.Is(err, errVariantNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errUnknownCategory):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errStockOutOfRange), errors.Is(err, errCategoryExists),
		errors.Is(err, errCategoryInUse), errors.Is(err, errVariantExists):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("database request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
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
