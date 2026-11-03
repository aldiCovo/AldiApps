package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestUsersAndRoles(t *testing.T) {
	server, f := newTestServer(t)
	status, body := do(t, server, http.MethodGet, "/users?q=c%40ex&role=customer&limit=50&offset=0&x=1", "admin-token", "")
	if status != http.StatusOK || !strings.Contains(body, `"total":1`) {
		t.Fatalf("list: %d %s", status, body)
	}
	if got := f.userHits[len(f.userHits)-1]; got != "GET /internal/users?limit=50&offset=0&q=c%40ex&role=customer " {
		t.Fatalf("sent %q", got)
	}
	status, body = do(t, server, http.MethodPut, "/users/3/role", "admin-token", `{"role":"admin"}`)
	if status != http.StatusOK || !strings.Contains(body, `"role":"admin"`) {
		t.Fatalf("promote: %d %s", status, body)
	}
	if got := f.userHits[len(f.userHits)-1]; got != `PUT /internal/users/3/role {"role":"admin"}` {
		t.Fatalf("sent %q", got)
	}

	before := len(f.userHits)
	for _, c := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/users", "", "", http.StatusUnauthorized},
		{"GET", "/users", "customer-token", "", http.StatusForbidden},
		{"PUT", "/users/3/role", "customer-token", `{"role":"admin"}`, http.StatusForbidden},
		// The fake /me says the admin is user 1.
		{"PUT", "/users/1/role", "admin-token", `{"role":"customer"}`, http.StatusConflict},
		{"PUT", "/users/abc/role", "admin-token", `{"role":"admin"}`, http.StatusNotFound},
		{"PUT", "/users/3/role", "admin-token", `{"role":"admin","extra":1}`, http.StatusBadRequest},
	} {
		if status, body := do(t, server, c.method, c.path, c.token, c.body); status != c.status {
			t.Errorf("%s %s as %q: %d %s, want %d", c.method, c.path, c.token, status, body, c.status)
		}
	}
	if len(f.userHits) != before {
		t.Fatalf("user-service was asked: %v", f.userHits[before:])
	}
	if status, body := do(t, server, "PUT", "/users/4/role", "admin-token", `{"role":"customer"}`); status != http.StatusConflict || !strings.Contains(body, "last admin") {
		t.Errorf("last admin: %d %s", status, body)
	}
	if status, _ := do(t, server, "PUT", "/users/9/role", "admin-token", `{"role":"admin"}`); status != http.StatusNotFound {
		t.Errorf("missing user: %d", status)
	}
}

func TestARefusedAdminTokenIsNotALogout(t *testing.T) {
	server, f := newTestServer(t)
	f.userRefuses = true
	if status, _ := do(t, server, "GET", "/users", "admin-token", ""); status != http.StatusBadGateway {
		t.Fatalf("wrong admin token: %d, want 502", status)
	}
}
