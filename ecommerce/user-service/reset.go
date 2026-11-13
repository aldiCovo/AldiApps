package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	resetLifetime = 30 * time.Minute
	// Stops the form from filling one inbox. Older links stay usable until they expire.
	maxResetsPerHour = 3
	// Links are sent after the answer, so a burst of requests can't pile up without limit.
	maxResetsSending = 4
	resetSendTimeout = time.Minute
	resetTokenBytes  = 32
)

var errResetInvalid = errors.New("the reset link is not valid or has expired")

// Resets sends links for setting a new password, and sets it when a link comes back.
type Resets struct {
	store     *Store
	passwords *Passwords
	mailer    mailer
	shopURL   string
	slots     chan struct{}
	sending   sync.WaitGroup
}

func newResets(store *Store, passwords *Passwords, mailer mailer, shopURL string) *Resets {
	return &Resets{
		store:     store,
		passwords: passwords,
		mailer:    mailer,
		shopURL:   strings.TrimRight(shopURL, "/"),
		slots:     make(chan struct{}, maxResetsSending),
	}
}

// Wait returns when every link that was being sent is out.
func (rs *Resets) Wait() { rs.sending.Wait() }

// Request answers before looking the email up, so the answer and its timing are the same
// whether or not the email has an account.
func (rs *Resets) Request(email string) {
	select {
	case rs.slots <- struct{}{}:
	default:
		slog.Warn("too many reset links being sent, dropping one")
		return
	}
	rs.sending.Go(func() {
		defer func() { <-rs.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), resetSendTimeout)
		defer cancel()
		if err := rs.send(ctx, email); err != nil {
			slog.Error("sending a reset link failed", "error", err)
		}
	})
}

func (rs *Resets) send(ctx context.Context, email string) error {
	user, _, err := rs.store.ByEmail(ctx, email)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	token, created, err := rs.store.CreateReset(ctx, user.ID)
	if err != nil {
		return err
	}
	if !created {
		slog.Warn("reset links per hour reached", "user_id", user.ID)
		return nil
	}
	link := rs.shopURL + "/#reset=" + token
	var body strings.Builder
	fmt.Fprintf(&body, "Halo %s,\r\n\r\n", user.Name)
	body.WriteString("Ada permintaan untuk mengganti password akun Prastika kamu. Buka link ini untuk membuat password baru:\r\n\r\n")
	body.WriteString(link + "\r\n\r\n")
	fmt.Fprintf(&body, "Link ini berlaku %d menit dan hanya bisa dipakai sekali.\r\n\r\n", int(resetLifetime.Minutes()))
	body.WriteString("Kalau kamu tidak meminta ini, abaikan saja email ini. Password kamu tidak berubah.\r\n")
	if err := rs.mailer.Send(Email{To: user, Subject: "Buat password baru untuk akun Prastika", Body: body.String()}); err != nil {
		return fmt.Errorf("sending the email: %w", err)
	}
	slog.Info("sent a reset link", "user_id", user.ID)
	return nil
}

// Reset sets a new password with a token from a link. Every other unused link of the account
// stops working too.
func (rs *Resets) Reset(ctx context.Context, token, password string) error {
	tokenHash, ok := hashResetToken(token)
	if !ok {
		return errResetInvalid
	}
	// Checked before hashing, so a made-up token costs no hash.
	if err := rs.store.CheckReset(ctx, tokenHash); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, passwordWait)
	defer cancel()
	hash, err := rs.passwords.Hash(waitCtx, password)
	if err != nil {
		return err
	}
	return rs.store.UseReset(ctx, tokenHash, hash)
}

func newResetToken() (string, []byte, error) {
	raw := make([]byte, resetTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

func hashResetToken(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != resetTokenBytes {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

// CreateReset stores a new link for the account, unless it already had maxResetsPerHour in
// the last hour. The account's row is locked, so requests at once can't pass the limit together.
func (s *Store) CreateReset(ctx context.Context, userID int64) (string, bool, error) {
	token, tokenHash, err := newResetToken()
	if err != nil {
		return "", false, err
	}
	created := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var locked int64
		if err := tx.QueryRow(ctx, "SELECT id FROM users WHERE id = $1 FOR UPDATE", userID).Scan(&locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		// Old links are only kept for the limit above.
		if _, err := tx.Exec(ctx,
			"DELETE FROM password_resets WHERE user_id = $1 AND created_at < now() - interval '1 day'", userID); err != nil {
			return err
		}
		var recent int
		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM password_resets WHERE user_id = $1 AND created_at > now() - interval '1 hour'",
			userID).Scan(&recent); err != nil {
			return err
		}
		if recent >= maxResetsPerHour {
			return nil
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, now() + $3 * interval '1 second')",
			tokenHash, userID, int(resetLifetime.Seconds())); err != nil {
			return err
		}
		created = true
		return nil
	})
	return token, created, err
}

func (s *Store) CheckReset(ctx context.Context, tokenHash []byte) error {
	var userID int64
	err := s.pool.QueryRow(ctx,
		"SELECT user_id FROM password_resets WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()",
		tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errResetInvalid
	}
	return err
}

// UseReset marks the link used and sets the password in one transaction, so a link used twice
// at once sets only one of the passwords.
func (s *Store) UseReset(ctx context.Context, tokenHash []byte, passwordHash string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var userID int64
		err := tx.QueryRow(ctx,
			`UPDATE password_resets SET used_at = now()
			 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now() RETURNING user_id`,
			tokenHash).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errResetInvalid
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			// The link came by email, so it proves the email too.
			`UPDATE users SET password_hash = $2, email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
			 WHERE id = $1`, userID, passwordHash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			"UPDATE password_resets SET used_at = now() WHERE user_id = $1 AND used_at IS NULL", userID); err != nil {
			return err
		}
		// Someone else guessing the password must not keep the owner out after a new one.
		_, err = tx.Exec(ctx,
			"DELETE FROM login_failures WHERE email = (SELECT email FROM users WHERE id = $1)", userID)
		return err
	})
}

type forgotInput struct {
	Email string `json:"email"`
}

type resetInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func addResetRoutes(mux *http.ServeMux, resets *Resets) {
	// Answers the same whether or not the email has an account, so it can't be used to find one.
	mux.HandleFunc("POST /password/forgot", func(w http.ResponseWriter, r *http.Request) {
		var in forgotInput
		if !readJSON(w, r, &in) {
			return
		}
		email, err := cleanEmail(in.Email)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resets.Request(email)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /password/reset", func(w http.ResponseWriter, r *http.Request) {
		var in resetInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := checkPassword(in.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		err := resets.Reset(r.Context(), in.Token, in.Password)
		if errors.Is(err, errResetInvalid) {
			writeError(w, http.StatusGone, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
