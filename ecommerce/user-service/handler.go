package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxBodyBytes      = 1 << 16
	maxEmailLength    = 254
	maxNameLength     = 200
	minPasswordLength = 8
	maxPasswordLength = 128
	// How long a request waits for a free password slot before answering 503.
	passwordWait = 5 * time.Second
)

// cleanEmail returns the lowercase form of a plain address like name@example.com.
func cleanEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", errors.New("email is required")
	}
	if len(value) > maxEmailLength || strings.ContainsRune(value, 0) {
		return "", errors.New("email is not valid")
	}
	// ParseAddress also accepts forms like "Name <a@b>", so the result must equal the input.
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return "", errors.New("email is not valid")
	}
	return value, nil
}

type registerInput struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (in *registerInput) validate() (err error) {
	if in.Email, err = cleanEmail(in.Email); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return errors.New("name is required")
	case utf8.RuneCountInString(in.Name) > maxNameLength:
		return fmt.Errorf("name must be at most %d characters", maxNameLength)
	case strings.ContainsRune(in.Name, 0):
		return errors.New("name must not contain NUL characters")
	}
	// The password isn't trimmed, because spaces are allowed to be part of it.
	switch length := utf8.RuneCountInString(in.Password); {
	case length < minPasswordLength:
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	case length > maxPasswordLength:
		return fmt.Errorf("password must be at most %d characters", maxPasswordLength)
	}
	return nil
}

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	TokenType string `json:"token_type"`
	ExpiresIn int    `json:"expires_in"`
	User      User   `json:"user"`
}

func newHandler(store *Store, tokens *Tokens, passwords *Passwords) http.Handler {
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

	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, http.StatusOK, tokens.JWKS())
	})

	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var in registerInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), passwordWait)
		defer cancel()
		hash, err := passwords.Hash(ctx, in.Password)
		if err != nil {
			writeFailure(w, err)
			return
		}
		user, err := store.Create(r.Context(), in.Email, in.Name, hash)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, user)
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var in loginInput
		if !readJSON(w, r, &in) {
			return
		}
		if in.Email == "" || in.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required")
			return
		}
		user, ok, err := authenticate(r.Context(), store, passwords, in)
		if err != nil {
			writeFailure(w, err)
			return
		}
		if !ok {
			writeUnauthorized(w, "wrong email or password")
			return
		}
		token, err := tokens.Issue(user)
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, loginResponse{
			Token:     token,
			TokenType: "Bearer",
			ExpiresIn: int(tokens.lifetime.Seconds()),
			User:      user,
		})
	})

	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		bearer, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found {
			writeUnauthorized(w, "a bearer token is required")
			return
		}
		id, err := tokens.UserID(bearer)
		if err != nil {
			writeUnauthorized(w, "the token is not valid")
			return
		}
		user, err := store.ByID(r.Context(), id)
		// The account was deleted after the token was issued.
		if errors.Is(err, errNotFound) {
			writeUnauthorized(w, "the token is not valid")
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, user)
	})

	return mux
}

// authenticate always does one password check, so a login takes as long whether or not the
// email has an account.
func authenticate(ctx context.Context, store *Store, passwords *Passwords, in loginInput) (User, bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, passwordWait)
	defer cancel()
	email, err := cleanEmail(in.Email)
	if err != nil {
		return User{}, false, passwords.WasteTime(waitCtx, in.Password)
	}
	user, hash, err := store.ByEmail(ctx, email)
	if errors.Is(err, errNotFound) {
		return User{}, false, passwords.WasteTime(waitCtx, in.Password)
	}
	if err != nil {
		return User{}, false, err
	}
	ok, err := passwords.Verify(waitCtx, hash, in.Password)
	return user, ok, err
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

func writeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errEmailExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errBusy):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		slog.Error("request failed", "error", err)
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
