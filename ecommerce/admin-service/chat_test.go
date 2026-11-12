package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestChat(t *testing.T) {
	server, f := newTestServer(t)

	// Only admins get in.
	for _, token := range []string{"", "customer-token"} {
		if status, _ := do(t, server, "GET", "/chat/conversations", token, ""); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("token %q: status %d", token, status)
		}
	}

	status, body := do(t, server, "GET", "/chat/conversations?filter=waiting&limit=20&x=1", "admin-token", "")
	if status != http.StatusOK || !strings.Contains(body, `"customer":{"email":"buyer@example.com","name":"Buyer"}`) || !strings.Contains(body, `"customer":null`) || !strings.Contains(body, `"waiting":1`) {
		t.Fatalf("list: status %d, body %s", status, body)
	}
	if got := f.chatHits[0]; got != "GET /internal/conversations?filter=waiting&limit=20 " {
		t.Fatalf("list query: %q", got)
	}

	// The customer panel: who they are and their newest orders.
	status, body = do(t, server, "GET", "/chat/conversations/2", "admin-token", "")
	if status != http.StatusOK || !strings.Contains(body, `"name":"Buyer"`) || !strings.Contains(body, `"id":9`) || strings.Contains(body, "shipping_address") {
		t.Fatalf("panel: status %d, body %s", status, body)
	}
	if got := f.orderHits[len(f.orderHits)-1]; !strings.Contains(got, "/internal/orders?limit=3&user_id=2") {
		t.Fatalf("panel orders: %q", got)
	}
	if status, body := do(t, server, "GET", "/chat/conversations/5", "admin-token", ""); status != http.StatusOK || !strings.Contains(body, `"customer":null`) {
		t.Fatalf("deleted account: status %d, body %s", status, body)
	}

	status, body = do(t, server, "GET", "/chat/conversations/2/messages?before=9", "admin-token", "")
	if status != http.StatusOK || !strings.Contains(body, `"read_up_to":5`) {
		t.Fatalf("messages: status %d, body %s", status, body)
	}
	// The admin id comes from the token, not from the page.
	status, body = do(t, server, "POST", "/chat/conversations/2/messages", "admin-token", `{"body":"Siap"}`)
	if status != http.StatusCreated || !strings.Contains(body, `"from":"admin"`) {
		t.Fatalf("send: status %d, body %s", status, body)
	}
	if got := f.chatHits[len(f.chatHits)-1]; !strings.Contains(got, `"admin_id":`) || !strings.Contains(got, `"body":"Siap"`) {
		t.Fatalf("send forwarded: %q", got)
	}
	if status, _ := do(t, server, "POST", "/chat/conversations/2/messages", "admin-token", `{"body":"","attach":{"kind":"product","id":5}}`); status != http.StatusCreated {
		t.Fatalf("send card: status %d", status)
	}
	if got := f.chatHits[len(f.chatHits)-1]; !strings.Contains(got, `"attach":{"kind":"product","id":5}`) || strings.Contains(got, `"attach":null`) {
		t.Fatalf("card forwarded: %q", got)
	}
	if status, _ := do(t, server, "POST", "/chat/conversations/2/messages", "admin-token", `{"body":"Siap lagi"}`); status != http.StatusCreated || strings.Contains(f.chatHits[len(f.chatHits)-1], "attach") {
		t.Fatalf("no card forwarded: %q", f.chatHits[len(f.chatHits)-1])
	}
	if status, _ := do(t, server, "POST", "/chat/conversations/2/messages", "admin-token", `{"body":"x","admin_id":99}`); status != http.StatusBadRequest {
		t.Fatalf("admin_id from the page: status %d", status)
	}
	if status, _ := do(t, server, "POST", "/chat/conversations/2/read", "admin-token", ""); status != http.StatusNoContent {
		t.Fatalf("read: status %d", status)
	}
	if status, body := do(t, server, "GET", "/chat/waiting", "admin-token", ""); status != http.StatusOK || strings.TrimSpace(body) != `{"waiting":1}` {
		t.Fatalf("waiting: status %d, body %s", status, body)
	}
	// Refusals pass through; failures don't show their details.
	if status, _ := do(t, server, "GET", "/chat/conversations/3/messages", "admin-token", ""); status != http.StatusNotFound {
		t.Fatalf("unknown: status %d", status)
	}
	if status, _ := do(t, server, "GET", "/chat/conversations/abc/messages", "admin-token", ""); status != http.StatusNotFound {
		t.Fatalf("bad id: status %d", status)
	}
	if status, body := do(t, server, "GET", "/chat/conversations/8/messages", "admin-token", ""); status != http.StatusBadGateway || strings.Contains(body, "secret") {
		t.Fatalf("failure: status %d, body %s", status, body)
	}
}

func TestQuickReplies(t *testing.T) {
	server, f := newTestServer(t)
	for _, token := range []string{"", "customer-token"} {
		if status, _ := do(t, server, "GET", "/chat/quick-replies", token, ""); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("token %q: status %d", token, status)
		}
	}
	if status, body := do(t, server, "GET", "/chat/quick-replies", "admin-token", ""); status != http.StatusOK || !strings.Contains(body, "{nama}") {
		t.Fatalf("list: %d %s", status, body)
	}
	if status, _ := do(t, server, "POST", "/chat/quick-replies", "admin-token", `{"body":"Baru"}`); status != http.StatusCreated {
		t.Fatalf("add: %d", status)
	}
	if got := f.chatHits[len(f.chatHits)-1]; got != `POST /internal/quick-replies {"body":"Baru"}` {
		t.Fatalf("add forwarded: %q", got)
	}
	if status, _ := do(t, server, "POST", "/chat/quick-replies", "admin-token", `{"body":"x","id":9}`); status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", status)
	}
	if status, _ := do(t, server, "PUT", "/chat/quick-replies/2", "admin-token", `{"body":"Ubah"}`); status != http.StatusOK {
		t.Fatalf("update: %d", status)
	}
	if status, _ := do(t, server, "DELETE", "/chat/quick-replies/2", "admin-token", ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if status, _ := do(t, server, "DELETE", "/chat/quick-replies/abc", "admin-token", ""); status != http.StatusNotFound {
		t.Fatalf("bad id: %d", status)
	}
	if status, _ := do(t, server, "DELETE", "/chat/quick-replies/7", "admin-token", ""); status != http.StatusNotFound {
		t.Fatalf("missing: %d", status)
	}
}

func TestBotRoutes(t *testing.T) {
	server, f := newTestServer(t)
	for _, token := range []string{"", "customer-token"} {
		if status, _ := do(t, server, "GET", "/chat/bot-settings", token, ""); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("token %q: status %d", token, status)
		}
	}
	if status, body := do(t, server, "GET", "/chat/bot-settings", "admin-token", ""); status != http.StatusOK || !strings.Contains(body, `"configured":true`) {
		t.Fatalf("settings: %d %s", status, body)
	}
	if status, _ := do(t, server, "PUT", "/chat/bot-settings", "admin-token", `{"mode":"off","store_info":"Retur 3 hari","telegram":false,"quiet_outside_hours":true,"ai":"none"}`); status != http.StatusNoContent {
		t.Fatalf("save: %d", status)
	}
	if got := f.chatHits[len(f.chatHits)-1]; got != `PUT /internal/bot-settings {"mode":"off","store_info":"Retur 3 hari","telegram":false,"quiet_outside_hours":true,"ai":"none"}` {
		t.Fatalf("save forwarded: %q", got)
	}
	if status, _ := do(t, server, "PUT", "/chat/bot-settings", "admin-token", `{"mode":"off","extra":1}`); status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", status)
	}
	if status, _ := do(t, server, "POST", "/chat/conversations/2/bot", "admin-token", `{"on":false}`); status != http.StatusNoContent {
		t.Fatalf("take over: %d", status)
	}
	if got := f.chatHits[len(f.chatHits)-1]; got != `POST /internal/conversations/2/bot {"on":false}` {
		t.Fatalf("take over forwarded: %q", got)
	}
	for _, c := range []struct{ path, body string }{{"/chat/conversations/2/bot", `{}`}, {"/chat/conversations/x/bot", `{"on":true}`}} {
		if status, _ := do(t, server, "POST", c.path, "admin-token", c.body); status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Errorf("%s %s: %d", c.path, c.body, status)
		}
	}
}
