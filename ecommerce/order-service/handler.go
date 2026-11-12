package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxBodyBytes  = 1 << 16
	maxItems      = 20
	maxQuantity   = 20
	defaultLimit  = 20
	maxLimit      = 100
	outcomeOK     = "success"
	outcomeFailed = "failure"
)

type orderInput struct {
	Items []struct {
		VariantID int64 `json:"variant_id"`
		Quantity  int32 `json:"quantity"`
	} `json:"items"`
	ShippingAddress *Address `json:"shipping_address"`
	ShippingOption  string   `json:"shipping_option"`
	// Optional.
	VoucherCode string `json:"voucher_code"`
}

func (in *orderInput) validate() error {
	if in.ShippingAddress == nil {
		return errors.New("shipping_address is required")
	}
	if err := in.ShippingAddress.validate(); err != nil {
		return fmt.Errorf("shipping_address: %w", err)
	}
	return in.validateItems()
}

func (in *orderInput) validateItems() error {
	if len(in.Items) == 0 {
		return errors.New("items is required")
	}
	if len(in.Items) > maxItems {
		return fmt.Errorf("an order can have at most %d items", maxItems)
	}
	seen := make(map[int64]bool, len(in.Items))
	for _, item := range in.Items {
		switch {
		case item.VariantID <= 0:
			return errors.New("variant_id must be a positive integer")
		case item.Quantity < 1 || item.Quantity > maxQuantity:
			return fmt.Errorf("quantity must be between 1 and %d", maxQuantity)
		case seen[item.VariantID]:
			return errors.New("each variant can only appear once")
		}
		seen[item.VariantID] = true
	}
	return nil
}

func newHandler(store *Store, verifier *Verifier, catalog *Catalog, internalToken string) http.Handler {
	mux := http.NewServeMux()
	addInternalRoutes(mux, store, internalToken)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.pool.Ping(ctx); err != nil {
			slog.Error("database not ready", "error", err)
			writeError(w, http.StatusServiceUnavailable, "database not ready")
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	authenticated := func(next func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !found {
				writeUnauthorized(w, "a bearer token is required")
				return
			}
			userID, err := verifier.UserID(r.Context(), token)
			if errors.Is(err, errKeysUnavailable) {
				slog.Error("checking a token failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "login can't be checked now")
				return
			}
			if err != nil {
				writeUnauthorized(w, "the token is not valid")
				return
			}
			next(w, r, userID)
		}
	}

	// The same for everyone and rarely changed, so browsers and Cloudflare may keep it a while.
	mux.HandleFunc("GET /shipping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, http.StatusOK, shippingTable())
	})

	// The voucher the shop shows above its header, the same for everyone. Only what a
	// customer needs to use it: how many are left stays with the admins.
	mux.HandleFunc("GET /vouchers/banner", func(w http.ResponseWriter, r *http.Request) {
		v, err := store.BannerVoucher(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		if v == nil {
			writeJSON(w, http.StatusOK, map[string]any{"voucher": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"voucher": map[string]any{
			"code": v.Code, "description": v.Description, "kind": v.Kind, "value": v.Value,
			"max_discount": v.MaxDiscount, "min_subtotal": v.MinSubtotal, "ends_at": v.EndsAt,
		}})
	})

	mux.HandleFunc("POST /orders", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		var in orderInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		shipping, err := quote(in.ShippingOption, in.ShippingAddress.Province)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		items, ok := readItems(w, r, catalog, &in)
		if !ok {
			return
		}
		order, err := store.Create(r.Context(), userID, items, *in.ShippingAddress, shipping, in.VoucherCode)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, order)
	}))

	// The vouchers this customer can see on their account page.
	mux.HandleFunc("GET /vouchers/mine", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		list, err := store.MyVouchers(r.Context(), userID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"vouchers": list})
	}))

	// What a voucher would take off this checkout. The items are priced from the catalog, as
	// the order will be, and the province and courier price the shipping.
	mux.HandleFunc("POST /vouchers/check", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		var in struct {
			orderInput
			Province string `json:"province"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.VoucherCode) == "" {
			writeError(w, http.StatusBadRequest, "voucher_code is required")
			return
		}
		if err := in.validateItems(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Without a courier yet, free shipping is worth nothing so far.
		var shippingCost int64
		if in.ShippingOption != "" {
			shipping, err := quote(in.ShippingOption, in.Province)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			shippingCost = shipping.Cost
		}
		items, ok := readItems(w, r, catalog, &in.orderInput)
		if !ok {
			return
		}
		voucher, discount, err := store.CheckVoucher(r.Context(), userID, in.VoucherCode, subtotalOf(items), shippingCost)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"code": voucher.Code, "description": voucher.Description, "kind": voucher.Kind, "value": voucher.Value,
			"max_discount": voucher.MaxDiscount, "min_subtotal": voucher.MinSubtotal, "discount": discount,
		})
	}))

	mux.HandleFunc("GET /orders", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		limit, offset := defaultLimit, 0
		var err error
		if value := r.URL.Query().Get("limit"); value != "" {
			if limit, err = strconv.Atoi(value); err != nil || limit < 1 || limit > maxLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxLimit))
				return
			}
		}
		if value := r.URL.Query().Get("offset"); value != "" {
			if offset, err = strconv.Atoi(value); err != nil || offset < 0 {
				writeError(w, http.StatusBadRequest, "offset must not be negative")
				return
			}
		}
		orders, err := store.List(r.Context(), userID, limit, offset)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"orders": orders})
	}))

	orderID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errNotFound.Error())
			return 0, false
		}
		return id, true
	}

	mux.HandleFunc("GET /orders/{id}", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		id, ok := orderID(w, r)
		if !ok {
			return
		}
		order, err := store.Get(r.Context(), userID, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))

	mux.HandleFunc("POST /orders/{id}/pay", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		id, ok := orderID(w, r)
		if !ok {
			return
		}
		var in struct {
			Simulate string `json:"simulate"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.Simulate != outcomeOK && in.Simulate != outcomeFailed {
			writeError(w, http.StatusBadRequest, "simulate must be success or failure")
			return
		}
		order, err := store.Pay(r.Context(), userID, id, in.Simulate)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, order)
	}))

	mux.HandleFunc("POST /orders/{id}/cancel", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		id, ok := orderID(w, r)
		if !ok {
			return
		}
		order, err := store.Cancel(r.Context(), userID, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))

	// The customer confirms the parcel reached them.
	mux.HandleFunc("POST /orders/{id}/receive", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		id, ok := orderID(w, r)
		if !ok {
			return
		}
		order, err := store.Receive(r.Context(), userID, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))

	mux.HandleFunc("PUT /orders/{id}/reviews/{product_id}", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		id, ok := orderID(w, r)
		if !ok {
			return
		}
		productID, err := strconv.ParseInt(r.PathValue("product_id"), 10, 64)
		if err != nil || productID <= 0 {
			writeError(w, http.StatusNotFound, errNotInOrder.Error())
			return
		}
		var in struct {
			Rating int16  `json:"rating"`
			Body   string `json:"body"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		review, err := store.SaveReview(r.Context(), userID, id, productID, in.Rating, in.Body)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, review)
	}))

	// Reviews are public, and a short cache keeps a busy product page cheap.
	mux.HandleFunc("GET /products/{id}/reviews", func(w http.ResponseWriter, r *http.Request) {
		productID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || productID <= 0 {
			writeError(w, http.StatusNotFound, "product not found")
			return
		}
		query := r.URL.Query()
		stars, limit, offset := 0, defaultReviewPage, 0
		if value := query.Get("rating"); value != "" {
			if stars, err = strconv.Atoi(value); err != nil || stars < 1 || stars > 5 {
				writeError(w, http.StatusBadRequest, errBadRating.Error())
				return
			}
		}
		if value := query.Get("limit"); value != "" {
			if limit, err = strconv.Atoi(value); err != nil || limit < 1 || limit > maxReviewPage {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxReviewPage))
				return
			}
		}
		if value := query.Get("offset"); value != "" {
			if offset, err = strconv.Atoi(value); err != nil || offset < 0 || offset > 10000 {
				writeError(w, http.StatusBadRequest, "offset must be between 0 and 10000")
				return
			}
		}
		page, err := store.ProductReviews(r.Context(), productID, stars, limit, offset)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=30")
		writeJSON(w, http.StatusOK, page)
	})

	mux.HandleFunc("GET /ratings", func(w http.ResponseWriter, r *http.Request) {
		ratings, err := store.Ratings(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		writeJSON(w, http.StatusOK, map[string]any{"ratings": ratings})
	})

	return mux
}

// readItems prices the order's items from the catalog. It answers the request itself when
// that fails.
func readItems(w http.ResponseWriter, r *http.Request, catalog *Catalog, in *orderInput) ([]OrderItem, bool) {
	items := make([]OrderItem, len(in.Items))
	for i, item := range in.Items {
		var err error
		if items[i], err = catalog.Item(r.Context(), item.VariantID, item.Quantity); err != nil {
			if errors.Is(err, errUnknownVariant) {
				writeError(w, http.StatusBadRequest, err.Error())
				return nil, false
			}
			slog.Error("reading the catalog failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "the catalog is not available")
			return nil, false
		}
	}
	return items, true
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeStoreError(w http.ResponseWriter, err error) {
	var refusal *voucherRefusal
	switch {
	case errors.As(err, &refusal):
		writeVoucherRefusal(w, refusal)
	case errors.Is(err, errNotFound), errors.Is(err, errVoucherNotFound), errors.Is(err, errNotInOrder):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errBadStep), errors.Is(err, errBadTracking), errors.Is(err, errBadRating), errors.Is(err, errLongReview):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errCannotPay), errors.Is(err, errCannotCancel), errors.Is(err, errTooManyOrders), errors.Is(err, errCannotAdvance),
		errors.Is(err, errVoucherCodeTaken), errors.Is(err, errVoucherInUse), errors.Is(err, errCannotReceive), errors.Is(err, errNotReviewable):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("database request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, message)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}
