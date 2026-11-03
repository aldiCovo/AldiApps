package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
)

// addUserRoutes lists accounts and changes their role through user-service, with a token
// only admin-service has. An admin can't change their own role, so no one locks themselves
// out by accident; user-service also keeps at least one admin.
func (a *admin) addUserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /users", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		query := url.Values{}
		for _, name := range []string{"q", "role", "limit", "offset"} {
			if value := r.URL.Query().Get(name); value != "" {
				query.Set(name, value)
			}
		}
		var page map[string]any
		if err := a.call(r.Context(), http.MethodGet, a.userURL+"/internal/users?"+query.Encode(), a.userAdminToken, nil, &page); err != nil {
			a.failInternal(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	}))

	mux.HandleFunc("PUT /users/{id}/role", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		var in struct {
			Role string `json:"role"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if id == strconv.FormatInt(me.ID, 10) {
			writeError(w, http.StatusConflict, "you can't change your own role")
			return
		}
		body, _ := json.Marshal(in)
		var user map[string]any
		if err := a.call(r.Context(), http.MethodPut, a.userURL+"/internal/users/"+id+"/role", a.userAdminToken, body, &user); err != nil {
			a.failInternal(w, err)
			return
		}
		slog.Info("user role changed", "admin_id", me.ID, "user_id", id, "role", in.Role)
		writeJSON(w, http.StatusOK, user)
	}))
}

// failInternal is fail, except that a refused internal token answers 502, so it can't look
// like a logged-out admin.
func (a *admin) failInternal(w http.ResponseWriter, err error) {
	var answer *upstreamError
	if errors.As(err, &answer) && answer.status == http.StatusUnauthorized {
		slog.Error("user-service refused the admin token")
		writeError(w, http.StatusBadGateway, "a service is not available")
		return
	}
	a.fail(w, err)
}
