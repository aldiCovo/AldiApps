package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"time"
)

const (
	maxBodyBytes     = 1 << 16
	maxUpstreamBytes = 8 << 20
	upstreamTimeout  = 15 * time.Second
	sessionCookie    = "session"
)

//go:embed static
var static embed.FS

var numericID = regexp.MustCompile(`^[0-9]{1,18}$`)

type gateway struct {
	catalogURL, searchURL, userURL, orderURL string
	client                                   *http.Client
}

func newHandler(catalogURL, searchURL, userURL, orderURL string) http.Handler {
	g := &gateway{
		catalogURL: catalogURL,
		searchURL:  searchURL,
		userURL:    userURL,
		orderURL:   orderURL,
		client: &http.Client{
			Timeout: upstreamTimeout,
			// Redirects from a service stay inside the cluster, so they must not reach the browser.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Only these requests pass, so a new endpoint on another service stays private by default.
	mux.HandleFunc("GET /api/categories", func(w http.ResponseWriter, r *http.Request) {
		g.pass(w, r, g.catalogURL+"/categories", nil, "")
	})
	mux.HandleFunc("GET /api/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "product not found")
			return
		}
		g.pass(w, r, g.catalogURL+"/products/"+id, nil, "")
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		g.pass(w, r, g.searchURL+"/search?"+r.URL.RawQuery, nil, "")
	})
	mux.HandleFunc("POST /api/register", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r)
		if ok {
			g.pass(w, r, g.userURL+"/register", body, "")
		}
	})
	mux.HandleFunc("POST /api/login", g.login)
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		setSession(w, "", -1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/me", g.me)
	mux.HandleFunc("POST /api/orders", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.orderURL+"/orders", body)
		}
	})
	mux.HandleFunc("GET /api/orders", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, g.orderURL+"/orders?"+r.URL.RawQuery, nil)
	})
	mux.HandleFunc("GET /api/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := orderPath(w, r); ok {
			g.withSession(w, r, g.orderURL+"/orders/"+id, nil)
		}
	})
	mux.HandleFunc("POST /api/orders/{id}/pay", func(w http.ResponseWriter, r *http.Request) {
		id, ok := orderPath(w, r)
		if !ok {
			return
		}
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.orderURL+"/orders/"+id+"/pay", body)
		}
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServerFS(files)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// isJSON refuses what a form on another site could send, because a browser asks permission
// before sending JSON across sites.
func isJSON(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	return true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if !isJSON(w, r) {
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return nil, false
	case err != nil:
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return nil, false
	}
	return body, true
}

// call sends only what's given here, so the browser's cookies and headers stay out of it.
func (g *gateway) call(r *http.Request, url string, body []byte, bearer string) (*http.Response, []byte, error) {
	method, reader := http.MethodGet, io.Reader(nil)
	if body != nil {
		method, reader = http.MethodPost, bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, url, reader)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBytes))
	return resp, data, err
}

func (g *gateway) pass(w http.ResponseWriter, r *http.Request, url string, body []byte, bearer string) {
	resp, data, err := g.call(r, url, body, bearer)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeRaw(w, resp, data)
}

// login keeps the token in a cookie that page scripts can't read, so injected script can't steal it.
func (g *gateway) login(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	resp, data, err := g.call(r, g.userURL+"/login", body, "")
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeRaw(w, resp, data)
		return
	}
	var login struct {
		Token     string          `json:"token"`
		ExpiresIn int             `json:"expires_in"`
		User      json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal(data, &login); err != nil || login.Token == "" {
		slog.Error("user-service sent an unexpected login response")
		writeError(w, http.StatusBadGateway, "login is not available")
		return
	}
	setSession(w, login.Token, login.ExpiresIn)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(login.User); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}

func orderPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !numericID.MatchString(id) {
		writeError(w, http.StatusNotFound, "order not found")
		return "", false
	}
	return id, true
}

func (g *gateway) me(w http.ResponseWriter, r *http.Request) {
	g.withSession(w, r, g.userURL+"/me", nil)
}

// withSession passes the cookie's token on as the service expects it. Personal answers must
// not be cached, and a token the service refuses is cleared.
func (g *gateway) withSession(w http.ResponseWriter, r *http.Request, url string, body []byte) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	resp, data, err := g.call(r, url, body, cookie.Value)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		setSession(w, "", -1)
	}
	writeRaw(w, resp, data)
}

func setSession(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/api",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func writeRaw(w http.ResponseWriter, resp *http.Response, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(data); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	slog.Error("service request failed", "error", err)
	writeError(w, http.StatusBadGateway, "service is not available")
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}
