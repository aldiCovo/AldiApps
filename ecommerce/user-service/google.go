package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const googleTokenURL = "https://oauth2.googleapis.com/token"

var (
	errGoogleRefused = errors.New("google did not accept the login")
	// The account with this email is already linked to another Google account.
	errGoogleOther = errors.New("the email belongs to an account linked to another Google account")
)

// Google turns the code Google gives the browser into the Google account that logged in.
type Google struct {
	clientID, clientSecret string
	tokenURL               string
	client                 *http.Client
}

func newGoogle(clientID, clientSecret string) *Google {
	return &Google{
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenURL:     googleTokenURL,
		client:       &http.Client{Timeout: 10 * time.Second},
	}
}

type googleIdentity struct {
	Subject string
	Email   string
	Name    string
}

// Identity trades the code for an ID token. The token comes straight from Google over TLS,
// which OpenID Connect counts as proof it is Google's, so its signature isn't checked again.
// Its audience, issuer and expiry still are.
func (g *Google) Identity(ctx context.Context, code, codeVerifier, redirectURI string) (googleIdentity, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {codeVerifier},
		"redirect_uri":  {redirectURI},
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return googleIdentity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.client.Do(req)
	if err != nil {
		return googleIdentity{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return googleIdentity{}, err
	}
	// 400 is a used, expired or made-up code.
	if resp.StatusCode == http.StatusBadRequest {
		return googleIdentity{}, errGoogleRefused
	}
	if resp.StatusCode != http.StatusOK {
		return googleIdentity{}, fmt.Errorf("google answered %d", resp.StatusCode)
	}
	var answer struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(data, &answer); err != nil || answer.IDToken == "" {
		return googleIdentity{}, errors.New("google sent no ID token")
	}
	return g.readIDToken(answer.IDToken, time.Now())
}

func (g *Google) readIDToken(token string, now time.Time) (googleIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return googleIdentity{}, errors.New("the ID token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return googleIdentity{}, errors.New("the ID token is not a JWT")
	}
	var claims struct {
		Issuer        string `json:"iss"`
		Audience      string `json:"aud"`
		Expires       int64  `json:"exp"`
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return googleIdentity{}, errors.New("the ID token has bad claims")
	}
	switch {
	case claims.Issuer != "https://accounts.google.com" && claims.Issuer != "accounts.google.com":
		return googleIdentity{}, fmt.Errorf("the ID token is from %q", claims.Issuer)
	case claims.Audience != g.clientID:
		return googleIdentity{}, errors.New("the ID token is for another app")
	case now.Unix() >= claims.Expires:
		return googleIdentity{}, errors.New("the ID token has expired")
	case claims.Subject == "":
		return googleIdentity{}, errors.New("the ID token has no subject")
	}
	email, err := cleanEmail(claims.Email)
	// Only an email Google has checked may take over an account with the same email.
	if err != nil || !claims.EmailVerified {
		return googleIdentity{}, errGoogleRefused
	}
	name := strings.TrimSpace(strings.ReplaceAll(claims.Name, "\x00", ""))
	if name == "" {
		name = email[:strings.IndexByte(email, '@')]
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		name = string([]rune(name)[:maxNameLength])
	}
	return googleIdentity{Subject: claims.Subject, Email: email, Name: name}, nil
}

// GoogleUser finds the account of a Google login: the one linked to it, else the one with its
// email, which is then linked, else a new one with no password. Google checked the email, so
// it counts as verified.
func (s *Store) GoogleUser(ctx context.Context, id googleIdentity) (User, error) {
	var user User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		user, err = scanUser(tx.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE google_sub = $1", id.Subject))
		if !errors.Is(err, errNotFound) {
			return err
		}
		var linked *string
		user, err = scanUser(tx.QueryRow(ctx,
			"SELECT "+userColumns+", google_sub FROM users WHERE email = $1 FOR UPDATE", id.Email), &linked)
		switch {
		case err == nil && linked != nil:
			return errGoogleOther
		case err == nil:
			_, err = tx.Exec(ctx,
				`UPDATE users SET google_sub = $2, email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
				 WHERE id = $1`, user.ID, id.Subject)
			return err
		case errors.Is(err, errNotFound):
			user, err = scanUser(tx.QueryRow(ctx,
				`INSERT INTO users (email, name, password_hash, google_sub, email_verified_at)
				 VALUES ($1, $2, '', $3, now()) RETURNING `+userColumns,
				id.Email, id.Name, id.Subject))
			return err
		default:
			return err
		}
	})
	var pgErr *pgconn.PgError
	// Another login with the same Google account or email got there first, so it now exists.
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE google_sub = $1", id.Subject))
	}
	return user, err
}

type googleInput struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

func addGoogleRoutes(mux *http.ServeMux, store *Store, tokens *Tokens, google *Google) {
	mux.HandleFunc("POST /google", func(w http.ResponseWriter, r *http.Request) {
		if google == nil {
			writeError(w, http.StatusNotFound, "Google login is not set up")
			return
		}
		var in googleInput
		if !readJSON(w, r, &in) {
			return
		}
		if in.Code == "" || in.CodeVerifier == "" || in.RedirectURI == "" {
			writeError(w, http.StatusBadRequest, "code, code_verifier and redirect_uri are required")
			return
		}
		identity, err := google.Identity(r.Context(), in.Code, in.CodeVerifier, in.RedirectURI)
		if errors.Is(err, errGoogleRefused) {
			writeUnauthorized(w, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, fmt.Errorf("asking Google: %w", err))
			return
		}
		user, err := store.GoogleUser(r.Context(), identity)
		if errors.Is(err, errGoogleOther) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeLogin(w, r, store, tokens, user)
	})
}
