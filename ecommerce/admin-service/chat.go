package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
)

// chatOrdersShown is how many of a customer's newest orders sit beside their chat.
const chatOrdersShown = "3"

// addChatRoutes lets admins read and answer customers' chats. chat-service keeps only user ids,
// so the customer's name and email come from user-service here.
func (a *admin) addChatRoutes(mux *http.ServeMux) {
	// pass sends chat-service's answer on as it is, hiding only its failures.
	pass := func(w http.ResponseWriter, method, target string, body []byte, r *http.Request) {
		status, data, err := a.raw(r.Context(), method, target, a.chatInternalToken, body)
		if err != nil || status >= http.StatusInternalServerError {
			slog.Error("chat-service request failed", "status", status, "error", err)
			writeError(w, http.StatusBadGateway, "a service is not available")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(data)
	}
	customer := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return "", false
		}
		return id, true
	}

	mux.HandleFunc("GET /chat/conversations", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		query := url.Values{}
		for _, name := range []string{"filter", "limit", "offset"} {
			if value := r.URL.Query().Get(name); value != "" {
				query.Set(name, value)
			}
		}
		var out struct {
			Conversations []map[string]any `json:"conversations"`
			Total         json.Number      `json:"total"`
			Waiting       json.Number      `json:"waiting"`
		}
		if err := a.call(r.Context(), http.MethodGet, a.chatURL+"/internal/conversations?"+query.Encode(), a.chatInternalToken, nil, &out); err != nil {
			a.fail(w, err)
			return
		}
		if out.Conversations == nil {
			out.Conversations = []map[string]any{}
		}
		if err := a.addCustomers(r.Context(), out.Conversations); err != nil {
			a.fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}))

	mux.HandleFunc("GET /chat/waiting", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		pass(w, http.MethodGet, a.chatURL+"/internal/chat/waiting", nil, r)
	}))

	// Who the customer is and their newest orders, for the panel beside the chat.
	mux.HandleFunc("GET /chat/conversations/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := customer(w, r)
		if !ok {
			return
		}
		out := map[string]any{"user_id": json.Number(id), "customer": nil}
		var user map[string]any
		err := a.call(r.Context(), http.MethodGet, a.userURL+"/internal/users/"+id, a.userInternalToken, nil, &user)
		var answer *upstreamError
		switch {
		case errors.As(err, &answer) && answer.status == http.StatusNotFound:
		case err != nil:
			a.fail(w, err)
			return
		default:
			out["customer"] = map[string]any{"name": user["name"], "email": user["email"], "created_at": user["created_at"]}
		}
		var orders struct {
			Orders []map[string]any `json:"orders"`
		}
		if err := a.call(r.Context(), http.MethodGet, a.orderURL+"/internal/orders?limit="+chatOrdersShown+"&user_id="+id, a.orderInternalToken, nil, &orders); err != nil {
			a.fail(w, err)
			return
		}
		shown := []map[string]any{}
		for _, order := range orders.Orders {
			shown = append(shown, map[string]any{"id": order["id"], "status": order["status"], "total": order["total"], "created_at": order["created_at"]})
		}
		out["orders"] = shown
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}))

	mux.HandleFunc("GET /chat/conversations/{id}/messages", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := customer(w, r)
		if !ok {
			return
		}
		query := url.Values{}
		for _, name := range []string{"before", "limit"} {
			if value := r.URL.Query().Get(name); value != "" {
				query.Set(name, value)
			}
		}
		pass(w, http.MethodGet, a.chatURL+"/internal/conversations/"+id+"/messages?"+query.Encode(), nil, r)
	}))

	mux.HandleFunc("POST /chat/conversations/{id}/messages", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := customer(w, r)
		if !ok {
			return
		}
		var in struct {
			Body string `json:"body"`
			// A product card; chat-service checks the product and refuses orders from admins.
			Attach *struct {
				Kind string `json:"kind"`
				ID   int64  `json:"id"`
			} `json:"attach"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		// Who answered is the admin the token belongs to, never what the page says.
		out := map[string]any{"body": in.Body, "admin_id": me.ID}
		if in.Attach != nil {
			out["attach"] = in.Attach
		}
		body, _ := json.Marshal(out)
		pass(w, http.MethodPost, a.chatURL+"/internal/conversations/"+id+"/messages", body, r)
	}))

	// The answers every admin shares. chat-service checks the text and the limit.
	quick := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "quick reply not found")
			return "", false
		}
		return id, true
	}
	quickBody := func(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
		var in struct {
			Body string `json:"body"`
		}
		if !readJSON(w, r, &in) {
			return nil, false
		}
		body, _ := json.Marshal(in)
		return body, true
	}
	mux.HandleFunc("GET /chat/quick-replies", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		pass(w, http.MethodGet, a.chatURL+"/internal/quick-replies", nil, r)
	}))
	mux.HandleFunc("POST /chat/quick-replies", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		if body, ok := quickBody(w, r); ok {
			pass(w, http.MethodPost, a.chatURL+"/internal/quick-replies", body, r)
		}
	}))
	mux.HandleFunc("PUT /chat/quick-replies/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := quick(w, r)
		if !ok {
			return
		}
		if body, ok := quickBody(w, r); ok {
			pass(w, http.MethodPut, a.chatURL+"/internal/quick-replies/"+id, body, r)
		}
	}))
	mux.HandleFunc("DELETE /chat/quick-replies/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		if id, ok := quick(w, r); ok {
			pass(w, http.MethodDelete, a.chatURL+"/internal/quick-replies/"+id, nil, r)
		}
	}))

	// The assistant that answers customers first. chat-service checks the settings.
	mux.HandleFunc("GET /chat/bot-settings", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		pass(w, http.MethodGet, a.chatURL+"/internal/bot-settings", nil, r)
	}))
	mux.HandleFunc("PUT /chat/bot-settings", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		var in struct {
			Mode              string `json:"mode"`
			StoreInfo         string `json:"store_info"`
			Telegram          bool   `json:"telegram"`
			QuietOutsideHours bool   `json:"quiet_outside_hours"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		body, _ := json.Marshal(in)
		pass(w, http.MethodPut, a.chatURL+"/internal/bot-settings", body, r)
	}))
	// An admin takes a conversation over from the bot, or gives it back.
	mux.HandleFunc("POST /chat/conversations/{id}/bot", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id, ok := customer(w, r)
		if !ok {
			return
		}
		var in struct {
			On *bool `json:"on"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.On == nil {
			writeError(w, http.StatusBadRequest, "on is required")
			return
		}
		body, _ := json.Marshal(in)
		pass(w, http.MethodPost, a.chatURL+"/internal/conversations/"+id+"/bot", body, r)
	}))

	mux.HandleFunc("POST /chat/conversations/{id}/read", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		if id, ok := customer(w, r); ok {
			pass(w, http.MethodPost, a.chatURL+"/internal/conversations/"+id+"/read", nil, r)
		}
	}))
}
