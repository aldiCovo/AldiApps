package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	maxBodyBytes     = 1 << 16
	maxPhotoBytes    = 5 << 20
	maxUpstreamBytes = 8 << 20
	upstreamTimeout  = 15 * time.Second
	// Its own name and path, so the shop's session never reaches these pages, nor this one the shop.
	sessionCookie = "admin_session"
	sessionPath   = "/admin/api"
)

//go:embed static
var static embed.FS

var (
	numericID = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
	photoSlot = regexp.MustCompile(`^[1-4]$`)
	// A form on another site can't send these types without the browser asking first.
	photoTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}
)

type gateway struct {
	adminURL string
	client   *http.Client
}

func newHandler(adminURL string) http.Handler {
	g := &gateway{adminURL: adminURL, client: &http.Client{
		Timeout:       upstreamTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Only these requests pass to admin-service.
	mux.HandleFunc("POST /admin/api/login", g.login)
	mux.HandleFunc("POST /admin/api/logout", func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		setSession(w, "", -1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /admin/api/me", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, http.MethodGet, "/me", nil)
	})
	mux.HandleFunc("GET /admin/api/orders", func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{}
		for _, name := range []string{"status", "limit", "offset"} {
			if value := r.URL.Query().Get(name); value != "" {
				query.Set(name, value)
			}
		}
		g.withSession(w, r, http.MethodGet, "/orders?"+query.Encode(), nil)
	})
	mux.HandleFunc("GET /admin/api/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := pathID(w, r); ok {
			g.withSession(w, r, http.MethodGet, "/orders/"+id, nil)
		}
	})
	mux.HandleFunc("POST /admin/api/orders/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, http.MethodPost, "/orders/"+id+"/status", body)
		}
	})
	// Products, variants, prices and stock.
	for _, route := range []struct {
		method, path string
		body         bool
	}{
		{http.MethodGet, "/categories", false},
		{http.MethodPost, "/categories", true},
		{http.MethodPut, "/categories/{id}", true},
		{http.MethodDelete, "/categories/{id}", false},
		{http.MethodGet, "/products", false},
		{http.MethodGet, "/products/{id}", false},
		{http.MethodPost, "/products", true},
		{http.MethodPut, "/products/{id}", true},
		{http.MethodDelete, "/products/{id}", false},
		{http.MethodPost, "/products/{id}/variants", true},
		{http.MethodPut, "/variants/{id}", true},
		{http.MethodDelete, "/variants/{id}", false},
		{http.MethodPatch, "/variants/{id}/stock", true},
		{http.MethodGet, "/vouchers", false},
		{http.MethodPost, "/vouchers", true},
		{http.MethodPut, "/vouchers/{id}", true},
		{http.MethodDelete, "/vouchers/{id}", false},
	} {
		mux.HandleFunc(route.method+" /admin/api"+route.path, func(w http.ResponseWriter, r *http.Request) {
			path := route.path
			if strings.Contains(path, "{id}") {
				id, ok := pathID(w, r)
				if !ok {
					return
				}
				path = strings.Replace(path, "{id}", id, 1)
			}
			var body []byte
			if route.body {
				var ok bool
				if body, ok = readBody(w, r); !ok {
					return
				}
			} else if route.method != http.MethodGet && !isJSON(w, r) {
				// A DELETE also needs the JSON header, so a form on another site can't send one.
				return
			}
			if route.method == http.MethodGet {
				query := url.Values{}
				for _, name := range []string{"limit", "offset", "category_id"} {
					if value := r.URL.Query().Get(name); value != "" {
						query.Set(name, value)
					}
				}
				if len(query) > 0 {
					path += "?" + query.Encode()
				}
			}
			g.withSession(w, r, route.method, path, body)
		})
	}
	g.addPhotoRoutes(mux)
	mux.HandleFunc("GET /admin/api/users", func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{}
		for _, name := range []string{"q", "role", "limit", "offset"} {
			if value := r.URL.Query().Get(name); value != "" {
				query.Set(name, value)
			}
		}
		g.withSession(w, r, http.MethodGet, "/users?"+query.Encode(), nil)
	})
	mux.HandleFunc("PUT /admin/api/users/{id}/role", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, http.MethodPut, "/users/"+id+"/role", body)
		}
	})
	mux.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	page, assets := versionedPage(files)
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(page))
	})
	fileServer := http.StripPrefix("/admin", http.FileServerFS(files))
	mux.HandleFunc("GET /admin/{file}", func(w http.ResponseWriter, r *http.Request) {
		path := "/" + r.PathValue("file")
		version, known := assets[path]
		if !known {
			http.NotFound(w, r)
			return
		}
		// During a rollout, another version's page can reach this Pod. Answering with this
		// version's file would let the browser keep it under that URL.
		if r.URL.Query().Get("v") != version {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})

	return securityHeaders(mux)
}

// versionedPage returns index.html with a hash of each file's content in its URL, because
// Cloudflare keeps CSS and JavaScript for hours whatever this server says.
func versionedPage(files fs.FS) ([]byte, map[string]string) {
	page, err := fs.ReadFile(files, "index.html")
	if err != nil {
		panic(err)
	}
	assets := make(map[string]string)
	for _, name := range []string{"admin.css", "admin.js", "products.js", "photos.js", "users.js", "vouchers.js"} {
		content, err := fs.ReadFile(files, name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(content)
		version := hex.EncodeToString(sum[:6])
		quoted := `"/admin/` + name + `"`
		if !bytes.Contains(page, []byte(quoted)) {
			panic("index.html does not link /admin/" + name)
		}
		page = []byte(strings.ReplaceAll(string(page), quoted, `"/admin/`+name+"?v="+version+`"`))
		assets["/"+name] = version
	}
	return page, assets
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "same-origin")
		// Search engines have no business with the admin pages.
		header.Set("X-Robots-Tag", "noindex, nofollow")
		next.ServeHTTP(w, r)
	})
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !numericID.MatchString(id) {
		writeError(w, http.StatusNotFound, "not found")
		return "", false
	}
	return id, true
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
func (g *gateway) call(r *http.Request, method, path string, body []byte, bearer string) (*http.Response, []byte, error) {
	return g.callTyped(r, method, path, body, "application/json", bearer)
}

func (g *gateway) callTyped(r *http.Request, method, path string, body []byte, contentType, bearer string) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, g.adminURL+path, reader)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
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

// login keeps the token in a cookie that page scripts can't read, so injected script can't steal it.
func (g *gateway) login(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	resp, data, err := g.call(r, http.MethodPost, "/login", body, "")
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeRaw(w, resp.StatusCode, data)
		return
	}
	var login struct {
		Token     string          `json:"token"`
		ExpiresIn int             `json:"expires_in"`
		User      json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal(data, &login); err != nil || login.Token == "" || login.ExpiresIn <= 0 {
		slog.Error("admin-service sent an unexpected login response")
		writeError(w, http.StatusBadGateway, "login is not available")
		return
	}
	setSession(w, login.Token, login.ExpiresIn)
	writeRaw(w, http.StatusOK, login.User)
}

// withSession passes the cookie's token on. A token admin-service refuses is cleared.
func (g *gateway) withSession(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
	g.withSessionTyped(w, r, method, path, body, "application/json")
}

func (g *gateway) withSessionTyped(w http.ResponseWriter, r *http.Request, method, path string, body []byte, contentType string) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	resp, data, err := g.callTyped(r, method, path, body, contentType, cookie.Value)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		setSession(w, "", -1)
	}
	writeRaw(w, resp.StatusCode, data)
}

func setSession(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     sessionPath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func writeRaw(w http.ResponseWriter, status int, data []byte) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	slog.Error("admin-service request failed", "error", err)
	writeError(w, http.StatusBadGateway, "service is not available")
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}

// addPhotoRoutes passes photo uploads on as they are; admin-service checks what they contain.
func (g *gateway) addPhotoRoutes(mux *http.ServeMux) {
	productSlot := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		id, slot := r.PathValue("id"), r.PathValue("slot")
		if !numericID.MatchString(id) || !photoSlot.MatchString(slot) {
			writeError(w, http.StatusNotFound, "not found")
			return "", false
		}
		return "/photos/products/" + id + "/" + slot, true
	}
	mux.HandleFunc("GET /admin/api/photos/products", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, http.MethodGet, "/photos/products", nil)
	})
	mux.HandleFunc("PUT /admin/api/photos/products/{id}/{slot}", func(w http.ResponseWriter, r *http.Request) {
		path, ok := productSlot(w, r)
		if !ok {
			return
		}
		if photo, contentType, ok := readPhoto(w, r); ok {
			g.withSessionTyped(w, r, http.MethodPut, path, photo, contentType)
		}
	})
	mux.HandleFunc("DELETE /admin/api/photos/products/{id}/{slot}", func(w http.ResponseWriter, r *http.Request) {
		if path, ok := productSlot(w, r); ok && isJSON(w, r) {
			g.withSession(w, r, http.MethodDelete, path, nil)
		}
	})
	mux.HandleFunc("POST /admin/api/photos/products/{id}/{slot}/first", func(w http.ResponseWriter, r *http.Request) {
		if path, ok := productSlot(w, r); ok && isJSON(w, r) {
			g.withSession(w, r, http.MethodPost, path+"/first", nil)
		}
	})
	mux.HandleFunc("GET /admin/api/photos/categories", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, http.MethodGet, "/photos/categories", nil)
	})
	mux.HandleFunc("PUT /admin/api/photos/categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if photo, contentType, ok := readPhoto(w, r); ok {
			g.withSessionTyped(w, r, http.MethodPut, "/photos/categories/"+id, photo, contentType)
		}
	})
	mux.HandleFunc("DELETE /admin/api/photos/categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := pathID(w, r); ok && isJSON(w, r) {
			g.withSession(w, r, http.MethodDelete, "/photos/categories/"+id, nil)
		}
	})
}

func readPhoto(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !photoTypes[mediaType] {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be image/jpeg, image/png or image/webp")
		return nil, "", false
	}
	photo, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPhotoBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "a photo can be at most 5 MB")
		return nil, "", false
	case err != nil:
		writeError(w, http.StatusBadRequest, "could not read the photo")
		return nil, "", false
	}
	return photo, mediaType, true
}
