package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeServices records every request the gateway passes on.
type fakeServices struct {
	mu       sync.Mutex
	requests []string
	headers  []http.Header
}

func (f *fakeServices) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeServices) start(t *testing.T, name string, respond http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, name+" "+r.Method+" "+r.URL.RequestURI())
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

const testToken = "signed.jwt.token"

func newTestGateway(t *testing.T) (*httptest.Server, *fakeServices) {
	t.Helper()
	f := &fakeServices{}
	ok := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }
	users := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "benar") {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"wrong email or password"}`)
				return
			}
			io.WriteString(w, `{"token":"`+testToken+`","token_type":"Bearer","expires_in":3600,"user":{"id":1,"name":"Sari"}}`)
		case "/me", "/me/address":
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"the token is not valid"}`)
				return
			}
			io.WriteString(w, `{"id":1,"name":"Sari"}`)
		default:
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":1}`)
		}
	}
	orders := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/shipping" {
			io.WriteString(w, `{"provinces":[],"options":[]}`)
			return
		}
		if r.URL.Path == "/vouchers/banner" {
			io.WriteString(w, `{"voucher":null}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"the token is not valid"}`)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}
	server := httptest.NewServer(newHandler(f.start(t, "catalog", ok), f.start(t, "search", ok), f.start(t, "user", users), f.start(t, "order", orders), f.start(t, "realtime", ok), f.start(t, "cart", orders), t.TempDir()))
	t.Cleanup(server.Close)
	return server, f
}

func do(t *testing.T, method, url, contentType, body string, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(data)
}

func TestAllowedRequestsReachTheRightService(t *testing.T) {
	server, f := newTestGateway(t)
	cases := []struct{ method, path, body, want string }{
		{"GET", "/api/categories", "", "catalog GET /categories"},
		{"GET", "/api/products/12", "", "catalog GET /products/12"},
		{"GET", "/api/search?q=dress+merah&in_stock=true", "", "search GET /search?q=dress+merah&in_stock=true"},
		{"POST", "/api/register", `{"email":"a@b.c"}`, "user POST /register"},
		{"POST", "/api/password/forgot", `{"email":"a@b.c"}`, "user POST /password/forgot"},
		{"POST", "/api/password/reset", `{"token":"t","password":"p"}`, "user POST /password/reset"},
		{"GET", "/api/shipping?x=1", "", "order GET /shipping"},
		{"GET", "/api/vouchers/banner", "", "order GET /vouchers/banner"},
	}
	for _, c := range cases {
		resp, body := do(t, c.method, server.URL+c.path, "application/json", c.body)
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: status %d, body %s", c.method, c.path, resp.StatusCode, body)
		}
		seen := f.seen()
		if got := seen[len(seen)-1]; got != c.want {
			t.Fatalf("%s %s reached %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestWriteAndUnlistedRequestsNeverReachAService(t *testing.T) {
	server, f := newTestGateway(t)
	cases := []struct{ method, path string }{
		{"POST", "/api/products"},
		{"GET", "/api/products"},
		{"PUT", "/api/products/1"},
		{"DELETE", "/api/products/1"},
		{"POST", "/api/products/1/variants"},
		{"PATCH", "/api/variants/1/stock"},
		{"POST", "/api/categories"},
		{"DELETE", "/api/categories/1"},
		{"GET", "/api/products/1%2Fvariants"},
		{"GET", "/api/products/..%2Fcategories"},
		{"GET", "/api/products/abc"},
		{"GET", "/api/products/1?x=../../categories"},
		{"GET", "/api/.well-known/jwks.json"},
		{"GET", "/api/healthz"},
		{"GET", "/api/search/../../secret"},
	}
	for _, c := range cases {
		before := len(f.seen())
		resp, _ := do(t, c.method, server.URL+c.path, "application/json", "{}")
		if c.path == "/api/products/1?x=../../categories" {
			// The query is dropped, so this reads product 1 and nothing else.
			seen := f.seen()
			if got := seen[len(seen)-1]; got != "catalog GET /products/1" {
				t.Fatalf("got %q", got)
			}
			continue
		}
		if after := len(f.seen()); after != before {
			t.Fatalf("%s %s reached a service: %v", c.method, c.path, f.seen()[before:])
		}
		if resp.StatusCode < 300 {
			t.Fatalf("%s %s: status %d", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestSearchCannotChangeTheUpstreamPath(t *testing.T) {
	server, f := newTestGateway(t)
	do(t, "GET", server.URL+"/api/search?q=a%26limit%3D1%23frag", "", "")
	seen := f.seen()
	if got := seen[len(seen)-1]; got != "search GET /search?q=a%26limit%3D1%23frag" {
		t.Fatalf("got %q", got)
	}
}

func TestBrowserHeadersStayOutOfServices(t *testing.T) {
	server, f := newTestGateway(t)
	req, err := http.NewRequest("GET", server.URL+"/api/categories", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer from-browser")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: testToken})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got := f.headers[len(f.headers)-1]
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" {
		t.Fatalf("headers passed on: %v", got)
	}
}

func TestLoginKeepsTheTokenInAProtectedCookie(t *testing.T) {
	server, _ := newTestGateway(t)
	resp, body := do(t, "POST", server.URL+"/api/login", "application/json", `{"email":"a@b.c","password":"benar"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body %s", resp.StatusCode, body)
	}
	if strings.Contains(body, testToken) || strings.Contains(body, "token") {
		t.Fatalf("response exposes the token: %s", body)
	}
	var user map[string]any
	if err := json.Unmarshal([]byte(body), &user); err != nil || user["name"] != "Sari" {
		t.Fatalf("got %s", body)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %v", cookies)
	}
	c := cookies[0]
	if c.Name != sessionCookie || c.Value != testToken || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/api" || c.MaxAge != 3600 {
		t.Fatalf("cookie %+v", c)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", resp.Header.Get("Cache-Control"))
	}

	resp, body = do(t, "GET", server.URL+"/api/me", "", "", c)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Sari") {
		t.Fatalf("me: status %d, body %s", resp.StatusCode, body)
	}
}

func TestFailedLoginSetsNoCookie(t *testing.T) {
	server, _ := newTestGateway(t)
	resp, _ := do(t, "POST", server.URL+"/api/login", "application/json", `{"email":"a@b.c","password":"salah"}`)
	if resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 {
		t.Fatalf("status %d, cookies %v", resp.StatusCode, resp.Cookies())
	}
}

func TestMeWithoutValidSession(t *testing.T) {
	server, _ := newTestGateway(t)
	if resp, _ := do(t, "GET", server.URL+"/api/me", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: status %d", resp.StatusCode)
	}
	resp, _ := do(t, "GET", server.URL+"/api/me", "", "", &http.Cookie{Name: sessionCookie, Value: "bad"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad cookie: status %d", resp.StatusCode)
	}
	if cookies := resp.Cookies(); len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("bad cookie stays: %v", cookies)
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	server, _ := newTestGateway(t)
	resp, _ := do(t, "POST", server.URL+"/api/logout", "application/json", "{}")
	if cookies := resp.Cookies(); resp.StatusCode != http.StatusNoContent || len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("status %d, cookies %v", resp.StatusCode, cookies)
	}
}

func TestFormsFromOtherSitesAreRefused(t *testing.T) {
	server, f := newTestGateway(t)
	for _, path := range []string{"/api/login", "/api/register", "/api/logout", "/api/password/forgot", "/api/password/reset"} {
		for _, contentType := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
			before := len(f.seen())
			resp, _ := do(t, "POST", server.URL+path, contentType, `{"email":"a@b.c","password":"benar"}`)
			if resp.StatusCode != http.StatusUnsupportedMediaType || len(f.seen()) != before || len(resp.Cookies()) != 0 {
				t.Fatalf("%s with %q: status %d", path, contentType, resp.StatusCode)
			}
		}
	}
}

func TestLargeBodyIsRefused(t *testing.T) {
	server, f := newTestGateway(t)
	resp, _ := do(t, "POST", server.URL+"/api/register", "application/json", strings.Repeat("a", maxBodyBytes+1))
	if resp.StatusCode != http.StatusRequestEntityTooLarge || len(f.seen()) != 0 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCartRequestsCarryTheSession(t *testing.T) {
	server, f := newTestGateway(t)
	session := &http.Cookie{Name: sessionCookie, Value: testToken}
	cases := []struct{ method, path, body, want string }{
		{"GET", "/api/cart", "", "cart GET /cart"},
		{"DELETE", "/api/cart", "", "cart DELETE /cart"},
		{"POST", "/api/cart/merge", `{"items":[]}`, "cart POST /cart/merge"},
		{"PUT", "/api/cart/items/7", `{"quantity":1}`, "cart PUT /cart/items/7"},
		{"DELETE", "/api/cart/items/7", "", "cart DELETE /cart/items/7"},
	}
	for _, c := range cases {
		resp, _ := do(t, c.method, server.URL+c.path, "application/json", c.body, session)
		seen := f.seen()
		if resp.StatusCode != http.StatusOK || seen[len(seen)-1] != c.want || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: status %d, seen %v", c.method, c.path, resp.StatusCode, seen)
		}
		f.mu.Lock()
		got := f.headers[len(f.headers)-1].Get("Authorization")
		f.mu.Unlock()
		if got != "Bearer "+testToken {
			t.Fatalf("%s %s: Authorization %q", c.method, c.path, got)
		}
		before := len(f.seen())
		if resp, _ := do(t, c.method, server.URL+c.path, "application/json", c.body); resp.StatusCode != http.StatusUnauthorized || len(f.seen()) != before {
			t.Fatalf("%s %s without a session: status %d", c.method, c.path, resp.StatusCode)
		}
	}
	for _, c := range []struct {
		method, path, contentType string
		want                      int
	}{
		{"PUT", "/api/cart/items/abc", "application/json", http.StatusNotFound},
		{"DELETE", "/api/cart/items/abc", "", http.StatusNotFound},
		{"PUT", "/api/cart/items/7", "text/plain", http.StatusUnsupportedMediaType},
		{"POST", "/api/cart/merge", "text/plain", http.StatusUnsupportedMediaType},
		{"PUT", "/api/cart", "application/json", http.StatusNotFound},
		{"POST", "/api/cart", "application/json", http.StatusNotFound},
	} {
		if resp, _ := do(t, c.method, server.URL+c.path, c.contentType, "{}", session); resp.StatusCode != c.want {
			t.Fatalf("%s %s: status %d", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestDownServiceAnswers502WithoutDetails(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	server := httptest.NewServer(newHandler(url, url, url, url, url, url, t.TempDir()))
	defer server.Close()
	for _, c := range []struct{ method, path string }{{"GET", "/api/search"}, {"POST", "/api/login"}} {
		resp, body := do(t, c.method, server.URL+c.path, "application/json", "{}")
		if resp.StatusCode != http.StatusBadGateway || strings.Contains(body, "127.0.0.1") {
			t.Fatalf("%s: status %d, body %s", c.path, resp.StatusCode, body)
		}
	}
}

func TestTheFontIsServedFromThisSite(t *testing.T) {
	server, _ := newTestGateway(t)
	resp, body := do(t, "GET", server.URL+"/fonts/cormorant-garamond-latin-500-normal.woff2", "", "")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "wOF2") || resp.Header.Get("Content-Type") != "font/woff2" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "font-src 'self'") {
		t.Fatalf("CSP %q", resp.Header.Get("Content-Security-Policy"))
	}
}

func TestPagesAndHeaders(t *testing.T) {
	server, _ := newTestGateway(t)
	cases := map[string]string{"/": "<title>Prastika</title>", "/app.js": "use strict", "/style.css": "--paper"}
	for path, want := range cases {
		resp, body := do(t, "GET", server.URL+path, "", "")
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: headers %v", path, resp.Header)
		}
	}
	if resp, _ := do(t, "GET", server.URL+"/nope", "", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/nope: status %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", server.URL+"/healthz", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz: status %d", resp.StatusCode)
	}
}

func TestPageLinksFilesByTheirContent(t *testing.T) {
	server, _ := newTestGateway(t)
	_, page := do(t, "GET", server.URL+"/", "", "")
	cases := map[string]string{"/style.css": "--paper", "/app.js": "use strict"}
	for path, want := range cases {
		content, err := static.ReadFile("static" + path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		url := path + "?v=" + hex.EncodeToString(sum[:6])
		if !strings.Contains(page, `"`+url+`"`) {
			t.Fatalf("page does not link %s", url)
		}
		resp, body := do(t, "GET", server.URL+url, "", "")
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("%s: status %d", url, resp.StatusCode)
		}
		// A file from another version must not be kept under this URL.
		resp, _ = do(t, "GET", server.URL+path+"?v=000000000000", "", "")
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s other version: status %d, cache %q", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
	if resp, body := do(t, "HEAD", server.URL+"/", "", ""); resp.StatusCode != http.StatusOK || body != "" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("HEAD /: status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := do(t, "GET", server.URL+"/index.html", "", ""); resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("/index.html: status %d", resp.StatusCode)
	}
}

func TestCategoryPhotos(t *testing.T) {
	photos := t.TempDir()
	dir := filepath.Join(photos, "categories")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dress.jpg", "hijab.webp", "notes.txt", "Upper.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "folder.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(photos, "secret.jpg"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(photos, "secret.jpg"), filepath.Join(dir, "link.jpg")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler("http://127.0.0.1:1", "", "", "", "http://127.0.0.1:1", "", photos))
	t.Cleanup(server.Close)

	resp, body := do(t, "GET", server.URL+"/api/category-photos", "", "")
	var listed map[string]string
	if err := json.Unmarshal([]byte(body), &listed); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body %q, err %v", resp.StatusCode, body, err)
	}
	if len(listed) != 2 || !strings.HasPrefix(listed["dress"], "/photos/categories/dress.jpg?v=") || !strings.HasPrefix(listed["hijab"], "/photos/categories/hijab.webp?v=") {
		t.Fatalf("listed %v", listed)
	}

	resp, body = do(t, "GET", server.URL+listed["dress"], "", "")
	if resp.StatusCode != http.StatusOK || body != "dress.jpg" || resp.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("photo: status %d, body %q, headers %v", resp.StatusCode, body, resp.Header)
	}
	if resp, _ := do(t, "GET", server.URL+"/photos/categories/dress.jpg", "", ""); resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("no version: cache %q", resp.Header.Get("Cache-Control"))
	}
	for _, path := range []string{"/photos/categories/", "/photos/categories/notes.txt", "/photos/categories/Upper.jpg", "/photos/categories/folder.jpg", "/photos/categories/link.jpg", "/photos/categories/..%2fsecret.jpg", "/photos/secret.jpg", "/photos/categories/missing.jpg"} {
		if resp, body := do(t, "GET", server.URL+path, "", ""); resp.StatusCode != http.StatusNotFound || strings.Contains(body, "secret") {
			t.Fatalf("%s: status %d, body %q", path, resp.StatusCode, body)
		}
	}
	empty := httptest.NewServer(newHandler("http://127.0.0.1:1", "", "", "", "http://127.0.0.1:1", "", t.TempDir()))
	t.Cleanup(empty.Close)
	if resp, body := do(t, "GET", empty.URL+"/api/category-photos", "", ""); resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != "{}" {
		t.Fatalf("no folder: status %d, body %q", resp.StatusCode, body)
	}
}

func TestOrdersUseTheSessionCookie(t *testing.T) {
	server, f := newTestGateway(t)
	session := &http.Cookie{Name: sessionCookie, Value: testToken}
	cases := []struct{ method, path, want string }{
		{"POST", "/api/orders", "order POST /orders"},
		{"POST", "/api/vouchers/check", "order POST /vouchers/check"},
		{"GET", "/api/orders?limit=5", "order GET /orders?limit=5"},
		{"GET", "/api/orders/12", "order GET /orders/12"},
		{"POST", "/api/orders/12/pay", "order POST /orders/12/pay"},
		{"POST", "/api/orders/12/cancel", "order POST /orders/12/cancel"},
	}
	for _, c := range cases {
		resp, body := do(t, c.method, server.URL+c.path, "application/json", "{}", session)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d, body %s", c.method, c.path, resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: Cache-Control %q", c.path, resp.Header.Get("Cache-Control"))
		}
		seen := f.seen()
		if got := seen[len(seen)-1]; got != c.want {
			t.Fatalf("%s reached %q", c.path, got)
		}
		if got := f.headers[len(f.headers)-1].Get("Authorization"); got != "Bearer "+testToken {
			t.Fatalf("%s: Authorization %q", c.path, got)
		}
	}
}

func TestOrdersWithoutValidSession(t *testing.T) {
	server, f := newTestGateway(t)
	for _, c := range []struct{ method, path string }{{"POST", "/api/orders"}, {"GET", "/api/orders"}, {"GET", "/api/orders/1"}, {"POST", "/api/orders/1/pay"}, {"POST", "/api/orders/1/cancel"}} {
		before := len(f.seen())
		resp, _ := do(t, c.method, server.URL+c.path, "application/json", "{}")
		if resp.StatusCode != http.StatusUnauthorized || len(f.seen()) != before {
			t.Fatalf("%s %s: status %d", c.method, c.path, resp.StatusCode)
		}
		resp, _ = do(t, c.method, server.URL+c.path, "application/json", "{}", &http.Cookie{Name: sessionCookie, Value: "bad"})
		if cookies := resp.Cookies(); resp.StatusCode != http.StatusUnauthorized || len(cookies) != 1 || cookies[0].MaxAge >= 0 {
			t.Fatalf("%s %s bad cookie: status %d, cookies %v", c.method, c.path, resp.StatusCode, cookies)
		}
	}
}

func TestOrderPathsStayNarrow(t *testing.T) {
	server, f := newTestGateway(t)
	session := &http.Cookie{Name: sessionCookie, Value: testToken}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/orders/abc"},
		{"GET", "/api/orders/1%2Fpay"},
		{"POST", "/api/orders/abc/pay"},
		{"DELETE", "/api/orders/1"},
		{"PUT", "/api/orders/1"},
		{"GET", "/api/orders/1/pay"},
		{"POST", "/api/orders/1/refund"},
		{"POST", "/api/orders/abc/cancel"},
		{"GET", "/api/orders/1/cancel"},
		{"DELETE", "/api/orders/1/cancel"},
		{"GET", "/api/payments"},
	} {
		before := len(f.seen())
		resp, _ := do(t, c.method, server.URL+c.path, "application/json", "{}", session)
		if resp.StatusCode < 400 || len(f.seen()) != before {
			t.Fatalf("%s %s: status %d, reached %v", c.method, c.path, resp.StatusCode, f.seen()[before:])
		}
	}
	for _, path := range []string{"/api/orders", "/api/orders/1/pay", "/api/orders/1/cancel"} {
		before := len(f.seen())
		resp, _ := do(t, "POST", server.URL+path, "text/plain", "{}", session)
		if resp.StatusCode != http.StatusUnsupportedMediaType || len(f.seen()) != before {
			t.Fatalf("%s from a form: status %d", path, resp.StatusCode)
		}
	}
}

func TestProductPhotos(t *testing.T) {
	photos := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(photos, "products", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Product 1: places 1, 3 and 4, a second file for place 1, and files that don't count.
	for _, name := range []string{"1/4.webp", "1/1.jpg", "1/1.png", "1/3.png", "1/5.jpg", "1/0.jpg", "1/notes.txt", "1/A.jpg", "12/2.jpg", "abc/1.jpg"} {
		write(name, name)
	}
	write("secret.jpg", "secret")
	if err := os.Symlink(filepath.Join(photos, "products", "secret.jpg"), filepath.Join(photos, "products", "12", "1.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(photos, "products", "7"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler("http://127.0.0.1:1", "", "", "", "http://127.0.0.1:1", "", photos))
	t.Cleanup(server.Close)

	resp, body := do(t, "GET", server.URL+"/api/product-photos", "", "")
	var listed map[string][]string
	if err := json.Unmarshal([]byte(body), &listed); err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("status %d, body %q, err %v", resp.StatusCode, body, err)
	}
	want := map[string][]string{"1": {"/photos/products/1/1.jpg", "/photos/products/1/3.png", "/photos/products/1/4.webp"}, "12": {"/photos/products/12/2.jpg"}}
	if len(listed) != len(want) {
		t.Fatalf("listed %v", listed)
	}
	for id, urls := range want {
		if len(listed[id]) != len(urls) {
			t.Fatalf("product %s: listed %v", id, listed[id])
		}
		for i, url := range urls {
			if !strings.HasPrefix(listed[id][i], url+"?v=") {
				t.Fatalf("product %s: listed %v", id, listed[id])
			}
		}
	}

	resp, body = do(t, "GET", server.URL+listed["1"][1], "", "")
	if resp.StatusCode != http.StatusOK || body != "1/3.png" || resp.Header.Get("Content-Type") != "image/png" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("photo: status %d, body %q, headers %v", resp.StatusCode, body, resp.Header)
	}
	for _, path := range []string{"/photos/products/1/5.jpg", "/photos/products/1/notes.txt", "/photos/products/abc/1.jpg", "/photos/products/12/1.jpg", "/photos/products/1/..%2f..%2fsecret.jpg", "/photos/products/..%2fproducts/1.jpg", "/photos/products/2/1.jpg", "/photos/products/1/"} {
		if resp, body := do(t, "GET", server.URL+path, "", ""); resp.StatusCode != http.StatusNotFound || strings.Contains(body, "secret") {
			t.Fatalf("%s: status %d, body %q", path, resp.StatusCode, body)
		}
	}
	empty := httptest.NewServer(newHandler("http://127.0.0.1:1", "", "", "", "http://127.0.0.1:1", "", t.TempDir()))
	t.Cleanup(empty.Close)
	if resp, body := do(t, "GET", empty.URL+"/api/product-photos", "", ""); resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != "{}" {
		t.Fatalf("no folder: status %d, body %q", resp.StatusCode, body)
	}
}

func TestAddressRequestsCarryTheSession(t *testing.T) {
	server, f := newTestGateway(t)
	session := &http.Cookie{Name: sessionCookie, Value: testToken}
	for _, c := range []struct{ method, body, want string }{
		{"GET", "", "user GET /me/address"},
		{"PUT", `{"recipient":"Sari"}`, "user PUT /me/address"},
	} {
		resp, _ := do(t, c.method, server.URL+"/api/me/address", "application/json", c.body, session)
		seen := f.seen()
		if resp.StatusCode != http.StatusOK || seen[len(seen)-1] != c.want || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status %d, seen %v", c.method, resp.StatusCode, seen)
		}
		f.mu.Lock()
		got := f.headers[len(f.headers)-1].Get("Authorization")
		f.mu.Unlock()
		if got != "Bearer "+testToken {
			t.Fatalf("%s: Authorization %q", c.method, got)
		}
		before := len(f.seen())
		if resp, _ := do(t, c.method, server.URL+"/api/me/address", "application/json", c.body); resp.StatusCode != http.StatusUnauthorized || len(f.seen()) != before {
			t.Fatalf("%s without a session: status %d", c.method, resp.StatusCode)
		}
	}
	for _, c := range []struct {
		method, contentType string
		want                int
	}{
		{"PUT", "text/plain", http.StatusUnsupportedMediaType},
		{"POST", "application/json", http.StatusNotFound},
		{"DELETE", "application/json", http.StatusNotFound},
	} {
		before := len(f.seen())
		if resp, _ := do(t, c.method, server.URL+"/api/me/address", c.contentType, "{}", session); resp.StatusCode != c.want || len(f.seen()) != before {
			t.Fatalf("%s %s: status %d", c.method, c.contentType, resp.StatusCode)
		}
	}
}
