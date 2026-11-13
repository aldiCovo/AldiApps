package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestVoucherForOneCustomer(t *testing.T) {
	server, f := newTestServer(t)
	lastOrderHit := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.orderHits[len(f.orderHits)-1]
	}

	// The email becomes the account's id; order-service never sees the email.
	status, body := do(t, server, http.MethodPost, "/vouchers", "admin-token", `{"code":"BARU","audience":"user","customer_email":" C@Example.com "}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	if hit := lastOrderHit(); !strings.Contains(hit, `"user_id":3`) || strings.Contains(hit, "customer_email") {
		t.Fatalf("order-service got %s", hit)
	}

	// An email without an account is refused here.
	f.mu.Lock()
	before := len(f.orderHits)
	f.mu.Unlock()
	status, body = do(t, server, http.MethodPost, "/vouchers", "admin-token", `{"code":"BARU","audience":"user","customer_email":"nobody@example.com"}`)
	f.mu.Lock()
	after := len(f.orderHits)
	f.mu.Unlock()
	if status != http.StatusBadRequest || !strings.Contains(body, "no customer has this email") || after != before {
		t.Fatalf("unknown email: %d %s, order-service hit %v", status, body, after != before)
	}

	// Other audiences lose any user_id or email the page sent.
	if status, _ := do(t, server, http.MethodPut, "/vouchers/1", "admin-token", `{"code":"HEMAT20","audience":"all","user_id":7,"customer_email":"c@example.com"}`); status != http.StatusOK {
		t.Fatalf("update: %d", status)
	}
	if hit := lastOrderHit(); strings.Contains(hit, "user_id") || strings.Contains(hit, "customer_email") {
		t.Fatalf("order-service got %s", hit)
	}

	// The list shows who a personal voucher is for.
	status, body = do(t, server, http.MethodGet, "/vouchers", "admin-token", "")
	if status != http.StatusOK || !strings.Contains(body, `"customer":{"email":"buyer@example.com","name":"Buyer"}`) || strings.Count(body, `"customer"`) != 1 {
		t.Fatalf("list: %d %s", status, body)
	}
}

func TestSignupPopupSettings(t *testing.T) {
	server, f := newTestServer(t)
	for _, token := range []string{"", "customer-token"} {
		if status, _ := do(t, server, http.MethodGet, "/signup-popup", token, ""); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("token %q: status %d", token, status)
		}
	}
	if status, body := do(t, server, http.MethodGet, "/signup-popup", "admin-token", ""); status != http.StatusOK || body != `{"rest_hours":168}` {
		t.Fatalf("read: %d %s", status, body)
	}
	if status, body := do(t, server, http.MethodPut, "/signup-popup", "admin-token", `{"rest_hours":3}`); status != http.StatusOK || body != `{"rest_hours":3}` {
		t.Fatalf("save: %d %s", status, body)
	}
	f.mu.Lock()
	last := f.orderHits[len(f.orderHits)-1]
	f.mu.Unlock()
	if last != `PUT /internal/signup-popup {"rest_hours":3}` {
		t.Fatalf("forwarded %q", last)
	}
	// order-service's refusal reaches the admin; an unknown field stops here.
	if status, body := do(t, server, http.MethodPut, "/signup-popup", "admin-token", `{"rest_hours":1}`); status != http.StatusBadRequest || !strings.Contains(body, "between 2 and 8760") {
		t.Fatalf("refused: %d %s", status, body)
	}
	if status, _ := do(t, server, http.MethodPut, "/signup-popup", "admin-token", `{"rest_hours":3,"other":1}`); status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", status)
	}
}
