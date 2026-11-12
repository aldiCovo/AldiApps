package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

var errNoCustomer = errors.New("no customer has this email")

// customerID finds the account with exactly this email.
func (a *admin) customerID(ctx context.Context, email string) (int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return 0, errNoCustomer
	}
	var page struct {
		Users []User `json:"users"`
	}
	query := url.Values{"q": {email}, "limit": {"100"}}
	if err := a.call(ctx, http.MethodGet, a.userURL+"/internal/users?"+query.Encode(), a.userAdminToken, nil, &page); err != nil {
		return 0, err
	}
	for _, user := range page.Users {
		if strings.EqualFold(user.Email, email) {
			return user.ID, nil
		}
	}
	return 0, errNoCustomer
}

// voucherBody turns the admin page's customer_email into the user_id order-service keeps.
// It answers a message for the admin when the email has no account.
func (a *admin) voucherBody(ctx context.Context, body []byte) ([]byte, string, error) {
	var voucher map[string]json.RawMessage
	if err := json.Unmarshal(body, &voucher); err != nil {
		return nil, "invalid JSON", nil
	}
	var audience, email string
	json.Unmarshal(voucher["audience"], &audience)
	json.Unmarshal(voucher["customer_email"], &email)
	delete(voucher, "customer_email")
	delete(voucher, "user_id")
	if audience == "user" {
		id, err := a.customerID(ctx, email)
		if errors.Is(err, errNoCustomer) {
			return nil, errNoCustomer.Error(), nil
		}
		if err != nil {
			return nil, "", err
		}
		voucher["user_id"], _ = json.Marshal(id)
	}
	out, err := json.Marshal(voucher)
	return out, "", err
}

// withCustomers puts the customer's name and email next to each voucher meant for one customer.
func (a *admin) withCustomers(ctx context.Context, data []byte, list bool) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if list {
		var answer map[string][]map[string]any
		if err := decoder.Decode(&answer); err != nil {
			return nil, err
		}
		if err := a.addCustomers(ctx, answer["vouchers"]); err != nil {
			return nil, err
		}
		return json.Marshal(answer)
	}
	var voucher map[string]any
	if err := decoder.Decode(&voucher); err != nil {
		return nil, err
	}
	if err := a.addCustomers(ctx, []map[string]any{voucher}); err != nil {
		return nil, err
	}
	return json.Marshal(voucher)
}

// addVoucherRoutes lists, makes, changes and deletes vouchers through order-service, which
// checks them and keeps them. Each change is logged with the admin who made it.
func (a *admin) addVoucherRoutes(mux *http.ServeMux) {
	forward := func(method string, withID, withBody bool) http.HandlerFunc {
		return a.onlyAdmins(func(w http.ResponseWriter, r *http.Request, me User) {
			target := a.orderURL + "/internal/vouchers"
			if withID {
				id := r.PathValue("id")
				if !numericID.MatchString(id) {
					writeError(w, http.StatusNotFound, "voucher not found")
					return
				}
				target += "/" + id
			}
			var body []byte
			if withBody {
				var ok bool
				if body, ok = readRawJSON(w, r); !ok {
					return
				}
				changed, problem, err := a.voucherBody(r.Context(), body)
				if err != nil {
					a.failInternal(w, err)
					return
				}
				if problem != "" {
					writeError(w, http.StatusBadRequest, problem)
					return
				}
				body = changed
			}
			status, data, err := a.raw(r.Context(), method, target, a.orderInternalToken, body)
			if err != nil {
				a.fail(w, err)
				return
			}
			if method != http.MethodGet {
				slog.Info("vouchers changed", "admin_id", me.ID, "method", method, "voucher", r.PathValue("id"), "status", status)
			}
			switch status {
			case http.StatusNoContent:
				w.WriteHeader(status)
			case http.StatusOK, http.StatusCreated:
				if data, err = a.withCustomers(r.Context(), data, method == http.MethodGet); err != nil {
					a.failInternal(w, err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				w.Write(data)
			default:
				// A wrong internal token is our fault, not the admin's, and fail hides it.
				if status == http.StatusUnauthorized {
					status = http.StatusBadGateway
				}
				a.fail(w, &upstreamError{status: status, body: data})
			}
		})
	}
	mux.HandleFunc("GET /vouchers", forward(http.MethodGet, false, false))
	mux.HandleFunc("POST /vouchers", forward(http.MethodPost, false, true))
	mux.HandleFunc("PUT /vouchers/{id}", forward(http.MethodPut, true, true))
	mux.HandleFunc("DELETE /vouchers/{id}", forward(http.MethodDelete, true, false))
}
