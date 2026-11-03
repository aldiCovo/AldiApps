package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const testAdminToken = "admin-token-for-the-tests-only-0123456789"

func (s *testServer) makeAdmin(t *testing.T, id int64) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), "UPDATE users SET role = 'admin' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
}

func (s *testServer) list(t *testing.T, query string) UserPage {
	t.Helper()
	status, body := request(t, http.MethodGet, s.URL+"/internal/users?"+query, "", testAdminToken)
	if status != http.StatusOK {
		t.Fatalf("list %q: %d %s", query, status, body)
	}
	var page UserPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func emails(page UserPage) string {
	var out []string
	for _, user := range page.Users {
		out = append(out, user.Email)
	}
	return strings.Join(out, ",")
}

func TestAdminRoutesNeedTheAdminToken(t *testing.T) {
	server := newTestServer(t, nil)
	sari := server.register(t, "sari@example.com", "rahasia-sekali")
	userToken, _ := server.tokens.Issue(sari)
	for _, bearer := range []string{"", "wrong", testInternalToken, userToken} {
		if status, _ := request(t, http.MethodGet, server.URL+"/internal/users", "", bearer); status != http.StatusUnauthorized {
			t.Errorf("list with %q: %d", bearer, status)
		}
		if status, _ := request(t, http.MethodPut, server.URL+fmt.Sprintf("/internal/users/%d/role", sari.ID), `{"role":"admin"}`, bearer); status != http.StatusUnauthorized {
			t.Errorf("role with %q: %d", bearer, status)
		}
	}
	if got, _ := server.pool.Exec(context.Background(), "SELECT 1 FROM users WHERE role = 'admin'"); got.RowsAffected() != 0 {
		t.Fatal("someone became an admin without the admin token")
	}
	// The admin token doesn't open the lookup other services use.
	if status, _ := request(t, http.MethodGet, server.URL+fmt.Sprintf("/internal/users/%d", sari.ID), "", testAdminToken); status != http.StatusUnauthorized {
		t.Errorf("lookup with the admin token: %d", status)
	}
}

func TestListFindsAndPages(t *testing.T) {
	server := newTestServer(t, nil)
	for _, email := range []string{"sari@example.com", "budi@example.com", "rina_100%@example.com", "dewi@toko.id"} {
		server.register(t, email, "rahasia-sekali")
	}
	server.makeAdmin(t, 2)

	page := server.list(t, "")
	if emails(page) != "dewi@toko.id,rina_100%@example.com,budi@example.com,sari@example.com" || page.Total != 4 {
		t.Fatalf("newest first: %s total %d", emails(page), page.Total)
	}
	if strings.Contains(fmt.Sprint(page), "pbkdf2") {
		t.Fatal("a password hash was listed")
	}
	for query, want := range map[string]string{
		"q=" + url.QueryEscape("TOKO"):  "dewi@toko.id",
		"q=" + url.QueryEscape("sari"):  "sari@example.com,budi@example.com,rina_100%@example.com,dewi@toko.id", // every name is Sari
		"q=" + url.QueryEscape("_100%"): "rina_100%@example.com",
		"q=" + url.QueryEscape("%"):     "rina_100%@example.com",
		"q=" + url.QueryEscape("a_i"):   "",
		"role=admin":                    "budi@example.com",
		"role=customer&q=example":       "rina_100%@example.com,sari@example.com",
		"limit=2&offset=1":              "rina_100%@example.com,budi@example.com",
	} {
		got := server.list(t, query)
		if query == "q=sari" {
			if got.Total != 4 {
				t.Errorf("%s: total %d", query, got.Total)
			}
			continue
		}
		if emails(got) != want {
			t.Errorf("%s: got %q, want %q", query, emails(got), want)
		}
	}
	if got := server.list(t, "limit=2&offset=10"); len(got.Users) != 0 || got.Total != 4 {
		t.Errorf("past the end: %+v", got)
	}
	for _, query := range []string{"role=owner", "limit=0", "limit=101", "offset=-1", "limit=x", "q=" + strings.Repeat("a", 201)} {
		if status, _ := request(t, http.MethodGet, server.URL+"/internal/users?"+query, "", testAdminToken); status != http.StatusBadRequest {
			t.Errorf("%s: %d", query, status)
		}
	}
}

func TestRoleChanges(t *testing.T) {
	server := newTestServer(t, nil)
	sari := server.register(t, "sari@example.com", "rahasia-sekali")
	budi := server.register(t, "budi@example.com", "rahasia-sekali")
	role := func(id int64, body string) (int, string) {
		status, out := request(t, http.MethodPut, server.URL+fmt.Sprintf("/internal/users/%d/role", id), body, testAdminToken)
		return status, string(out)
	}
	if status, body := role(sari.ID, `{"role":"admin"}`); status != http.StatusOK || !strings.Contains(body, `"role":"admin"`) {
		t.Fatalf("promote: %d %s", status, body)
	}
	// A login token carries the role, and /me answers the new one at once.
	login := server.login(t, "sari@example.com", "rahasia-sekali")
	if login.User.Role != "admin" {
		t.Fatalf("login after promotion: %+v", login.User)
	}
	if status, body := role(sari.ID, `{"role":"customer"}`); status != http.StatusConflict || !strings.Contains(body, "last admin") {
		t.Fatalf("demoting the last admin: %d %s", status, body)
	}
	role(budi.ID, `{"role":"admin"}`)
	if status, _ := role(sari.ID, `{"role":"customer"}`); status != http.StatusOK {
		t.Fatalf("demote with another admin left: %d", status)
	}
	_, me := request(t, http.MethodGet, server.URL+"/me", "", login.Token)
	if !strings.Contains(string(me), `"role":"customer"`) {
		t.Fatalf("/me after demotion: %s", me)
	}
	for body, want := range map[string]int{`{"role":"owner"}`: 400, `{"role":""}`: 400, `{"role":"admin","x":1}`: 400, `not json`: 400} {
		if status, _ := role(sari.ID, body); status != want {
			t.Errorf("%s: %d, want %d", body, status, want)
		}
	}
	for _, id := range []string{"999999", "0", "abc"} {
		if status, _ := request(t, http.MethodPut, server.URL+"/internal/users/"+id+"/role", `{"role":"admin"}`, testAdminToken); status != http.StatusNotFound {
			t.Errorf("id %s: %d", id, status)
		}
	}
}

func TestTwoAdminsCantRemoveEachOther(t *testing.T) {
	server := newTestServer(t, nil)
	for round := 0; round < 20; round++ {
		if _, err := server.pool.Exec(context.Background(), "TRUNCATE users, addresses RESTART IDENTITY"); err != nil {
			t.Fatal(err)
		}
		a := server.register(t, "a@example.com", "rahasia-sekali")
		b := server.register(t, "b@example.com", "rahasia-sekali")
		server.makeAdmin(t, a.ID)
		server.makeAdmin(t, b.ID)
		var wg sync.WaitGroup
		for _, id := range []int64{a.ID, b.ID} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				request(t, http.MethodPut, server.URL+fmt.Sprintf("/internal/users/%d/role", id), `{"role":"customer"}`, testAdminToken)
			}()
		}
		wg.Wait()
		if page := server.list(t, "role=admin"); page.Total != 1 {
			t.Fatalf("round %d: %d admins left", round, page.Total)
		}
	}
}
