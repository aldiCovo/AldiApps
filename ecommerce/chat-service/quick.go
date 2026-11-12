package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	maxQuickReplyLength = 500
	// Enough for every common answer, few enough to fit above the reply box.
	maxQuickReplies = 30
)

var (
	errQuickNotFound = errors.New("quick reply not found")
	errTooManyQuick  = fmt.Errorf("there can be at most %d quick replies", maxQuickReplies)
)

// QuickReply is an answer the admins send often.
type QuickReply struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

const quickColumns = "id, body, updated_at"

func scanQuick(row pgx.Row) (QuickReply, error) {
	var q QuickReply
	err := row.Scan(&q.ID, &q.Body, &q.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, errQuickNotFound
	}
	return q, err
}

// QuickReplies lists them oldest first, so new ones go to the end.
func (s *Store) QuickReplies(ctx context.Context) ([]QuickReply, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+quickColumns+" FROM quick_replies ORDER BY id")
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (QuickReply, error) { return scanQuick(row) })
	if list == nil {
		list = []QuickReply{}
	}
	return list, err
}

func (s *Store) AddQuickReply(ctx context.Context, body string) (QuickReply, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return QuickReply{}, err
	}
	defer tx.Rollback(ctx)
	// Two admins adding at once still can't pass the limit.
	if _, err := tx.Exec(ctx, "LOCK TABLE quick_replies IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return QuickReply{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM quick_replies").Scan(&count); err != nil {
		return QuickReply{}, err
	}
	if count >= maxQuickReplies {
		return QuickReply{}, errTooManyQuick
	}
	q, err := scanQuick(tx.QueryRow(ctx, "INSERT INTO quick_replies (body) VALUES ($1) RETURNING "+quickColumns, body))
	if err != nil {
		return QuickReply{}, err
	}
	return q, tx.Commit(ctx)
}

func (s *Store) UpdateQuickReply(ctx context.Context, id int64, body string) (QuickReply, error) {
	return scanQuick(s.pool.QueryRow(ctx,
		"UPDATE quick_replies SET body = $2, updated_at = now() WHERE id = $1 RETURNING "+quickColumns, id, body))
}

func (s *Store) DeleteQuickReply(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM quick_replies WHERE id = $1", id)
	if err == nil && tag.RowsAffected() == 0 {
		return errQuickNotFound
	}
	return err
}

func cleanQuickReply(body string) (string, error) {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	switch {
	case body == "":
		return "", errors.New("body is required")
	case !utf8.ValidString(body) || strings.ContainsRune(body, 0):
		return "", errors.New("body must be text")
	case utf8.RuneCountInString(body) > maxQuickReplyLength:
		return "", fmt.Errorf("body must be at most %d characters", maxQuickReplyLength)
	}
	return body, nil
}

// addQuickRoutes serves the quick replies to admin-service.
func addQuickRoutes(mux *http.ServeMux, store *Store, internal func(http.HandlerFunc) http.HandlerFunc) {
	quickID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errQuickNotFound.Error())
			return 0, false
		}
		return id, true
	}
	readQuick := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		var in struct {
			Body string `json:"body"`
		}
		if !readJSON(w, r, &in) {
			return "", false
		}
		body, err := cleanQuickReply(in.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return "", false
		}
		return body, true
	}
	answer := func(w http.ResponseWriter, status int, q QuickReply, err error) {
		switch {
		case errors.Is(err, errQuickNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, errTooManyQuick):
			writeError(w, http.StatusConflict, err.Error())
		case err != nil:
			writeFailure(w, err)
		default:
			writeJSON(w, status, q)
		}
	}

	mux.HandleFunc("GET /internal/quick-replies", internal(func(w http.ResponseWriter, r *http.Request) {
		list, err := store.QuickReplies(r.Context())
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"quick_replies": list})
	}))
	mux.HandleFunc("POST /internal/quick-replies", internal(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readQuick(w, r); ok {
			q, err := store.AddQuickReply(r.Context(), body)
			answer(w, http.StatusCreated, q, err)
		}
	}))
	mux.HandleFunc("PUT /internal/quick-replies/{id}", internal(func(w http.ResponseWriter, r *http.Request) {
		id, ok := quickID(w, r)
		if !ok {
			return
		}
		if body, ok := readQuick(w, r); ok {
			q, err := store.UpdateQuickReply(r.Context(), id, body)
			answer(w, http.StatusOK, q, err)
		}
	}))
	mux.HandleFunc("DELETE /internal/quick-replies/{id}", internal(func(w http.ResponseWriter, r *http.Request) {
		id, ok := quickID(w, r)
		if !ok {
			return
		}
		err := store.DeleteQuickReply(r.Context(), id)
		if errors.Is(err, errQuickNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
