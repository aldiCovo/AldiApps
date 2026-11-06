package main

import (
	"log/slog"
	"net/http"
)

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
