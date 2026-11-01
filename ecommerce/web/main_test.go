package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
		case "/me":
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
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"the token is not valid"}`)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}
	server := httptest.NewServer(newHandler(f.start(t, "catalog", ok), f.start(t, "search", ok), f.start(t, "user", users), f.start(t, "order", orders), f.start(t, "realtime", ok)))
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
	for _, path := range []string{"/api/login", "/api/register", "/api/logout"} {
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

func TestDownServiceAnswers502WithoutDetails(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	server := httptest.NewServer(newHandler(url, url, url, url, url))
	defer server.Close()
	for _, c := range []struct{ method, path string }{{"GET", "/api/search"}, {"POST", "/api/login"}} {
		resp, body := do(t, c.method, server.URL+c.path, "application/json", "{}")
		if resp.StatusCode != http.StatusBadGateway || strings.Contains(body, "127.0.0.1") {
			t.Fatalf("%s: status %d, body %s", c.path, resp.StatusCode, body)
		}
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

func TestOrdersUseTheSessionCookie(t *testing.T) {
	server, f := newTestGateway(t)
	session := &http.Cookie{Name: sessionCookie, Value: testToken}
	cases := []struct{ method, path, want string }{
		{"POST", "/api/orders", "order POST /orders"},
		{"GET", "/api/orders?limit=5", "order GET /orders?limit=5"},
		{"GET", "/api/orders/12", "order GET /orders/12"},
		{"POST", "/api/orders/12/pay", "order POST /orders/12/pay"},
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
	for _, c := range []struct{ method, path string }{{"POST", "/api/orders"}, {"GET", "/api/orders"}, {"GET", "/api/orders/1"}, {"POST", "/api/orders/1/pay"}} {
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
		{"GET", "/api/payments"},
	} {
		before := len(f.seen())
		resp, _ := do(t, c.method, server.URL+c.path, "application/json", "{}", session)
		if resp.StatusCode < 400 || len(f.seen()) != before {
			t.Fatalf("%s %s: status %d, reached %v", c.method, c.path, resp.StatusCode, f.seen()[before:])
		}
	}
	for _, path := range []string{"/api/orders", "/api/orders/1/pay"} {
		before := len(f.seen())
		resp, _ := do(t, "POST", server.URL+path, "text/plain", "{}", session)
		if resp.StatusCode != http.StatusUnsupportedMediaType || len(f.seen()) != before {
			t.Fatalf("%s from a form: status %d", path, resp.StatusCode)
		}
	}
}
