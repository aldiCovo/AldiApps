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

const (
	userToken  = "user-internal-token-0123456789abcdef"
	orderToken = "order-internal-token-0123456789abcdef"
)

// fakes stands in for user-service and order-service, and records what reached order-service.
type fakes struct {
	mu        sync.Mutex
	roles     map[string]string // bearer token -> role
	orderHits []string
	advance   int // status order-service answers to a status change
}

func newTestServer(t *testing.T) (*httptest.Server, *fakes) {
	t.Helper()
	f := &fakes{roles: map[string]string{"admin-token": "admin", "customer-token": "customer"}, advance: http.StatusOK}
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			var in struct{ Email, Password string }
			json.NewDecoder(r.Body).Decode(&in)
			switch in.Email {
			case "admin@example.com":
				io.WriteString(w, `{"token":"admin-token","token_type":"Bearer","expires_in":3600,"user":{"id":1,"email":"admin@example.com","name":"Admin","role":"admin"}}`)
			case "buyer@example.com":
				io.WriteString(w, `{"token":"customer-token","token_type":"Bearer","expires_in":3600,"user":{"id":2,"email":"buyer@example.com","name":"Buyer","role":"customer"}}`)
			default:
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"wrong email or password"}`)
			}
		case r.URL.Path == "/me":
			f.mu.Lock()
			role, found := f.roles[bearer]
			f.mu.Unlock()
			if !found {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"the token is not valid"}`)
				return
			}
			io.WriteString(w, `{"id":1,"email":"admin@example.com","name":"Admin","role":"`+role+`"}`)
		case r.URL.Path == "/internal/users/2" && bearer == userToken:
			io.WriteString(w, `{"id":2,"email":"buyer@example.com","name":"Buyer","role":"customer"}`)
		case strings.HasPrefix(r.URL.Path, "/internal/users/") && bearer == userToken:
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(users.Close)
	orders := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+orderToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.orderHits = append(f.orderHits, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		advance := f.advance
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/internal/orders":
			io.WriteString(w, `{"orders":[{"id":9,"user_id":2,"status":"paid","total":12345678901234567},{"id":8,"user_id":7,"status":"paid","total":1}]}`)
		case r.URL.Path == "/internal/orders/9/status":
			w.WriteHeader(advance)
			if advance == http.StatusOK {
				io.WriteString(w, `{"id":9,"user_id":2,"status":"packed"}`)
			} else {
				io.WriteString(w, `{"error":"the order isn't in the step before this one"}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"order not found"}`)
		}
	}))
	t.Cleanup(orders.Close)
	server := httptest.NewServer(newHandler(services{userURL: users.URL, orderURL: orders.URL, userInternalToken: userToken, orderInternalToken: orderToken}))
	t.Cleanup(server.Close)
	return server, f
}

func do(t *testing.T, server *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func TestOnlyAdminsCanLogIn(t *testing.T) {
	server, _ := newTestServer(t)
	status, body := do(t, server, http.MethodPost, "/login", "", `{"email":"admin@example.com","password":"x"}`)
	if status != http.StatusOK || !strings.Contains(body, `"token":"admin-token"`) {
		t.Fatalf("admin: %d %s", status, body)
	}
	if status, body := do(t, server, http.MethodPost, "/login", "", `{"email":"buyer@example.com","password":"x"}`); status != http.StatusForbidden || strings.Contains(body, "customer-token") {
		t.Fatalf("customer: %d %s", status, body)
	}
	if status, _ := do(t, server, http.MethodPost, "/login", "", `{"email":"nobody@example.com","password":"x"}`); status != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", status)
	}
	if status, _ := do(t, server, http.MethodPost, "/login", "", `{"email":"admin@example.com","password":"x","role":"admin"}`); status != http.StatusBadRequest {
		t.Fatalf("extra field: %d", status)
	}
}

func TestEveryAdminRouteChecksTheRoleEachTime(t *testing.T) {
	server, f := newTestServer(t)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/me", ""},
		{http.MethodGet, "/orders", ""},
		{http.MethodGet, "/orders/9", ""},
		{http.MethodPost, "/orders/9/status", `{"status":"packed"}`},
	}
	for _, token := range []string{"", "customer-token", "made-up"} {
		for _, route := range routes {
			status, _ := do(t, server, route.method, route.path, token, route.body)
			if status != http.StatusUnauthorized && status != http.StatusForbidden {
				t.Fatalf("%s %s with %q: %d", route.method, route.path, token, status)
			}
		}
	}
	if len(f.orderHits) != 0 {
		t.Fatalf("order-service was called: %v", f.orderHits)
	}
	if status, _ := do(t, server, http.MethodGet, "/me", "admin-token", ""); status != http.StatusOK {
		t.Fatalf("admin: %d", status)
	}
	// The account stops being an admin while its token is still valid.
	f.mu.Lock()
	f.roles["admin-token"] = "customer"
	f.mu.Unlock()
	if status, _ := do(t, server, http.MethodGet, "/orders", "admin-token", ""); status != http.StatusForbidden {
		t.Fatalf("demoted admin: %d", status)
	}
}

func TestOrdersCarryTheirCustomer(t *testing.T) {
	server, f := newTestServer(t)
	status, body := do(t, server, http.MethodGet, "/orders?status=paid&limit=5&offset=10&other=x", "admin-token", "")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	var got struct{ Orders []map[string]any }
	json.Unmarshal([]byte(body), &got)
	if len(got.Orders) != 2 || got.Orders[1]["customer"] != nil {
		t.Fatalf("%s", body)
	}
	if customer, _ := got.Orders[0]["customer"].(map[string]any); customer["name"] != "Buyer" || customer["email"] != "buyer@example.com" {
		t.Fatalf("%s", body)
	}
	if !strings.Contains(body, `"total":12345678901234567`) {
		t.Fatalf("total changed: %s", body)
	}
	if f.orderHits[0] != "GET /internal/orders?limit=5&offset=10&status=paid " {
		t.Fatalf("query %q", f.orderHits[0])
	}
}

func TestStatusChangesPassOnOnlyKnownFields(t *testing.T) {
	server, f := newTestServer(t)
	status, body := do(t, server, http.MethodPost, "/orders/9/status", "admin-token", `{"status":"shipped","tracking_number":"JP1234567"}`)
	if status != http.StatusOK || !strings.Contains(body, `"name":"Buyer"`) {
		t.Fatalf("%d %s", status, body)
	}
	if want := `POST /internal/orders/9/status {"status":"shipped","tracking_number":"JP1234567"}`; f.orderHits[0] != want {
		t.Fatalf("sent %q", f.orderHits[0])
	}
	if status, _ := do(t, server, http.MethodPost, "/orders/9/status", "admin-token", `{"status":"packed","user_id":5}`); status != http.StatusBadRequest {
		t.Fatalf("extra field: %d", status)
	}
	for _, path := range []string{"/orders/0/status", "/orders/abc/status", "/orders/9%2F..%2Fx/status"} {
		if status, _ := do(t, server, http.MethodPost, path, "admin-token", `{"status":"packed"}`); status != http.StatusNotFound {
			t.Fatalf("%s: %d", path, status)
		}
	}
	f.mu.Lock()
	f.advance = http.StatusConflict
	f.mu.Unlock()
	if status, body := do(t, server, http.MethodPost, "/orders/9/status", "admin-token", `{"status":"packed"}`); status != http.StatusConflict || !strings.Contains(body, "step before") {
		t.Fatalf("conflict: %d %s", status, body)
	}
	f.mu.Lock()
	f.advance = http.StatusInternalServerError
	f.mu.Unlock()
	if status, body := do(t, server, http.MethodPost, "/orders/9/status", "admin-token", `{"status":"packed"}`); status != http.StatusBadGateway || strings.Contains(body, "step") {
		t.Fatalf("failure: %d %s", status, body)
	}
}
