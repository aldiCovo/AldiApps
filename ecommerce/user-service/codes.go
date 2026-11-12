package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	codeLifetime = 10 * time.Minute
	codeDigits   = 6
	// Tries per code. A new code ends the older ones, so with maxCodesPerHour a stranger gets
	// at most 25 guesses an hour out of a million.
	maxCodeAttempts = 5
	maxCodesPerHour = 5
	// Email codes are sent after the answer, like reset links.
	maxCodesSending = 4
	codeSendTimeout = time.Minute

	channelEmail    = "email"
	channelWhatsApp = "whatsapp"
)

var (
	errCodeInvalid   = errors.New("the code is wrong or has expired")
	errTooManyCodes  = errors.New("too many codes were sent, try again later")
	errNoPhone       = errors.New("the account has no phone number")
	errPhoneVerified = errors.New("the phone number is already verified")
	errNoWhatsApp    = errors.New("WhatsApp codes are not available")
	errNotVerified   = errors.New("the email is not verified")
)

// whatsApp sends a text to an Indonesian mobile number like 081234567890.
type whatsApp interface {
	Send(ctx context.Context, phone, text string) error
}

// Codes sends the codes that prove an email or a phone belongs to the account, and checks them.
type Codes struct {
	store    *Store
	mailer   mailer
	whatsApp whatsApp // nil when WhatsApp isn't set up
	slots    chan struct{}
	sending  sync.WaitGroup
}

func newCodes(store *Store, mailer mailer, whatsApp whatsApp) *Codes {
	return &Codes{store: store, mailer: mailer, whatsApp: whatsApp, slots: make(chan struct{}, maxCodesSending)}
}

// Wait returns when every email code that was being sent is out.
func (c *Codes) Wait() { c.sending.Wait() }

// RequestEmail sends a code to an account's email after the answer, so the answer and its
// timing don't tell whether the email has an account. Verified emails get nothing.
func (c *Codes) RequestEmail(email string) {
	select {
	case c.slots <- struct{}{}:
	default:
		slog.Warn("too many email codes being sent, dropping one")
		return
	}
	c.sending.Go(func() {
		defer func() { <-c.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), codeSendTimeout)
		defer cancel()
		if err := c.sendEmail(ctx, email); err != nil {
			slog.Error("sending an email code failed", "error", err)
		}
	})
}

func (c *Codes) sendEmail(ctx context.Context, email string) error {
	user, verified, err := c.store.EmailStatus(ctx, email)
	if errors.Is(err, errNotFound) || verified {
		return nil
	}
	if err != nil {
		return err
	}
	code, err := c.store.CreateCode(ctx, user.ID, channelEmail, user.Email)
	if errors.Is(err, errTooManyCodes) {
		slog.Warn("email codes per hour reached", "user_id", user.ID)
		return nil
	}
	if err != nil {
		return err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Halo %s,\r\n\r\n", user.Name)
	body.WriteString("Ini kode untuk memastikan email ini milikmu:\r\n\r\n")
	body.WriteString("    " + code + "\r\n\r\n")
	fmt.Fprintf(&body, "Masukkan kode itu di Prastika. Kode berlaku %d menit.\r\n\r\n", int(codeLifetime.Minutes()))
	body.WriteString("Kalau kamu tidak mendaftar di Prastika, abaikan saja email ini.\r\n")
	if err := c.mailer.Send(Email{To: user, Subject: code + " adalah kode verifikasi Prastika", Body: body.String()}); err != nil {
		return fmt.Errorf("sending the email: %w", err)
	}
	slog.Info("sent an email code", "user_id", user.ID)
	return nil
}

// VerifyEmail checks a code for an email and marks the email verified.
func (c *Codes) VerifyEmail(ctx context.Context, email, code string) (User, error) {
	user, verified, err := c.store.EmailStatus(ctx, email)
	if errors.Is(err, errNotFound) {
		return User{}, errCodeInvalid
	}
	if err != nil {
		return User{}, err
	}
	if verified {
		return User{}, errCodeInvalid
	}
	return user, c.store.UseCode(ctx, user.ID, channelEmail, code)
}

// SendWhatsApp sends a code to the account's phone, and answers only once it's out, so the
// customer hears at once when the number can't get messages.
func (c *Codes) SendWhatsApp(ctx context.Context, userID int64) error {
	if c.whatsApp == nil {
		return errNoWhatsApp
	}
	profile, err := c.store.Profile(ctx, userID)
	if err != nil {
		return err
	}
	switch {
	case profile.Phone == "":
		return errNoPhone
	case profile.PhoneVerified:
		return errPhoneVerified
	}
	code, err := c.store.CreateCode(ctx, userID, channelWhatsApp, profile.Phone)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("Kode verifikasi Prastika kamu: *%s*\n\nBerlaku %d menit. Jangan berikan kode ini kepada siapa pun, termasuk yang mengaku dari Prastika.",
		code, int(codeLifetime.Minutes()))
	if err := c.whatsApp.Send(ctx, profile.Phone, text); err != nil {
		return fmt.Errorf("sending the WhatsApp message: %w", err)
	}
	slog.Info("sent a WhatsApp code", "user_id", userID)
	return nil
}

func (c *Codes) VerifyWhatsApp(ctx context.Context, userID int64, code string) error {
	return c.store.UseCode(ctx, userID, channelWhatsApp, code)
}

func newCode() (string, error) {
	limit := big.NewInt(1)
	for range codeDigits {
		limit.Mul(limit, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", codeDigits, n), nil
}

// hashCode mixes in the code's row, so the same digits hash differently in each row.
func hashCode(id int64, code string) []byte {
	var prefix [8]byte
	binary.BigEndian.PutUint64(prefix[:], uint64(id))
	sum := sha256.Sum256(append(prefix[:], code...))
	return sum[:]
}

// CreateCode stores a new code for the account and ends its older ones on the channel, unless
// maxCodesPerHour were already sent. The account's row is locked, so requests at once can't
// pass the limit together.
func (s *Store) CreateCode(ctx context.Context, userID int64, channel, target string) (string, error) {
	code, err := newCode()
	if err != nil {
		return "", err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var locked int64
		if err := tx.QueryRow(ctx, "SELECT id FROM users WHERE id = $1 FOR UPDATE", userID).Scan(&locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		// Old codes are only kept for the limit above.
		if _, err := tx.Exec(ctx,
			"DELETE FROM verification_codes WHERE user_id = $1 AND created_at < now() - interval '1 day'", userID); err != nil {
			return err
		}
		var recent int
		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM verification_codes WHERE user_id = $1 AND channel = $2 AND created_at > now() - interval '1 hour'",
			userID, channel).Scan(&recent); err != nil {
			return err
		}
		if recent >= maxCodesPerHour {
			return errTooManyCodes
		}
		if _, err := tx.Exec(ctx,
			"UPDATE verification_codes SET used_at = now() WHERE user_id = $1 AND channel = $2 AND used_at IS NULL",
			userID, channel); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO verification_codes (user_id, channel, target, code_hash, expires_at)
			 VALUES ($1, $2, $3, '', now() + $4 * interval '1 second') RETURNING id`,
			userID, channel, target, int(codeLifetime.Seconds())).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE verification_codes SET code_hash = $2 WHERE id = $1", id, hashCode(id, code))
		return err
	})
	return code, err
}

// UseCode checks a code against the account's newest one on the channel. A right code marks the
// email or phone verified, if it is still the one the code went to. Every try counts, right
// or wrong, and the row is locked, so tries at once can't pass maxCodeAttempts.
func (s *Store) UseCode(ctx context.Context, userID int64, channel, code string) error {
	if len(code) != codeDigits || strings.Trim(code, "0123456789") != "" {
		return errCodeInvalid
	}
	right := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		var target string
		var codeHash []byte
		var attempts int
		err := tx.QueryRow(ctx,
			`SELECT id, target, code_hash, attempts FROM verification_codes
			 WHERE user_id = $1 AND channel = $2 AND used_at IS NULL AND expires_at > now()
			 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
			userID, channel).Scan(&id, &target, &codeHash, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		right = subtle.ConstantTimeCompare(hashCode(id, code), codeHash) == 1
		// A right code, or the last try, ends the code.
		ended := right || attempts+1 >= maxCodeAttempts
		_, err = tx.Exec(ctx,
			"UPDATE verification_codes SET attempts = attempts + 1, used_at = CASE WHEN $2 THEN now() END WHERE id = $1",
			id, ended)
		if err != nil || !right {
			return err
		}
		column, current := "email_verified_at", "email"
		if channel == channelWhatsApp {
			column, current = "phone_verified_at", "phone"
		}
		tag, err := tx.Exec(ctx,
			"UPDATE users SET "+column+" = now(), updated_at = now() WHERE id = $1 AND "+current+" = $2", userID, target)
		if err == nil && tag.RowsAffected() == 0 {
			right = false
		}
		return err
	})
	if err != nil {
		return err
	}
	if !right {
		return errCodeInvalid
	}
	return nil
}

// EmailStatus finds an account by email and says whether its email is verified.
func (s *Store) EmailStatus(ctx context.Context, email string) (User, bool, error) {
	var verified bool
	user, err := scanUser(s.pool.QueryRow(ctx,
		"SELECT "+userColumns+", email_verified_at IS NOT NULL FROM users WHERE email = $1", email), &verified)
	return user, verified, err
}

type emailInput struct {
	Email string `json:"email"`
}

type emailCodeInput struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type codeInput struct {
	Code string `json:"code"`
}

func addCodeRoutes(mux *http.ServeMux, store *Store, tokens *Tokens, codes *Codes, tokenUser func(http.ResponseWriter, *http.Request) (int64, bool)) {
	// Answers the same whether or not the email has an account, so it can't be used to find one.
	mux.HandleFunc("POST /email/code", func(w http.ResponseWriter, r *http.Request) {
		var in emailInput
		if !readJSON(w, r, &in) {
			return
		}
		email, err := cleanEmail(in.Email)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		codes.RequestEmail(email)
		w.WriteHeader(http.StatusNoContent)
	})

	// A right code proves the customer reads the email, so it logs them in like a password.
	mux.HandleFunc("POST /email/verify", func(w http.ResponseWriter, r *http.Request) {
		var in emailCodeInput
		if !readJSON(w, r, &in) {
			return
		}
		email, err := cleanEmail(in.Email)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		user, err := codes.VerifyEmail(r.Context(), email, strings.TrimSpace(in.Code))
		if errors.Is(err, errCodeInvalid) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeLogin(w, r, store, tokens, user)
	})

	mux.HandleFunc("POST /me/phone/code", func(w http.ResponseWriter, r *http.Request) {
		id, ok := tokenUser(w, r)
		if !ok {
			return
		}
		err := codes.SendWhatsApp(r.Context(), id)
		switch {
		case errors.Is(err, errNotFound):
			writeUnauthorized(w, "the token is not valid")
		case errors.Is(err, errNoPhone), errors.Is(err, errPhoneVerified):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, errTooManyCodes):
			w.Header().Set("Retry-After", strconv.Itoa(int(time.Hour.Seconds())))
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, errNoWhatsApp):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		case err != nil:
			// Usually the number has no WhatsApp, or the sender is down.
			slog.Error("sending a WhatsApp code failed", "user_id", id, "error", err)
			writeError(w, http.StatusBadGateway, "the WhatsApp message could not be sent")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	mux.HandleFunc("POST /me/phone/verify", func(w http.ResponseWriter, r *http.Request) {
		id, ok := tokenUser(w, r)
		if !ok {
			return
		}
		var in codeInput
		if !readJSON(w, r, &in) {
			return
		}
		err := codes.VerifyWhatsApp(r.Context(), id, strings.TrimSpace(in.Code))
		if errors.Is(err, errCodeInvalid) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		profile, err := store.Profile(r.Context(), id)
		if errors.Is(err, errNotFound) {
			writeUnauthorized(w, "the token is not valid")
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, profile)
	})
}
