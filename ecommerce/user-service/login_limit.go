package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxLoginFailures   = 10
	loginFailureWindow = 15 * time.Minute
)

// errTooManyLogins carries how long until the email may try again.
type errTooManyLogins struct{ retryAfter time.Duration }

func (e errTooManyLogins) Error() string { return "too many wrong passwords, try again later" }

// StartLogin counts a password try for the email before the password is checked, so tries at
// once can't pass the limit together. The try counts as wrong until FinishLogin says otherwise.
// It answers the try's id, or errTooManyLogins when the email had maxLoginFailures already.
func (s *Store) StartLogin(ctx context.Context, email string) (int64, error) {
	var id int64
	window := int(loginFailureWindow.Seconds())
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('login:' || $1))", email); err != nil {
			return err
		}
		// Old tries of every email go here, so the table stays small without a job.
		if _, err := tx.Exec(ctx,
			"DELETE FROM login_failures WHERE created_at < now() - $1 * interval '1 second'", window); err != nil {
			return err
		}
		var count int
		var oldest *time.Time
		if err := tx.QueryRow(ctx,
			"SELECT count(*), min(created_at) FROM login_failures WHERE email = $1", email).Scan(&count, &oldest); err != nil {
			return err
		}
		if count >= maxLoginFailures {
			retry := time.Minute
			if oldest != nil {
				retry = max(time.Until(oldest.Add(loginFailureWindow)), time.Second)
			}
			return errTooManyLogins{retryAfter: retry}
		}
		return tx.QueryRow(ctx, "INSERT INTO login_failures (email) VALUES ($1) RETURNING id", email).Scan(&id)
	})
	return id, err
}

// FinishLogin settles a try from StartLogin. A right password forgets the email's wrong ones; a
// try that failed for another reason, like a busy server, doesn't count.
func (s *Store) FinishLogin(ctx context.Context, email string, id int64, right bool) error {
	var err error
	if right {
		_, err = s.pool.Exec(ctx, "DELETE FROM login_failures WHERE email = $1", email)
	} else {
		_, err = s.pool.Exec(ctx, "DELETE FROM login_failures WHERE id = $1", id)
	}
	return err
}

func isTooManyLogins(err error) (errTooManyLogins, bool) {
	var limit errTooManyLogins
	ok := errors.As(err, &limit)
	return limit, ok
}
