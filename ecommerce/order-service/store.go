package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	statusPending         = "pending"
	statusAwaitingPayment = "awaiting_payment"
	statusPaying          = "paying"
	statusPaid            = "paid"
	statusRejected        = "rejected"
	statusCancelled       = "cancelled"

	reasonPaymentFailed = "payment_failed"
	reasonExpired       = "expired"
	reasonByCustomer    = "cancelled_by_customer"

	// Open orders hold stock, so one account can't hold it all.
	maxOpenOrders = 5
	expireBatch   = 100
)

var (
	errNotFound      = errors.New("order not found")
	errTooManyOrders = errors.New("too many unfinished orders")
	errCannotPay     = errors.New("this order can't be paid now")
	errCannotCancel  = errors.New("this order can't be cancelled now")
)

type OrderItem struct {
	VariantID   int64  `json:"variant_id"`
	ProductID   int64  `json:"product_id"`
	ProductName string `json:"product_name"`
	Size        string `json:"size"`
	Color       string `json:"color"`
	Price       int64  `json:"price"`
	Quantity    int32  `json:"quantity"`
}

type Order struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Total  int64  `json:"total"`
	// Taken off by the voucher, already counted in Total. Zero without one.
	Discount    int64      `json:"discount"`
	VoucherCode string     `json:"voucher_code,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at"`
	// Nil only on orders made before addresses existed.
	ShippingAddress *Address `json:"shipping_address"`
	// Nil only on orders made before shipping was charged. Total includes its cost.
	Shipping  *Shipping   `json:"shipping"`
	Items     []OrderItem `json:"items"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	// Set once payment succeeds.
	PaidAt *time.Time `json:"paid_at"`
	// Set by the shop after payment, from the admin pages.
	PackedAt       *time.Time `json:"packed_at"`
	ShippedAt      *time.Time `json:"shipped_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	TrackingNumber string     `json:"tracking_number,omitempty"`
	// Set when the parcel reaches the customer. ArrivedBy is admin, or later courier.
	ArrivedAt *time.Time `json:"arrived_at"`
	ArrivedBy string     `json:"arrived_by,omitempty"`
	// When an arrived order completes by itself if the customer doesn't confirm it first.
	AutoCompleteAt *time.Time `json:"auto_complete_at,omitempty"`
	// customer, system or admin. Empty on orders completed before this was kept.
	CompletedBy string `json:"completed_by,omitempty"`
	// The customer's own reviews by product id, only on their completed orders.
	Reviews map[int64]MyReview `json:"reviews,omitempty"`
}

type reservationItem struct {
	VariantID int64 `json:"variant_id"`
	Quantity  int32 `json:"quantity"`
	// What the order charges, so catalog-service can count a Flash Sale's quota.
	Price int64 `json:"price"`
}

type orderCreatedEvent struct {
	OrderID int64             `json:"order_id"`
	Items   []reservationItem `json:"items"`
}

// orderFinishedEvent says how an order ended. It carries what an email needs, so
// notification-service doesn't need this database or another request.
type orderFinishedEvent struct {
	OrderID int64       `json:"order_id"`
	UserID  int64       `json:"user_id"`
	Reason  string      `json:"reason,omitempty"`
	Total   int64       `json:"total"`
	Items   []OrderItem `json:"items"`
	// Counted in Total already.
	Discount    int64  `json:"discount,omitempty"`
	VoucherCode string `json:"voucher_code,omitempty"`
	// Only on order.shipped, for the email.
	Shipping       *Shipping `json:"shipping,omitempty"`
	TrackingNumber string    `json:"tracking_number,omitempty"`
}

type paymentRequestedEvent struct {
	OrderID int64  `json:"order_id"`
	Amount  int64  `json:"amount"`
	Outcome string `json:"outcome"`
}

const orderColumns = "id, status, reason, total, discount, voucher_code, expires_at, shipping_address, shipping, created_at, updated_at, paid_at, packed_at, shipped_at, completed_at, tracking_number, arrived_at, arrived_by, completed_by"

// targets are where a row of orderColumns is scanned to.
func (o *Order) targets() []any {
	return []any{&o.ID, &o.Status, &o.Reason, &o.Total, &o.Discount, &o.VoucherCode, &o.ExpiresAt, &o.ShippingAddress, &o.Shipping, &o.CreatedAt, &o.UpdatedAt,
		&o.PaidAt, &o.PackedAt, &o.ShippedAt, &o.CompletedAt, &o.TrackingNumber, &o.ArrivedAt, &o.ArrivedBy, &o.CompletedBy}
}

// scanned fills in what follows from the columns.
func (o *Order) scanned() {
	if o.Status == statusArrived && o.ArrivedAt != nil {
		at := o.ArrivedAt.Add(receiveWindow)
		o.AutoCompleteAt = &at
	}
}

func scanOrder(row pgx.Row) (Order, error) {
	var o Order
	err := row.Scan(o.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, errNotFound
	}
	o.scanned()
	return o, err
}

type Store struct {
	pool          *pgxpool.Pool
	paymentWindow time.Duration
	// notify runs after a status change has committed. It may be nil.
	notify func(userID, orderID int64, status string)
}

func (s *Store) changed(userID, orderID int64, status string) {
	if s.notify != nil {
		s.notify(userID, orderID, status)
	}
}

// mark runs an update that returns the owner, and tells them only if a row changed. A
// repeated event changes nothing, so it sends no signal.
func (s *Store) mark(ctx context.Context, id int64, status, sql string, args ...any) error {
	var userID int64
	err := s.pool.QueryRow(ctx, sql, args...).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.changed(userID, id, status)
	return nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func attachItems(ctx context.Context, q querier, orders []Order) error {
	if len(orders) == 0 {
		return nil
	}
	ids := make([]int64, len(orders))
	byID := make(map[int64]*Order, len(orders))
	for i := range orders {
		ids[i] = orders[i].ID
		orders[i].Items = []OrderItem{}
		byID[orders[i].ID] = &orders[i]
	}
	rows, err := q.Query(ctx,
		`SELECT order_id, variant_id, product_id, product_name, size, color, price, quantity
		 FROM order_items WHERE order_id = ANY($1) ORDER BY order_id, variant_id`, ids)
	if err != nil {
		return err
	}
	var orderID int64
	var item OrderItem
	_, err = pgx.ForEachRow(rows, []any{&orderID, &item.VariantID, &item.ProductID, &item.ProductName, &item.Size, &item.Color, &item.Price, &item.Quantity}, func() error {
		byID[orderID].Items = append(byID[orderID].Items, item)
		return nil
	})
	return err
}

func subtotalOf(items []OrderItem) int64 {
	var subtotal int64
	for _, item := range items {
		subtotal += item.Price * int64(item.Quantity)
	}
	return subtotal
}

// Create saves the order and its event together, so the stock request can't get lost. A
// voucher code, if given, is checked and held in the same transaction.
func (s *Store) Create(ctx context.Context, userID int64, items []OrderItem, address Address, shipping Shipping, voucherCode string) (o Order, err error) {
	subtotal := subtotalOf(items)
	reserve := make([]reservationItem, len(items))
	for i, item := range items {
		reserve[i] = reservationItem{VariantID: item.VariantID, Quantity: item.Quantity, Price: item.Price}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One order at a time per user, or simultaneous requests could all pass the count.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, userID); err != nil {
			return err
		}
		var open int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM orders WHERE user_id = $1 AND status IN ('pending', 'awaiting_payment', 'paying')`,
			userID).Scan(&open); err != nil {
			return err
		}
		if open >= maxOpenOrders {
			return errTooManyOrders
		}
		var voucherID *int64
		var discount int64
		var code string
		if voucherCode != "" {
			voucher, d, err := applyVoucher(ctx, tx, true, userID, voucherCode, subtotal, shipping.Cost)
			if err != nil {
				return err
			}
			voucherID, discount, code = &voucher.ID, d, voucher.Code
		}
		o, err = scanOrder(tx.QueryRow(ctx,
			`INSERT INTO orders (user_id, status, total, shipping_address, shipping, voucher_id, voucher_code, discount)
			 VALUES ($1, 'pending', $2, $3, $4, $5, $6, $7) RETURNING `+orderColumns,
			userID, subtotal+shipping.Cost-discount, address, shipping, voucherID, code, discount))
		if err != nil {
			return err
		}
		for _, item := range items {
			if _, err := tx.Exec(ctx,
				`INSERT INTO order_items (order_id, variant_id, product_id, product_name, size, color, price, quantity)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				o.ID, item.VariantID, item.ProductID, item.ProductName, item.Size, item.Color, item.Price, item.Quantity); err != nil {
				return err
			}
		}
		o.Items = items
		return addEvent(ctx, tx, eventOrderCreated, orderCreatedEvent{OrderID: o.ID, Items: reserve})
	})
	if err != nil {
		return Order{}, err
	}
	s.changed(userID, o.ID, statusPending)
	return o, nil
}

// Get only finds the user's own orders, so another user's id looks like it doesn't exist.
func (s *Store) Get(ctx context.Context, userID, id int64) (Order, error) {
	o, err := scanOrder(s.pool.QueryRow(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE id = $1 AND user_id = $2`, id, userID))
	if err != nil {
		return Order{}, err
	}
	orders := []Order{o}
	if err := attachItems(ctx, s.pool, orders); err != nil {
		return Order{}, err
	}
	if err := attachMyReviews(ctx, s.pool, orders); err != nil {
		return Order{}, err
	}
	return orders[0], nil
}

func (s *Store) List(ctx context.Context, userID int64, limit, offset int) ([]Order, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE user_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, err
	}
	orders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Order, error) { return scanOrder(row) })
	if err != nil {
		return nil, err
	}
	if err := attachItems(ctx, s.pool, orders); err != nil {
		return nil, err
	}
	if err := attachMyReviews(ctx, s.pool, orders); err != nil {
		return nil, err
	}
	return orders, nil
}

// Pay moves the order out of reach of the expiry, so a payment can't succeed on an order
// that was cancelled while the payment was on its way.
func (s *Store) Pay(ctx context.Context, userID, id int64, outcome string) (o Order, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		o, err = scanOrder(tx.QueryRow(ctx,
			`UPDATE orders SET status = 'paying', expires_at = NULL, updated_at = now()
			 WHERE id = $1 AND user_id = $2 AND status = 'awaiting_payment' AND expires_at > now()
			 RETURNING `+orderColumns, id, userID))
		if errors.Is(err, errNotFound) {
			if _, err := s.Get(ctx, userID, id); err != nil {
				return err
			}
			return errCannotPay
		}
		if err != nil {
			return err
		}
		return addEvent(ctx, tx, eventPaymentRequested, paymentRequestedEvent{OrderID: o.ID, Amount: o.Total, Outcome: outcome})
	})
	if err != nil {
		return Order{}, err
	}
	s.changed(userID, o.ID, statusPaying)
	orders := []Order{o}
	if err := attachItems(ctx, s.pool, orders); err != nil {
		return Order{}, err
	}
	return orders[0], nil
}

func (s *Store) MarkReserved(ctx context.Context, id int64) error {
	return s.mark(ctx, id, statusAwaitingPayment,
		`UPDATE orders SET status = 'awaiting_payment', expires_at = now() + $2::interval, updated_at = now()
		 WHERE id = $1 AND status = 'pending' RETURNING user_id`, id, s.paymentWindow.String())
}

func (s *Store) MarkRejected(ctx context.Context, id int64, reason string) error {
	return s.finish(ctx, id, statusPending, statusRejected, reason, eventOrderRejected)
}

func (s *Store) MarkPaid(ctx context.Context, id int64) error {
	return s.finish(ctx, id, statusPaying, statusPaid, "", eventOrderPaid)
}

// addFinishedEvent saves how the order ended in the same transaction as the status.
func addFinishedEvent(ctx context.Context, tx pgx.Tx, eventType string, id, userID, total int64, reason string) error {
	orders := []Order{{ID: id}}
	if err := attachItems(ctx, tx, orders); err != nil {
		return err
	}
	event := orderFinishedEvent{OrderID: id, UserID: userID, Reason: reason, Total: total, Items: orders[0].Items}
	if err := tx.QueryRow(ctx, `SELECT discount, voucher_code FROM orders WHERE id = $1`, id).Scan(&event.Discount, &event.VoucherCode); err != nil {
		return err
	}
	return addEvent(ctx, tx, eventType, event)
}

// finish applies a final status only from the status before it, so a repeated event changes
// nothing and sends nothing.
func (s *Store) finish(ctx context.Context, id int64, from, to, reason, eventType string) error {
	var userID, total int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`UPDATE orders SET status = $3, reason = $4, updated_at = now(),
				paid_at = CASE WHEN $3 = 'paid' THEN now() END
			 WHERE id = $1 AND status = $2 RETURNING user_id, total`,
			id, from, to, reason).Scan(&userID, &total)
		if err != nil {
			return err
		}
		return addFinishedEvent(ctx, tx, eventType, id, userID, total, reason)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.changed(userID, id, to)
	return nil
}

// Cancel lets the customer drop an order that is waiting for payment. Its stock goes back on
// sale through the same event as an expiry. An order already being paid, or still having its
// stock checked, can't be cancelled: the row's status decides, so cancelling and paying at
// once can't both succeed.
func (s *Store) Cancel(ctx context.Context, userID, id int64) (o Order, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		o, err = scanOrder(tx.QueryRow(ctx,
			`UPDATE orders SET status = 'cancelled', reason = $3, expires_at = NULL, updated_at = now()
			 WHERE id = $1 AND user_id = $2 AND status = 'awaiting_payment'
			 RETURNING `+orderColumns, id, userID, reasonByCustomer))
		if errors.Is(err, errNotFound) {
			if _, err := s.Get(ctx, userID, id); err != nil {
				return err
			}
			return errCannotCancel
		}
		if err != nil {
			return err
		}
		return addFinishedEvent(ctx, tx, eventOrderCancelled, o.ID, userID, o.Total, reasonByCustomer)
	})
	if err != nil {
		return Order{}, err
	}
	s.changed(userID, o.ID, statusCancelled)
	orders := []Order{o}
	if err := attachItems(ctx, s.pool, orders); err != nil {
		return Order{}, err
	}
	return orders[0], nil
}

// CancelPaying is the saga's compensation: the failed payment's stock goes back on sale.
func (s *Store) CancelPaying(ctx context.Context, id int64) error {
	return s.finish(ctx, id, statusPaying, statusCancelled, reasonPaymentFailed, eventOrderCancelled)
}

type expiredOrder struct {
	ID, UserID, Total int64
}

// ExpireUnpaid cancels orders whose payment window has passed, so they stop holding stock.
func (s *Store) ExpireUnpaid(ctx context.Context) (int, error) {
	var expired []expiredOrder
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE orders SET status = 'cancelled', reason = $1, expires_at = NULL, updated_at = now()
			 WHERE id IN (
			     SELECT id FROM orders WHERE status = 'awaiting_payment' AND expires_at <= now()
			     ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED
			 ) RETURNING id, user_id, total`, reasonExpired, expireBatch)
		if err != nil {
			return err
		}
		expired, err = pgx.CollectRows(rows, pgx.RowToStructByPos[expiredOrder])
		if err != nil {
			return err
		}
		for _, order := range expired {
			if err := addFinishedEvent(ctx, tx, eventOrderCancelled, order.ID, order.UserID, order.Total, reasonExpired); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, order := range expired {
		s.changed(order.UserID, order.ID, statusCancelled)
	}
	return len(expired), nil
}

// RunExpiry checks for unpaid orders until ctx is cancelled.
func (s *Store) RunExpiry(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired, err := s.ExpireUnpaid(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("expiring orders failed", "error", err)
			}
			if expired > 0 {
				slog.Info("expired unpaid orders", "orders", expired)
			}
		}
	}
}

// RunAutoComplete completes arrived orders the customer hasn't confirmed in time, until ctx
// is cancelled.
func (s *Store) RunAutoComplete(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			done, err := s.CompleteArrived(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("completing arrived orders failed", "error", err)
			}
			if done > 0 {
				slog.Info("completed arrived orders", "orders", done)
			}
		}
	}
}
