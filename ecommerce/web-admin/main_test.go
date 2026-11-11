package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

type seen struct {
	mu       sync.Mutex
	requests []string
}

func (s *seen) add(r *http.Request, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+body)
}

func newTestServer(t *testing.T) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.add(r, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/login" && strings.Contains(string(body), "admin@example.com"):
			io.WriteString(w, `{"token":"tok-123","expires_in":3600,"user":{"id":1,"name":"Admin","role":"admin"}}`)
		case r.URL.Path == "/login":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":"this account is not an admin"}`)
		case r.Header.Get("Authorization") != "Bearer tok-123":
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"the token is not valid"}`)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(admin.Close)
	server := httptest.NewServer(newHandler(admin.URL))
	t.Cleanup(server.Close)
	return server, s
}

func request(t *testing.T, server *httptest.Server, method, path, body, cookie string, json bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if json {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestLoginSetsAnAdminOnlyCookie(t *testing.T) {
	server, _ := newTestServer(t)
	resp := request(t, server, http.MethodPost, "/admin/api/login", `{"email":"admin@example.com","password":"x"}`, "", true)
	body, _ := io.ReadAll(resp.Body)
	cookie := resp.Header.Get("Set-Cookie")
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "tok-123") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	for _, part := range []string{"admin_session=tok-123", "Path=/admin/api", "HttpOnly", "Secure", "SameSite=Strict", "Max-Age=3600"} {
		if !strings.Contains(cookie, part) {
			t.Fatalf("cookie %q lacks %s", cookie, part)
		}
	}
	resp = request(t, server, http.MethodPost, "/admin/api/login", `{"email":"buyer@example.com","password":"x"}`, "", true)
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("customer: %d %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	if resp := request(t, server, http.MethodPost, "/admin/api/login", `email=a`, "", false); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", resp.StatusCode)
	}
}

func TestRequestsCarryTheCookieAsBearer(t *testing.T) {
	server, s := newTestServer(t)
	cookie := "admin_session=tok-123; session=shop-token"
	for _, c := range []struct{ method, path, body, want string }{
		{http.MethodGet, "/admin/api/me", "", "GET /me Bearer tok-123 "},
		{http.MethodGet, "/admin/api/orders?status=paid&limit=20&offset=0&evil=1", "", "GET /orders?limit=20&offset=0&status=paid Bearer tok-123 "},
		{http.MethodGet, "/admin/api/orders/9", "", "GET /orders/9 Bearer tok-123 "},
		{http.MethodPost, "/admin/api/orders/9/status", `{"status":"packed"}`, `POST /orders/9/status Bearer tok-123 {"status":"packed"}`},
	} {
		resp := request(t, server, c.method, c.path, c.body, cookie, true)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
		if got := s.requests[len(s.requests)-1]; got != c.want {
			t.Fatalf("sent %q, want %q", got, c.want)
		}
	}
}

func TestNoSessionOrAStaleOneIsRefused(t *testing.T) {
	server, s := newTestServer(t)
	if resp := request(t, server, http.MethodGet, "/admin/api/orders", "", "session=shop-token", false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("shop cookie only: %d", resp.StatusCode)
	}
	if len(s.requests) != 0 {
		t.Fatalf("admin-service was called: %v", s.requests)
	}
	resp := request(t, server, http.MethodGet, "/admin/api/me", "", "admin_session=old", false)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("stale: %d %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
}

func TestOnlyKnownPathsPass(t *testing.T) {
	server, s := newTestServer(t)
	cookie := "admin_session=tok-123"
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/admin/api/orders/abc"},
		{http.MethodGet, "/admin/api/orders/0"},
		{http.MethodPost, "/admin/api/orders/9/cancel"},
		{http.MethodDelete, "/admin/api/users/3"},
		{http.MethodPut, "/admin/api/users/abc/role"},
		{http.MethodDelete, "/admin/api/orders/9"},
		{http.MethodGet, "/api/orders"},
		{http.MethodGet, "/internal/orders"},
	} {
		if resp := request(t, server, c.method, c.path, `{}`, cookie, true); resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
	if len(s.requests) != 0 {
		t.Fatalf("admin-service was called: %v", s.requests)
	}
	if resp := request(t, server, http.MethodPost, "/admin/api/orders/9/status", `{"status":"packed"}`, cookie, false); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form status change: %d", resp.StatusCode)
	}
}

func TestPageAndAssets(t *testing.T) {
	server, _ := newTestServer(t)
	if resp := request(t, server, http.MethodGet, "/admin", "", "", false); resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/admin/" {
		t.Fatalf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp := request(t, server, http.MethodGet, "/admin/", "", "", false)
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") || resp.Header.Get("X-Robots-Tag") == "" {
		t.Fatalf("page: %d %v", resp.StatusCode, resp.Header)
	}
	links := regexp.MustCompile(`"(/admin/(?:admin\.js|admin\.css|products\.js)\?v=[0-9a-f]{12})"`).FindAllStringSubmatch(string(page), -1)
	if len(links) != 3 {
		t.Fatalf("links %v", links)
	}
	for _, link := range links {
		if resp := request(t, server, http.MethodGet, link[1], "", "", false); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", link[1], resp.StatusCode)
		}
		stale := strings.Split(link[1], "?")[0] + "?v=000000000000"
		if resp := request(t, server, http.MethodGet, stale, "", "", false); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", stale, resp.StatusCode)
		}
	}
	for _, path := range []string{"/admin/index.html", "/admin/handler.go", "/admin/../main.go", "/", "/admin/static/admin.js"} {
		if resp := request(t, server, http.MethodGet, path, "", "", false); resp.StatusCode == http.StatusOK {
			t.Fatalf("%s served", path)
		}
	}
}

func TestCatalogRoutesPass(t *testing.T) {
	server, s := newTestServer(t)
	cookie := "admin_session=tok-123"
	for _, c := range []struct{ method, path, body, want string }{
		{http.MethodGet, "/admin/api/products?limit=100&offset=0&category_id=2&x=1", "", "GET /products?category_id=2&limit=100&offset=0 Bearer tok-123 "},
		{http.MethodPost, "/admin/api/products", `{"name":"Rok","price":1}`, `POST /products Bearer tok-123 {"name":"Rok","price":1}`},
		{http.MethodPut, "/admin/api/products/5", `{"name":"Rok","price":1}`, `PUT /products/5 Bearer tok-123 {"name":"Rok","price":1}`},
		{http.MethodPut, "/admin/api/products/5/sale", `{"price":1}`, `PUT /products/5/sale Bearer tok-123 {"price":1}`},
		{http.MethodDelete, "/admin/api/products/5/sale", "", `DELETE /products/5/sale Bearer tok-123 `},
		{http.MethodDelete, "/admin/api/products/5", "", "DELETE /products/5 Bearer tok-123 "},
		{http.MethodPost, "/admin/api/products/5/variants", `{"size":"M","color":"Hitam"}`, `POST /products/5/variants Bearer tok-123 {"size":"M","color":"Hitam"}`},
		{http.MethodPut, "/admin/api/variants/3", `{"size":"L","color":"Hitam"}`, `PUT /variants/3 Bearer tok-123 {"size":"L","color":"Hitam"}`},
		{http.MethodDelete, "/admin/api/variants/3", "", "DELETE /variants/3 Bearer tok-123 "},
		{http.MethodPatch, "/admin/api/variants/3/stock", `{"delta":-2}`, `PATCH /variants/3/stock Bearer tok-123 {"delta":-2}`},
		{http.MethodGet, "/admin/api/categories", "", "GET /categories Bearer tok-123 "},
		{http.MethodPost, "/admin/api/categories", `{"name":"Kaftan"}`, `POST /categories Bearer tok-123 {"name":"Kaftan"}`},
		{http.MethodPut, "/admin/api/categories/2", `{"name":"Kaftan"}`, `PUT /categories/2 Bearer tok-123 {"name":"Kaftan"}`},
		{http.MethodDelete, "/admin/api/categories/2", "", "DELETE /categories/2 Bearer tok-123 "},
	} {
		if resp := request(t, server, c.method, c.path, c.body, cookie, true); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
		if got := s.requests[len(s.requests)-1]; got != c.want {
			t.Fatalf("sent %q, want %q", got, c.want)
		}
	}
	before := len(s.requests)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodDelete, "/admin/api/products/5", ""},
		{http.MethodPatch, "/admin/api/variants/3/stock", `{"delta":1}`},
	} {
		if resp := request(t, server, c.method, c.path, c.body, cookie, false); resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("%s %s without JSON: %d", c.method, c.path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/admin/api/products/abc", "/admin/api/variants/0/stock", "/admin/api/products/5/photos"} {
		if resp := request(t, server, http.MethodPatch, path, `{}`, cookie, true); resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	if len(s.requests) != before {
		t.Fatalf("admin-service was called: %v", s.requests[before:])
	}
}

func TestUserRoutesPass(t *testing.T) {
	server, s := newTestServer(t)
	cookie := "admin_session=tok-123"
	for _, c := range []struct{ method, path, body, want string }{
		{http.MethodGet, "/admin/api/users?q=sari%40x&role=admin&limit=50&offset=0&id=1", "", "GET /users?limit=50&offset=0&q=sari%40x&role=admin Bearer tok-123 "},
		{http.MethodPut, "/admin/api/users/3/role", `{"role":"admin"}`, `PUT /users/3/role Bearer tok-123 {"role":"admin"}`},
	} {
		if resp := request(t, server, c.method, c.path, c.body, cookie, true); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
		if got := s.requests[len(s.requests)-1]; got != c.want {
			t.Fatalf("sent %q, want %q", got, c.want)
		}
	}
	before := len(s.requests)
	if resp := request(t, server, http.MethodPut, "/admin/api/users/3/role", `{"role":"admin"}`, cookie, false); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("role change as a form: %d", resp.StatusCode)
	}
	if len(s.requests) != before {
		t.Fatal("admin-service was called")
	}
}

func TestVoucherRoutesPass(t *testing.T) {
	server, s := newTestServer(t)
	cookie := "admin_session=tok-123"
	for _, c := range []struct{ method, path, body, want string }{
		{http.MethodGet, "/admin/api/vouchers?x=1", "", "GET /vouchers Bearer tok-123 "},
		{http.MethodPost, "/admin/api/vouchers", `{"code":"HEMAT10"}`, `POST /vouchers Bearer tok-123 {"code":"HEMAT10"}`},
		{http.MethodPut, "/admin/api/vouchers/4", `{"code":"HEMAT20"}`, `PUT /vouchers/4 Bearer tok-123 {"code":"HEMAT20"}`},
		{http.MethodDelete, "/admin/api/vouchers/4", "", "DELETE /vouchers/4 Bearer tok-123 "},
		{http.MethodGet, "/admin/api/looks", "", "GET /looks Bearer tok-123 "},
		{http.MethodPost, "/admin/api/looks", `{"title":"A"}`, `POST /looks Bearer tok-123 {"title":"A"}`},
		{http.MethodPut, "/admin/api/looks/2", `{"title":"B"}`, `PUT /looks/2 Bearer tok-123 {"title":"B"}`},
		{http.MethodDelete, "/admin/api/looks/2", "", "DELETE /looks/2 Bearer tok-123 "},
	} {
		if resp := request(t, server, c.method, c.path, c.body, cookie, true); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
		if got := s.requests[len(s.requests)-1]; got != c.want {
			t.Fatalf("sent %q, want %q", got, c.want)
		}
	}
	before := len(s.requests)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/admin/api/vouchers", `{"code":"X"}`},
		{http.MethodDelete, "/admin/api/vouchers/4", ""},
	} {
		if resp := request(t, server, c.method, c.path, c.body, cookie, false); resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("%s %s as a form: %d", c.method, c.path, resp.StatusCode)
		}
	}
	if resp := request(t, server, http.MethodPut, "/admin/api/vouchers/abc", `{}`, cookie, true); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad id: %d", resp.StatusCode)
	}
	if len(s.requests) != before {
		t.Fatalf("admin-service was called: %v", s.requests[before:])
	}
}
