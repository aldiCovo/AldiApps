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
	userToken    = "user-internal-token-0123456789abcdef"
	orderToken   = "order-internal-token-0123456789abcdef"
	catalogToken = "catalog-internal-token-0123456789abcdef"
	userAdmin    = "user-admin-token-0123456789abcdef-xyz"
	chatToken    = "chat-internal-token-0123456789abcdef"
)

// fakes stands in for user-service and order-service, and records what reached order-service.
type fakes struct {
	mu          sync.Mutex
	roles       map[string]string // bearer token -> role
	orderHits   []string
	catalogHits []string
	catalogAuth int    // status catalog-service answers to any token, to fake a wrong one
	advance     int    // status order-service answers to a status change
	category    string // the name of category 1, which a PUT changes
	photos      string // the photos folder
	userHits    []string
	userRefuses bool // user-service refuses the admin token
	chatHits    []string
}

func newTestServer(t *testing.T) (*httptest.Server, *fakes) {
	t.Helper()
	f := &fakes{roles: map[string]string{"admin-token": "admin", "customer-token": "customer"}, advance: http.StatusOK, category: "Office On Style", photos: t.TempDir()}
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
		case r.URL.Path == "/internal/users" || strings.HasSuffix(r.URL.Path, "/role"):
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			refuses := f.userRefuses || bearer != userAdmin
			if !refuses {
				f.userHits = append(f.userHits, r.Method+" "+r.URL.RequestURI()+" "+string(body))
			}
			f.mu.Unlock()
			switch {
			case refuses:
				w.WriteHeader(http.StatusUnauthorized)
			case r.URL.Path == "/internal/users":
				io.WriteString(w, `{"users":[{"id":3,"email":"c@example.com","name":"C","role":"customer"}],"total":1}`)
			case r.URL.Path == "/internal/users/4/role":
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"error":"the last admin can't stop being an admin"}`)
			case r.URL.Path == "/internal/users/9/role":
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":"user not found"}`)
			default:
				io.WriteString(w, `{"id":3,"email":"c@example.com","name":"C","role":"admin"}`)
			}
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
		case r.URL.Path == "/internal/vouchers" && r.Method == http.MethodGet:
			io.WriteString(w, `{"vouchers":[{"id":1,"code":"HEMAT10","used":3,"user_id":null},{"id":2,"code":"KHUSUS","audience":"user","user_id":2,"used":0}]}`)
		case r.URL.Path == "/internal/vouchers" && r.Method == http.MethodPost:
			if strings.Contains(string(body), `"code":"TAKEN"`) {
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"error":"another voucher has this code"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":2,"code":"BARU"}`)
		case r.URL.Path == "/internal/vouchers/1" && r.Method == http.MethodPut:
			io.WriteString(w, `{"id":1,"code":"HEMAT20"}`)
		case r.URL.Path == "/internal/vouchers/1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
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
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.catalogHits = append(f.catalogHits, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+string(body))
		auth := f.catalogAuth
		f.mu.Unlock()
		if auth != 0 {
			w.WriteHeader(auth)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/categories":
			f.mu.Lock()
			name, _ := json.Marshal(f.category)
			f.mu.Unlock()
			io.WriteString(w, `{"categories":[{"id":1,"name":`+string(name)+`},{"id":2,"name":"Kaos & Polo"}]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/categories/1":
			var in struct{ Name string }
			json.Unmarshal(body, &in)
			f.mu.Lock()
			f.category = in.Name
			f.mu.Unlock()
			io.WriteString(w, `{"id":1,"name":"`+in.Name+`"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/categories/1":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/products/5":
			io.WriteString(w, `{"id":5,"name":"Dress","price":350000,"variants":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/products":
			io.WriteString(w, `{"products":[{"id":5,"name":"Dress","price":350000,"variants":[]}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/products":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":6,"name":"Rok","price":150000,"variants":[]}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/products/5":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/products/5/sale":
			io.WriteString(w, `{"id":5,"name":"Dress","price":300000,"original_price":350000,"variants":[]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/variants/3/stock":
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"stock would leave its range"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"product not found"}`)
		}
	}))
	t.Cleanup(catalog.Close)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+chatToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.chatHits = append(f.chatHits, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/internal/conversations":
			io.WriteString(w, `{"conversations":[{"user_id":2,"waiting":true,"unread":1,"last_message":{"id":5,"from":"customer","body":"Halo"}},{"user_id":7,"waiting":false,"unread":0,"last_message":{"id":3,"from":"admin","body":"Siap"}}],"total":2,"waiting":1}`)
		case r.URL.Path == "/internal/chat/waiting":
			io.WriteString(w, `{"waiting":1}`)
		case r.URL.Path == "/internal/conversations/2/messages" && r.Method == http.MethodGet:
			io.WriteString(w, `{"messages":[{"id":5,"from":"customer","body":"Halo"}],"has_more":false,"read_up_to":5,"unread":1}`)
		case r.URL.Path == "/internal/conversations/2/messages":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"message":{"id":6,"from":"admin","body":"Siap"}}`)
		case r.URL.Path == "/internal/conversations/2/read":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/internal/quick-replies" && r.Method == http.MethodGet:
			io.WriteString(w, `{"quick_replies":[{"id":1,"body":"Halo Kak {nama}"}]}`)
		case r.URL.Path == "/internal/quick-replies":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":2,"body":"Baru"}`)
		case r.URL.Path == "/internal/quick-replies/2" && r.Method == http.MethodPut:
			io.WriteString(w, `{"id":2,"body":"Ubah"}`)
		case r.URL.Path == "/internal/quick-replies/2" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/internal/conversations/8/messages":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"secret detail"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"conversation not found"}`)
		}
	}))
	t.Cleanup(chat.Close)
	server := httptest.NewServer(newHandler(services{userURL: users.URL, orderURL: orders.URL, catalogURL: catalog.URL,
		userInternalToken: userToken, orderInternalToken: orderToken, catalogInternalToken: catalogToken, photosDir: f.photos, userAdminToken: userAdmin,
		chatURL: chat.URL, chatInternalToken: chatToken}))
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

func TestCatalogChangesNeedAnAdminAndCarryTheInternalToken(t *testing.T) {
	server, f := newTestServer(t)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/categories", ""},
		{http.MethodPost, "/categories", `{"name":"Kaftan"}`},
		{http.MethodPut, "/categories/1", `{"name":"Kaftan"}`},
		{http.MethodDelete, "/categories/1", ""},
		{http.MethodGet, "/products", ""},
		{http.MethodGet, "/products/5", ""},
		{http.MethodPost, "/products", `{"name":"Rok","price":150000}`},
		{http.MethodPut, "/products/5", `{"name":"Rok","price":150000}`},
		{http.MethodDelete, "/products/5", ""},
		{http.MethodPut, "/products/5/sale", `{"price":100000}`},
		{http.MethodDelete, "/products/5/sale", ""},
		{http.MethodPost, "/products/5/variants", `{"size":"M","color":"Hitam"}`},
		{http.MethodPut, "/variants/3", `{"size":"M","color":"Hitam"}`},
		{http.MethodDelete, "/variants/3", ""},
		{http.MethodPatch, "/variants/3/stock", `{"delta":1}`},
	}
	for _, token := range []string{"", "customer-token"} {
		for _, route := range routes {
			if status, _ := do(t, server, route.method, route.path, token, route.body); status != http.StatusUnauthorized && status != http.StatusForbidden {
				t.Fatalf("%s %s with %q: %d", route.method, route.path, token, status)
			}
		}
	}
	if len(f.catalogHits) != 0 {
		t.Fatalf("catalog-service was called: %v", f.catalogHits)
	}
	if status, body := do(t, server, http.MethodGet, "/products?limit=100&offset=0&category_id=2&x=1", "admin-token", ""); status != http.StatusOK || !strings.Contains(body, `"Dress"`) {
		t.Fatalf("list: %d %s", status, body)
	}
	if want := "GET /products?category_id=2&limit=100&offset=0 Bearer " + catalogToken + " "; f.catalogHits[0] != want {
		t.Fatalf("sent %q", f.catalogHits[0])
	}
	if status, body := do(t, server, http.MethodPost, "/products", "admin-token", `{"name":"Rok","price":150000}`); status != http.StatusCreated || !strings.Contains(body, `"id":6`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if want := `POST /products Bearer ` + catalogToken + ` {"name":"Rok","price":150000}`; f.catalogHits[1] != want {
		t.Fatalf("sent %q", f.catalogHits[1])
	}
	if status, _ := do(t, server, http.MethodDelete, "/products/5", "admin-token", ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	sale := `{"price":300000,"starts_at":"2026-11-06T00:00:00Z","ends_at":"2026-11-13T00:00:00Z"}`
	if status, body := do(t, server, http.MethodPut, "/products/5/sale", "admin-token", sale); status != http.StatusOK || !strings.Contains(body, `"original_price":350000`) {
		t.Fatalf("sale: %d %s", status, body)
	}
	if want := "PUT /products/5/sale Bearer " + catalogToken + " " + sale; f.catalogHits[len(f.catalogHits)-1] != want {
		t.Fatalf("sent %q", f.catalogHits[len(f.catalogHits)-1])
	}
	if status, body := do(t, server, http.MethodPatch, "/variants/3/stock", "admin-token", `{"delta":999}`); status != http.StatusConflict || !strings.Contains(body, "range") {
		t.Fatalf("stock: %d %s", status, body)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/products", `not json`},
		{http.MethodPost, "/products", `[1]`},
		{http.MethodPut, "/products/abc", `{}`},
		{http.MethodDelete, "/variants/0", ""},
	} {
		if status, _ := do(t, server, c.method, c.path, "admin-token", c.body); status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Fatalf("%s %s: %d", c.method, c.path, status)
		}
	}
	// A wrong internal token must not look like a logged-out admin.
	f.mu.Lock()
	f.catalogAuth = http.StatusUnauthorized
	f.mu.Unlock()
	if status, _ := do(t, server, http.MethodGet, "/products", "admin-token", ""); status != http.StatusBadGateway {
		t.Fatalf("wrong internal token: %d", status)
	}
}

func TestVoucherRoutesGoToOrderService(t *testing.T) {
	server, f := newTestServer(t)
	for _, c := range []struct {
		method, path, body string
		status             int
		answer             string
	}{
		{http.MethodGet, "/vouchers", "", http.StatusOK, `"code":"HEMAT10"`},
		{http.MethodPost, "/vouchers", `{"code":"BARU"}`, http.StatusCreated, `"code":"BARU"`},
		{http.MethodPost, "/vouchers", `{"code":"TAKEN"}`, http.StatusConflict, "another voucher"},
		{http.MethodPost, "/vouchers", `not json`, http.StatusBadRequest, "invalid JSON"},
		{http.MethodPut, "/vouchers/1", `{"code":"HEMAT20"}`, http.StatusOK, `"code":"HEMAT20"`},
		{http.MethodPut, "/vouchers/abc", `{}`, http.StatusNotFound, "voucher not found"},
		{http.MethodDelete, "/vouchers/1", "", http.StatusNoContent, ""},
		{http.MethodDelete, "/vouchers/5", "", http.StatusNotFound, ""},
	} {
		status, body := do(t, server, c.method, c.path, "admin-token", c.body)
		if status != c.status || !strings.Contains(body, c.answer) {
			t.Errorf("%s %s: %d %s", c.method, c.path, status, body)
		}
	}
	f.mu.Lock()
	hits := strings.Join(f.orderHits, "\n")
	f.mu.Unlock()
	if strings.Contains(hits, "abc") || strings.Contains(hits, "not json") {
		t.Fatalf("a refused request reached order-service: %s", hits)
	}
	for _, token := range []string{"", "customer-token"} {
		if status, _ := do(t, server, http.MethodPost, "/vouchers", token, `{"code":"X"}`); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("%q: %d", token, status)
		}
	}
}
