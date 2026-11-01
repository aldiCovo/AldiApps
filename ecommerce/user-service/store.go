package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pgUniqueViolation = "23505"

var (
	errNotFound    = errors.New("user not found")
	errEmailExists = errors.New("an account with this email already exists")
)

// User has no password hash, so it can't reach a response by accident.
type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const userColumns = "id, email, name, role, created_at, updated_at"

func scanUser(row pgx.Row, extra ...any) (User, error) {
	var u User
	dest := append([]any{&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &u.UpdatedAt}, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, errNotFound
	}
	return u, err
}

type Store struct {
	pool *pgxpool.Pool
}

func (s *Store) Create(ctx context.Context, email, name, passwordHash string) (User, error) {
	user, err := scanUser(s.pool.QueryRow(ctx,
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING "+userColumns,
		email, name, passwordHash))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return User{}, errEmailExists
	}
	return user, err
}

func (s *Store) ByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", id))
}

// ByEmail also returns the password hash, for checking a login.
func (s *Store) ByEmail(ctx context.Context, email string) (User, string, error) {
	var passwordHash string
	user, err := scanUser(s.pool.QueryRow(ctx,
		"SELECT "+userColumns+", password_hash FROM users WHERE email = $1", email), &passwordHash)
	return user, passwordHash, err
}
