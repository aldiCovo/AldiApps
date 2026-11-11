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
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

var (
	numericID = regexp.MustCompile(`^[0-9]{1,18}$`)
	photoFile = regexp.MustCompile(`^[a-z0-9-]{1,64}\.(jpg|jpeg|png|webp)$`)
	// A product has up to productPhotoSlots photos, named by their place: 1.jpg is shown first.
	productPhotoFile = regexp.MustCompile(`^([1-4])\.(jpg|jpeg|png|webp)$`)
)

const productPhotoSlots = 4

type gateway struct {
	catalogURL, searchURL, userURL, orderURL, cartURL string
	client                                            *http.Client
	events                                            http.Handler
}

func newHandler(catalogURL, searchURL, userURL, orderURL, realtimeURL, cartURL, photosDir string) http.Handler {
	g := &gateway{
		catalogURL: catalogURL,
		searchURL:  searchURL,
		userURL:    userURL,
		orderURL:   orderURL,
		cartURL:    cartURL,
		client: &http.Client{
			Timeout: upstreamTimeout,
			// Redirects from a service stay inside the cluster, so they must not reach the browser.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		events: newEventsProxy(realtimeURL),
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
	mux.HandleFunc("POST /api/password/forgot", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.pass(w, r, g.userURL+"/password/forgot", body, "")
		}
	})
	mux.HandleFunc("POST /api/password/reset", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.pass(w, r, g.userURL+"/password/reset", body, "")
		}
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		setSession(w, "", -1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/me", g.me)
	mux.HandleFunc("GET /api/me/address", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, g.userURL+"/me/address", nil)
	})
	mux.HandleFunc("PUT /api/me/address", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.userURL+"/me/address", body)
		}
	})
	// Prices for each courier by province. The same for everyone, so no session is needed.
	mux.HandleFunc("GET /api/shipping", func(w http.ResponseWriter, r *http.Request) {
		g.pass(w, r, g.orderURL+"/shipping", nil, "")
	})
	// The voucher shown above the header, the same for everyone.
	mux.HandleFunc("GET /api/vouchers/banner", func(w http.ResponseWriter, r *http.Request) {
		g.pass(w, r, g.orderURL+"/vouchers/banner", nil, "")
	})
	mux.HandleFunc("POST /api/orders", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.orderURL+"/orders", body)
		}
	})
	// What a voucher code would take off the checkout.
	mux.HandleFunc("POST /api/vouchers/check", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.orderURL+"/vouchers/check", body)
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
	mux.HandleFunc("POST /api/orders/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		id, ok := orderPath(w, r)
		if !ok {
			return
		}
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.orderURL+"/orders/"+id+"/cancel", body)
		}
	})
	mux.HandleFunc("GET /api/cart", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, g.cartURL+"/cart", nil)
	})
	mux.HandleFunc("DELETE /api/cart", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, g.cartURL+"/cart", nil)
	})
	mux.HandleFunc("POST /api/cart/merge", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.cartURL+"/cart/merge", body)
		}
	})
	mux.HandleFunc("PUT /api/cart/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.cartURL+"/cart/items/"+id, body)
		}
	})
	mux.HandleFunc("DELETE /api/cart/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		g.withSession(w, r, g.cartURL+"/cart/items/"+id, nil)
	})
	mux.HandleFunc("GET /api/favorites", func(w http.ResponseWriter, r *http.Request) {
		g.withSession(w, r, g.cartURL+"/favorites", nil)
	})
	mux.HandleFunc("POST /api/favorites/merge", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.cartURL+"/favorites/merge", body)
		}
	})
	mux.HandleFunc("PUT /api/favorites/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "product not found")
			return
		}
		if body, ok := readBody(w, r); ok {
			g.withSession(w, r, g.cartURL+"/favorites/"+id, body)
		}
	})
	mux.HandleFunc("DELETE /api/favorites/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "product not found")
			return
		}
		g.withSession(w, r, g.cartURL+"/favorites/"+id, nil)
	})
	mux.HandleFunc("GET /api/events", g.openEvents)
	categoryPhotosDir := filepath.Join(photosDir, "categories")
	mux.HandleFunc("GET /api/category-photos", func(w http.ResponseWriter, r *http.Request) {
		listCategoryPhotos(w, categoryPhotosDir)
	})
	mux.HandleFunc("GET /photos/categories/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !photoFile.MatchString(r.PathValue("name")) {
			http.NotFound(w, r)
			return
		}
		servePhoto(w, r, filepath.Join(categoryPhotosDir, r.PathValue("name")))
	})
	productPhotosDir := filepath.Join(photosDir, "products")
	mux.HandleFunc("GET /api/product-photos", func(w http.ResponseWriter, r *http.Request) {
		listProductPhotos(w, productPhotosDir)
	})
	mux.HandleFunc("GET /photos/products/{id}/{name}", func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		if !numericID.MatchString(id) || !productPhotoFile.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		servePhoto(w, r, filepath.Join(productPhotosDir, id, name))
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServerFS(files)
	page, assets := versionedPage(files)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/" {
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(page))
			return
		}
		// During a rollout, another version's page can reach this Pod. Answering
		// with this version's file would let the browser keep it under that URL.
		if version := r.URL.Query().Get("v"); version != "" && version != assets[r.URL.Path] {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	return securityHeaders(mux)
}

// listCategoryPhotos answers with each photo's URL by its name without the extension,
// such as "dress". The folder lives on the server's disk, so a photo can change without
// a new image, and its URL changes with it because Cloudflare keeps images for hours.
// A missing folder means no photos, and the page then keeps its drawings.
func listCategoryPhotos(w http.ResponseWriter, dir string) {
	photos := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("reading category photos failed", "error", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !photoFile.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		photos[name] = "/photos/categories/" + entry.Name() + "?v=" + photoVersion(info)
	}
	writePhotoList(w, photos)
}

// listProductPhotos answers with each product's photos in their order, by product id, from
// a folder per product such as products/12/1.jpg to 4.jpg. A missing place is skipped, so
// 1.jpg and 3.jpg give two photos. Products without a folder keep their category's photo.
func listProductPhotos(w http.ResponseWriter, dir string) {
	photos := map[string][]string{}
	products, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("reading product photos failed", "error", err)
	}
	for _, product := range products {
		if !product.IsDir() || !numericID.MatchString(product.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(dir, product.Name()))
		if err != nil {
			slog.Error("reading product photos failed", "product", product.Name(), "error", err)
			continue
		}
		var slots [productPhotoSlots]string
		for _, entry := range entries {
			match := productPhotoFile.FindStringSubmatch(entry.Name())
			if match == nil || !entry.Type().IsRegular() {
				continue
			}
			slot := int(match[1][0] - '1')
			// Two files for one place, such as 1.jpg and 1.png: the first by name wins.
			if slots[slot] != "" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			slots[slot] = "/photos/products/" + product.Name() + "/" + entry.Name() + "?v=" + photoVersion(info)
		}
		var urls []string
		for _, url := range slots {
			if url != "" {
				urls = append(urls, url)
			}
		}
		if len(urls) > 0 {
			photos[product.Name()] = urls
		}
	}
	writePhotoList(w, photos)
}

// photoVersion changes whenever a photo is replaced, because Cloudflare keeps images for hours.
func photoVersion(info fs.FileInfo) string {
	return strconv.FormatInt(info.ModTime().Unix(), 36) + "-" + strconv.FormatInt(info.Size(), 36)
}

func writePhotoList(w http.ResponseWriter, photos any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(photos); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}

// servePhoto sends a photo whose name was already checked. Links are refused, so nothing
// outside the photos folder can be reached through one.
func servePhoto(w http.ResponseWriter, r *http.Request, path string) {
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.URL.Query().Get("v") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFile(w, r, path)
}

// versionedPage returns index.html with a hash of each file's content in its URL,
// and the hash of each path. Cloudflare tells browsers to keep CSS and JavaScript
// for hours whatever this server says, while the page itself is never kept, so
// without this a new page runs on the old files.
func versionedPage(files fs.FS) ([]byte, map[string]string) {
	page, err := fs.ReadFile(files, "index.html")
	if err != nil {
		panic(err)
	}
	assets := make(map[string]string)
	for _, name := range []string{"style.css", "app.js"} {
		content, err := fs.ReadFile(files, name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(content)
		version := hex.EncodeToString(sum[:6])
		path := "/" + name
		quoted := `"` + path + `"`
		if !bytes.Contains(page, []byte(quoted)) {
			panic("index.html does not link " + path)
		}
		page = []byte(strings.ReplaceAll(string(page), quoted, `"`+path+"?v="+version+`"`))
		assets[path] = version
	}
	return page, assets
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
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
	if r.Method == http.MethodPut || r.Method == http.MethodDelete {
		method = r.Method
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

// passedEventHeaders are all that realtime-service needs from the browser's handshake.
var passedEventHeaders = map[string]bool{
	"Connection":               true,
	"Upgrade":                  true,
	"Sec-Websocket-Key":        true,
	"Sec-Websocket-Version":    true,
	"Sec-Websocket-Extensions": true,
	"Sec-Websocket-Protocol":   true,
}

func newEventsProxy(realtimeURL string) http.Handler {
	target, err := url.Parse(realtimeURL)
	if err != nil {
		panic(err)
	}
	return &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.URL.Path, p.Out.URL.RawPath, p.Out.URL.RawQuery = "/ws", "", ""
			for name := range p.Out.Header {
				if !passedEventHeaders[name] {
					p.Out.Header.Del(name)
				}
			}
			// openEvents has already checked that the cookie exists.
			if cookie, err := p.In.Cookie(sessionCookie); err == nil {
				p.Out.Header.Set("Authorization", "Bearer "+cookie.Value)
			}
		},
		Transport: &http.Transport{ResponseHeaderTimeout: upstreamTimeout},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeUpstreamError(w, err)
		},
	}
}

// openEvents checks Origin because a WebSocket handshake carries cookies but, unlike a JSON
// request, doesn't ask permission first. Another site could otherwise listen as the visitor.
func (g *gateway) openEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host != r.Host {
		writeError(w, http.StatusForbidden, "this origin may not connect")
		return
	}
	if _, err := r.Cookie(sessionCookie); err != nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	g.events.ServeHTTP(w, r)
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
