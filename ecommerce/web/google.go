package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const (
	googleAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	// Holds the state and PKCE verifier between leaving for Google and coming back.
	googleCookie     = "google_login"
	googleCookiePath = "/api/auth/google"
	googleCookieAge  = 600
	// Where the page lands after a Google login that didn't work. app.js reads it.
	googleFailed = "/#login=google-failed"
)

// googleConfig is empty when the shop has no Google login.
type googleConfig struct {
	clientID string
	// SHOP_URL + /api/auth/google/callback, as registered in Google Cloud.
	redirectURI string
}

func randomText() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func setGoogleCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     googleCookie,
		Value:    value,
		Path:     googleCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		// Lax still sends it on the top-level GET that comes back from Google.
		SameSite: http.SameSiteLaxMode,
	})
}

func addGoogleRoutes(mux *http.ServeMux, g *gateway, google googleConfig) {
	mux.HandleFunc("GET /api/auth/options", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"google": google.clientID != ""})
	})

	// The state stops another site from logging the browser into the wrong account, and PKCE
	// stops a code that leaked from the address bar from being used elsewhere.
	mux.HandleFunc("GET /api/auth/google", func(w http.ResponseWriter, r *http.Request) {
		if google.clientID == "" {
			writeError(w, http.StatusNotFound, "Google login is not set up")
			return
		}
		state, verifier := randomText(), randomText()
		setGoogleCookie(w, state+"."+verifier, googleCookieAge)
		challenge := sha256.Sum256([]byte(verifier))
		query := url.Values{
			"client_id":             {google.clientID},
			"redirect_uri":          {google.redirectURI},
			"response_type":         {"code"},
			"scope":                 {"openid email profile"},
			"state":                 {state},
			"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
			"code_challenge_method": {"S256"},
			"prompt":                {"select_account"},
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, googleAuthURL+"?"+query.Encode(), http.StatusFound)
	})

	mux.HandleFunc("GET /api/auth/google/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if google.clientID == "" {
			writeError(w, http.StatusNotFound, "Google login is not set up")
			return
		}
		cookie, err := r.Cookie(googleCookie)
		setGoogleCookie(w, "", -1)
		if err != nil {
			http.Redirect(w, r, googleFailed, http.StatusSeeOther)
			return
		}
		state, verifier, _ := strings.Cut(cookie.Value, ".")
		code := r.URL.Query().Get("code")
		// Without a code the customer pressed cancel at Google.
		if code == "" || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 {
			http.Redirect(w, r, googleFailed, http.StatusSeeOther)
			return
		}
		body, _ := json.Marshal(map[string]string{
			"code":          code,
			"code_verifier": verifier,
			"redirect_uri":  google.redirectURI,
		})
		token, ok := g.startSession(r, g.userURL+"/google", body)
		if !ok {
			http.Redirect(w, r, googleFailed, http.StatusSeeOther)
			return
		}
		setSession(w, token.Token, token.ExpiresIn)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}

type session struct {
	Token     string          `json:"token"`
	ExpiresIn int             `json:"expires_in"`
	User      json.RawMessage `json:"user"`
}

// startSession asks user-service for a token, and gives it back only for a proper answer.
func (g *gateway) startSession(r *http.Request, url string, body []byte) (session, bool) {
	resp, data, err := g.call(r, url, body, "")
	if err != nil {
		slog.Error("service request failed", "error", err)
		return session{}, false
	}
	if resp.StatusCode != http.StatusOK {
		return session{}, false
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil || s.Token == "" {
		slog.Error("user-service sent an unexpected login response")
		return session{}, false
	}
	return s, true
}
