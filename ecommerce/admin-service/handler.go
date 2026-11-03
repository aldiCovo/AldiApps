package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	maxBodyBytes     = 1 << 16
	maxUpstreamBytes = 8 << 20
	upstreamTimeout  = 10 * time.Second
	roleAdmin        = "admin"
)

var numericID = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

type services struct {
	userURL, orderURL, catalogURL                               string
	userInternalToken, orderInternalToken, catalogInternalToken string
}

// User is an account as user-service describes it.
type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// upstreamError is a service's answer that isn't a success, passed on to the admin pages.
type upstreamError struct {
	status int
	body   []byte
}

func (e *upstreamError) Error() string { return fmt.Sprintf("upstream answered %d", e.status) }

type admin struct {
	services
	client *http.Client
}

func newHandler(s services) http.Handler {
	a := &admin{services: s, client: &http.Client{
		Timeout:       upstreamTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Logging in goes through user-service as for customers, but only an admin gets the token.
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		body, _ := json.Marshal(in)
		var out struct {
			Token     string `json:"token"`
			ExpiresIn int    `json:"expires_in"`
			User      User   `json:"user"`
		}
		if err := a.call(r.Context(), http.MethodPost, a.userURL+"/login", "", body, &out); err != nil {
			a.fail(w, err)
			return
		}
		if out.User.Role != roleAdmin {
			slog.Warn("a customer tried the admin login", "user_id", out.User.ID)
			writeError(w, http.StatusForbidden, "this account is not an admin")
			return
		}
		slog.Info("admin logged in", "user_id", out.User.ID)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("GET /me", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		writeJSON(w, http.StatusOK, me)
	}))

	mux.HandleFunc("GET /orders", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		query := url.Values{}
		for _, name := range []string{"status", "limit", "offset"} {
			if value := r.URL.Query().Get(name); value != "" {
				query.Set(name, value)
			}
		}
		var out struct {
			Orders []map[string]any `json:"orders"`
		}
		if err := a.call(r.Context(), http.MethodGet, a.orderURL+"/internal/orders?"+query.Encode(), a.orderInternalToken, nil, &out); err != nil {
			a.fail(w, err)
			return
		}
		if out.Orders == nil {
			out.Orders = []map[string]any{}
		}
		if err := a.addCustomers(r.Context(), out.Orders); err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))

	mux.HandleFunc("GET /orders/{id}", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "order not found")
			return
		}
		var order map[string]any
		if err := a.call(r.Context(), http.MethodGet, a.orderURL+"/internal/orders/"+id, a.orderInternalToken, nil, &order); err != nil {
			a.fail(w, err)
			return
		}
		if err := a.addCustomers(r.Context(), []map[string]any{order}); err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))

	mux.HandleFunc("POST /orders/{id}/status", a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
		id := r.PathValue("id")
		if !numericID.MatchString(id) {
			writeError(w, http.StatusNotFound, "order not found")
			return
		}
		var in struct {
			Status         string `json:"status"`
			TrackingNumber string `json:"tracking_number,omitempty"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		body, _ := json.Marshal(in)
		var order map[string]any
		if err := a.call(r.Context(), http.MethodPost, a.orderURL+"/internal/orders/"+id+"/status", a.orderInternalToken, body, &order); err != nil {
			a.fail(w, err)
			return
		}
		slog.Info("order status changed", "admin_id", me.ID, "order_id", id, "status", in.Status)
		if err := a.addCustomers(r.Context(), []map[string]any{order}); err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))

	a.addCatalogRoutes(mux)
	return mux
}

// onlyAdmins asks user-service who the token belongs to on every request, so an account
// that stops being an admin, or is deleted, loses access at once rather than when its
// token expires.
func (a *admin) onlyAdmins(next func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || token == "" {
			writeError(w, http.StatusUnauthorized, "a bearer token is required")
			return
		}
		var me User
		if err := a.call(r.Context(), http.MethodGet, a.userURL+"/me", token, nil, &me); err != nil {
			a.fail(w, err)
			return
		}
		if me.Role != roleAdmin {
			writeError(w, http.StatusForbidden, "this account is not an admin")
			return
		}
		next(w, r, me)
	}
}

// addCustomers puts each order's buyer, by name and email, next to its user_id. A deleted
// account leaves customer null.
func (a *admin) addCustomers(ctx context.Context, orders []map[string]any) error {
	found := map[string]*User{}
	for _, order := range orders {
		id := fmt.Sprint(order["user_id"])
		if !numericID.MatchString(id) {
			continue
		}
		if _, seen := found[id]; !seen {
			var user User
			err := a.call(ctx, http.MethodGet, a.userURL+"/internal/users/"+id, a.userInternalToken, nil, &user)
			var answer *upstreamError
			switch {
			case errors.As(err, &answer) && answer.status == http.StatusNotFound:
				found[id] = nil
			case err != nil:
				return err
			default:
				found[id] = &user
			}
		}
		if user := found[id]; user != nil {
			order["customer"] = map[string]string{"name": user.Name, "email": user.Email}
		} else {
			order["customer"] = nil
		}
	}
	return nil
}

func (a *admin) call(ctx context.Context, method, target, bearer string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &upstreamError{status: resp.StatusCode, body: data}
	}
	// Numbers stay exact, because orders pass through here as plain maps.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(out)
}

// fail passes on a service's refusal, such as a wrong password or a step out of order, and
// hides everything else behind 502, so the admin pages never see internal details.
func (a *admin) fail(w http.ResponseWriter, err error) {
	var answer *upstreamError
	if errors.As(err, &answer) {
		switch answer.status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(answer.status)
			w.Write(answer.body)
			return
		}
	}
	slog.Error("a service request failed", "error", err)
	writeError(w, http.StatusBadGateway, "a service is not available")
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// addCatalogRoutes passes the admin pages' product, variant, price and stock changes to
// catalog-service, which checks them. Each change is logged with the admin who made it.
func (a *admin) addCatalogRoutes(mux *http.ServeMux) {
	ids := func(w http.ResponseWriter, r *http.Request) bool {
		if !numericID.MatchString(r.PathValue("id")) {
			writeError(w, http.StatusNotFound, "not found")
			return false
		}
		return true
	}
	forward := func(method, path string, withBody bool) http.HandlerFunc {
		return a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
			if strings.Contains(path, "{id}") && !ids(w, r) {
				return
			}
			target := strings.Replace(path, "{id}", r.PathValue("id"), 1)
			var body []byte
			if withBody {
				var ok bool
				if body, ok = readRawJSON(w, r); !ok {
					return
				}
			}
			if method == http.MethodGet {
				query := url.Values{}
				for _, name := range []string{"limit", "offset", "category_id"} {
					if value := r.URL.Query().Get(name); value != "" {
						query.Set(name, value)
					}
				}
				if len(query) > 0 {
					target += "?" + query.Encode()
				}
			}
			status, data, err := a.raw(r.Context(), method, a.catalogURL+target, a.catalogInternalToken, body)
			if err != nil {
				a.fail(w, err)
				return
			}
			if method != http.MethodGet {
				slog.Info("catalog changed", "admin_id", me.ID, "method", method, "path", target, "status", status)
			}
			switch {
			case status == http.StatusNoContent:
				w.WriteHeader(status)
			case status == http.StatusOK || status == http.StatusCreated:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				w.Write(data)
			case status == http.StatusUnauthorized:
				// The internal token is wrong, which the admin can't fix by logging in again.
				a.fail(w, errors.New("catalog-service refused the internal token"))
			default:
				a.fail(w, &upstreamError{status: status, body: data})
			}
		})
	}
	mux.HandleFunc("GET /categories", forward(http.MethodGet, "/categories", false))
	mux.HandleFunc("POST /categories", forward(http.MethodPost, "/categories", true))
	mux.HandleFunc("PUT /categories/{id}", forward(http.MethodPut, "/categories/{id}", true))
	mux.HandleFunc("DELETE /categories/{id}", forward(http.MethodDelete, "/categories/{id}", false))
	mux.HandleFunc("GET /products", forward(http.MethodGet, "/products", false))
	mux.HandleFunc("GET /products/{id}", forward(http.MethodGet, "/products/{id}", false))
	mux.HandleFunc("POST /products", forward(http.MethodPost, "/products", true))
	mux.HandleFunc("PUT /products/{id}", forward(http.MethodPut, "/products/{id}", true))
	mux.HandleFunc("DELETE /products/{id}", forward(http.MethodDelete, "/products/{id}", false))
	mux.HandleFunc("POST /products/{id}/variants", forward(http.MethodPost, "/products/{id}/variants", true))
	mux.HandleFunc("PUT /variants/{id}", forward(http.MethodPut, "/variants/{id}", true))
	mux.HandleFunc("DELETE /variants/{id}", forward(http.MethodDelete, "/variants/{id}", false))
	mux.HandleFunc("PATCH /variants/{id}/stock", forward(http.MethodPatch, "/variants/{id}/stock", true))
}

// raw sends a request and returns any answer, for services whose success status varies.
func (a *admin) raw(ctx context.Context, method, target, bearer string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBytes))
	return resp.StatusCode, data, err
}

// readRawJSON reads a JSON object as it is, for a service that checks its fields itself.
func readRawJSON(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var object map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &object) != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return nil, false
	}
	return body, true
}
