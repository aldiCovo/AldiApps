package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	roleAdmin      = "admin"
	roleCustomer   = "customer"
	defaultPage    = 50
	maxPage        = 100
	maxSearchChars = 200
)

var (
	errLastAdmin = errors.New("the last admin can't stop being an admin")
	errBadRole   = errors.New("role must be admin or customer")
)

// UserPage is one page of accounts and how many match in all.
type UserPage struct {
	Users []User `json:"users"`
	Total int    `json:"total"`
}

// likePattern finds text anywhere, with % and _ taken as themselves.
func likePattern(text string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
}

// List returns accounts, newest first, whose email or name contains search, and with role
// if one is given.
func (s *Store) List(ctx context.Context, search, role string, limit, offset int) (UserPage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+userColumns+`, count(*) OVER ()
		FROM users
		WHERE ($1 = '' OR email ILIKE $2 OR name ILIKE $2) AND ($3 = '' OR role = $3)
		ORDER BY id DESC
		LIMIT $4 OFFSET $5`, search, likePattern(search), role, limit, offset)
	if err != nil {
		return UserPage{}, err
	}
	defer rows.Close()
	page := UserPage{Users: []User{}}
	for rows.Next() {
		user, err := scanUser(rows, &page.Total)
		if err != nil {
			return UserPage{}, err
		}
		page.Users = append(page.Users, user)
	}
	if err := rows.Err(); err != nil {
		return UserPage{}, err
	}
	// Past the last page there are no rows to count with; the total is asked for alone.
	if len(page.Users) == 0 && offset > 0 {
		err = s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE ($1 = '' OR email ILIKE $2 OR name ILIKE $2) AND ($3 = '' OR role = $3)`,
			search, likePattern(search), role).Scan(&page.Total)
	}
	return page, err
}

// SetRole changes an account's role, and refuses to leave the shop without an admin. Every
// admin's row is locked first, in one order, so two admins taking each other's role at the
// same moment can't both see the other one left.
func (s *Store) SetRole(ctx context.Context, id int64, role string) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id FROM users WHERE role = 'admin' ORDER BY id FOR UPDATE")
	if err != nil {
		return User{}, err
	}
	admins, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return User{}, err
	}
	var current string
	err = tx.QueryRow(ctx, "SELECT role FROM users WHERE id = $1 FOR UPDATE", id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, errNotFound
	}
	if err != nil {
		return User{}, err
	}
	if current == roleAdmin && role != roleAdmin && len(admins) <= 1 {
		return User{}, errLastAdmin
	}
	user, err := scanUser(tx.QueryRow(ctx,
		"UPDATE users SET role = $2, updated_at = CASE WHEN role = $2 THEN updated_at ELSE now() END WHERE id = $1 RETURNING "+userColumns, id, role))
	if err != nil {
		return User{}, err
	}
	return user, tx.Commit(ctx)
}

// addAdminRoutes lists accounts and changes roles for admin-service only. Its token is not
// the one other services use to look up a customer, so they can't make anyone an admin.
func addAdminRoutes(mux *http.ServeMux, store *Store, adminToken string) {
	allowed := func(w http.ResponseWriter, r *http.Request) bool {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(bearer), []byte(adminToken)) != 1 {
			writeUnauthorized(w, "the admin token is not valid")
			return false
		}
		w.Header().Set("Cache-Control", "no-store")
		return true
	}
	number := func(value string, fallback, max int) (int, bool) {
		if value == "" {
			return fallback, true
		}
		n, err := strconv.Atoi(value)
		return n, err == nil && n >= 0 && n <= max
	}

	mux.HandleFunc("GET /internal/users", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r) {
			return
		}
		query := r.URL.Query()
		search := strings.TrimSpace(query.Get("q"))
		role := query.Get("role")
		limit, okLimit := number(query.Get("limit"), defaultPage, maxPage)
		offset, okOffset := number(query.Get("offset"), 0, 1_000_000)
		switch {
		case len([]rune(search)) > maxSearchChars:
			writeError(w, http.StatusBadRequest, "q can be at most 200 characters")
			return
		case role != "" && role != roleAdmin && role != roleCustomer:
			writeError(w, http.StatusBadRequest, errBadRole.Error())
			return
		case !okLimit || limit == 0 || !okOffset:
			writeError(w, http.StatusBadRequest, "limit must be 1 to 100 and offset at least 0")
			return
		}
		page, err := store.List(r.Context(), search, role, limit, offset)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})

	mux.HandleFunc("PUT /internal/users/{id}/role", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errNotFound.Error())
			return
		}
		var in struct {
			Role string `json:"role"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.Role != roleAdmin && in.Role != roleCustomer {
			writeError(w, http.StatusBadRequest, errBadRole.Error())
			return
		}
		user, err := store.SetRole(r.Context(), id, in.Role)
		switch {
		case errors.Is(err, errNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, errLastAdmin):
			writeError(w, http.StatusConflict, err.Error())
		case err != nil:
			writeFailure(w, err)
		default:
			writeJSON(w, http.StatusOK, user)
		}
	})
}
