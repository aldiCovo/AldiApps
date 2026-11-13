package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// After payment the shop packs, ships and completes an order. Only admin-service sets these,
// through the /internal routes, which need the internal token.
const (
	statusPacked    = "packed"
	statusShipped   = "shipped"
	statusArrived   = "arrived"
	statusCompleted = "completed"

	eventOrderShipped   = "order.shipped"
	eventOrderArrived   = "order.arrived"
	eventOrderCompleted = "order.completed"

	completedByAdmin    = "admin"
	completedByCustomer = "customer"
	completedBySystem   = "system"

	// How long the customer has to confirm an arrived order before it completes by itself.
	receiveWindow = 72 * time.Hour

	minInternalTokenLength = 32
)

var (
	errCannotAdvance = errors.New("the order isn't in the step before this one")
	errBadStep       = errors.New("status must be packed, shipped, arrived or completed")
	errCannotReceive = errors.New("this order hasn't arrived yet")
	errBadTracking   = errors.New("tracking_number must be 6 to 40 letters, digits or dashes, and only given when shipping")

	// Each step can only follow the one before it, so two admins can't skip or repeat one.
	previousStep = map[string]string{statusPacked: statusPaid, statusShipped: statusPacked, statusArrived: statusShipped, statusCompleted: statusArrived}
	// Steps that send the customer an email.
	stepEvents = map[string]string{statusShipped: eventOrderShipped, statusArrived: eventOrderArrived, statusCompleted: eventOrderCompleted}

	trackingNumber = regexp.MustCompile(`^[A-Z0-9-]{6,40}$`)

	allStatuses = map[string]bool{
		statusPending: true, statusAwaitingPayment: true, statusPaying: true, statusPaid: true,
		statusPacked: true, statusShipped: true, statusArrived: true, statusCompleted: true, statusRejected: true, statusCancelled: true,
	}
)

// AdminOrder is an order with its owner, which only the admin pages see.
type AdminOrder struct {
	Order
	UserID int64 `json:"user_id"`
}

const adminColumns = "user_id, " + orderColumns

func scanAdminOrder(row pgx.Row) (AdminOrder, error) {
	var a AdminOrder
	err := row.Scan(append([]any{&a.UserID}, a.targets()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminOrder{}, errNotFound
	}
	a.scanned()
	return a, err
}

func withItems(ctx context.Context, q querier, list []AdminOrder) error {
	orders := make([]Order, len(list))
	for i := range list {
		orders[i] = list[i].Order
	}
	if err := attachItems(ctx, q, orders); err != nil {
		return err
	}
	for i := range list {
		list[i].Items = orders[i].Items
	}
	return nil
}

// AdminList lists every customer's orders, newest first, optionally only with one status
// or of one customer (userID 0 for everyone).
func (s *Store) AdminList(ctx context.Context, status string, userID int64, limit, offset int) ([]AdminOrder, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+adminColumns+` FROM orders WHERE ($1 = '' OR status = $1) AND ($4 = 0 OR user_id = $4)
		 ORDER BY id DESC LIMIT $2 OFFSET $3`,
		status, limit, offset, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AdminOrder, error) { return scanAdminOrder(row) })
	if err != nil {
		return nil, err
	}
	if err := withItems(ctx, s.pool, list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *Store) AdminGet(ctx context.Context, id int64) (AdminOrder, error) {
	a, err := scanAdminOrder(s.pool.QueryRow(ctx, `SELECT `+adminColumns+` FROM orders WHERE id = $1`, id))
	if err != nil {
		return AdminOrder{}, err
	}
	list := []AdminOrder{a}
	if err := withItems(ctx, s.pool, list); err != nil {
		return AdminOrder{}, err
	}
	return list[0], nil
}

// Advance moves a paid order one step on. The row's status decides, so the same step sent
// twice, or two admins at once, changes the order only once.
func (s *Store) Advance(ctx context.Context, id int64, to, tracking string) (a AdminOrder, err error) {
	from, ok := previousStep[to]
	if !ok {
		return AdminOrder{}, errBadStep
	}
	tracking = strings.ToUpper(strings.TrimSpace(tracking))
	if (to == statusShipped) != (tracking != "") || (tracking != "" && !trackingNumber.MatchString(tracking)) {
		return AdminOrder{}, errBadTracking
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		a, err = scanAdminOrder(tx.QueryRow(ctx,
			`UPDATE orders SET status = $2, updated_at = now(),
				packed_at = CASE WHEN $2 = 'packed' THEN now() ELSE packed_at END,
				shipped_at = CASE WHEN $2 = 'shipped' THEN now() ELSE shipped_at END,
				arrived_at = CASE WHEN $2 = 'arrived' THEN now() ELSE arrived_at END,
				arrived_by = CASE WHEN $2 = 'arrived' THEN 'admin' ELSE arrived_by END,
				completed_at = CASE WHEN $2 = 'completed' THEN now() ELSE completed_at END,
				completed_by = CASE WHEN $2 = 'completed' THEN 'admin' ELSE completed_by END,
				tracking_number = CASE WHEN $2 = 'shipped' THEN $4 ELSE tracking_number END
			 WHERE id = $1 AND status = $3
			 RETURNING `+adminColumns, id, to, from, tracking))
		if errors.Is(err, errNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1)`, id).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return errCannotAdvance
			}
			return errNotFound
		}
		if err != nil {
			return err
		}
		list := []AdminOrder{a}
		if err := withItems(ctx, tx, list); err != nil {
			return err
		}
		a = list[0]
		return addStepEvent(ctx, tx, a)
	})
	if err != nil {
		return AdminOrder{}, err
	}
	s.changed(a.UserID, a.ID, to)
	return a, nil
}

// addStepEvent saves the email for the step the order has just reached, if it has one. The
// order needs its items.
func addStepEvent(ctx context.Context, tx pgx.Tx, a AdminOrder) error {
	eventType, found := stepEvents[a.Status]
	if !found {
		return nil
	}
	event := orderFinishedEvent{OrderID: a.ID, UserID: a.UserID, Total: a.Total, Items: a.Items, Discount: a.Discount, VoucherCode: a.VoucherCode}
	if a.Status == statusShipped {
		event.Shipping, event.TrackingNumber = a.Shipping, a.TrackingNumber
	}
	return addEvent(ctx, tx, eventType, event)
}

// Receive is the customer confirming that an arrived order reached them.
func (s *Store) Receive(ctx context.Context, userID, id int64) (Order, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := scanAdminOrder(tx.QueryRow(ctx,
			`UPDATE orders SET status = 'completed', completed_at = now(), completed_by = $3, updated_at = now()
			 WHERE id = $1 AND user_id = $2 AND status = 'arrived'
			 RETURNING `+adminColumns, id, userID, completedByCustomer))
		if errors.Is(err, errNotFound) {
			if _, err := s.Get(ctx, userID, id); err != nil {
				return err
			}
			return errCannotReceive
		}
		if err != nil {
			return err
		}
		list := []AdminOrder{a}
		if err := withItems(ctx, tx, list); err != nil {
			return err
		}
		return addStepEvent(ctx, tx, list[0])
	})
	if err != nil {
		return Order{}, err
	}
	s.changed(userID, id, statusCompleted)
	return s.Get(ctx, userID, id)
}

// CompleteArrived completes the orders whose customer hasn't confirmed them in time.
func (s *Store) CompleteArrived(ctx context.Context) (int, error) {
	var done []AdminOrder
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE orders SET status = 'completed', completed_at = now(), completed_by = $1, updated_at = now()
			 WHERE id IN (
			     SELECT id FROM orders WHERE status = 'arrived' AND arrived_at <= now() - $2::interval
			     ORDER BY arrived_at LIMIT $3 FOR UPDATE SKIP LOCKED
			 ) RETURNING `+adminColumns, completedBySystem, receiveWindow.String(), expireBatch)
		if err != nil {
			return err
		}
		done, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AdminOrder, error) { return scanAdminOrder(row) })
		if err != nil {
			return err
		}
		if err := withItems(ctx, tx, done); err != nil {
			return err
		}
		for _, a := range done {
			if err := addStepEvent(ctx, tx, a); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, a := range done {
		s.changed(a.UserID, a.ID, statusCompleted)
	}
	return len(done), nil
}

// addInternalRoutes adds what admin-service calls. Browsers can't reach them: web never
// passes /internal, and each request needs the internal token.
func addInternalRoutes(mux *http.ServeMux, store *Store, internalToken string) {
	internal := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(bearer), []byte(internalToken)) != 1 {
				writeUnauthorized(w, "the internal token is required")
				return
			}
			next(w, r)
		}
	}
	pathID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errNotFound.Error())
			return 0, false
		}
		return id, true
	}

	voucherID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errVoucherNotFound.Error())
			return 0, false
		}
		return id, true
	}

	mux.HandleFunc("GET /internal/orders", internal(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		status := query.Get("status")
		if status != "" && !allStatuses[status] {
			writeError(w, http.StatusBadRequest, "unknown status")
			return
		}
		limit, offset := defaultLimit, 0
		var err error
		if value := query.Get("limit"); value != "" {
			if limit, err = strconv.Atoi(value); err != nil || limit < 1 || limit > maxLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxLimit))
				return
			}
		}
		if value := query.Get("offset"); value != "" {
			if offset, err = strconv.Atoi(value); err != nil || offset < 0 {
				writeError(w, http.StatusBadRequest, "offset must not be negative")
				return
			}
		}
		var userID int64
		if value := query.Get("user_id"); value != "" {
			if userID, err = strconv.ParseInt(value, 10, 64); err != nil || userID < 1 {
				writeError(w, http.StatusBadRequest, "user_id must be a positive integer")
				return
			}
		}
		list, err := store.AdminList(r.Context(), status, userID, limit, offset)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"orders": list})
	}))

	mux.HandleFunc("GET /internal/orders/{id}", internal(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		order, err := store.AdminGet(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))

	mux.HandleFunc("GET /internal/vouchers", internal(func(w http.ResponseWriter, r *http.Request) {
		list, err := store.ListVouchers(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"vouchers": list})
	}))

	mux.HandleFunc("POST /internal/vouchers", internal(func(w http.ResponseWriter, r *http.Request) {
		var in voucherInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		voucher, err := store.CreateVoucher(r.Context(), in)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, voucher)
	}))

	mux.HandleFunc("PUT /internal/vouchers/{id}", internal(func(w http.ResponseWriter, r *http.Request) {
		id, ok := voucherID(w, r)
		if !ok {
			return
		}
		var in voucherInput
		if !readJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		voucher, err := store.UpdateVoucher(r.Context(), id, in)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, voucher)
	}))

	mux.HandleFunc("DELETE /internal/vouchers/{id}", internal(func(w http.ResponseWriter, r *http.Request) {
		id, ok := voucherID(w, r)
		if !ok {
			return
		}
		if err := store.DeleteVoucher(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("GET /internal/signup-popup", internal(func(w http.ResponseWriter, r *http.Request) {
		hours, err := store.SignupRestHours(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rest_hours": hours})
	}))

	mux.HandleFunc("PUT /internal/signup-popup", internal(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			RestHours int `json:"rest_hours"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.RestHours < minSignupRestHours || in.RestHours > maxSignupRestHours {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("rest_hours must be between %d and %d", minSignupRestHours, maxSignupRestHours))
			return
		}
		if err := store.SaveSignupRestHours(r.Context(), in.RestHours); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rest_hours": in.RestHours})
	}))

	mux.HandleFunc("POST /internal/orders/{id}/status", internal(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in struct {
			Status         string `json:"status"`
			TrackingNumber string `json:"tracking_number"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		order, err := store.Advance(r.Context(), id, in.Status, in.TrackingNumber)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	}))
}
