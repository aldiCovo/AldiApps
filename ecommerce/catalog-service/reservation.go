package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	orderExchange = "order.events"
	orderQueue    = "catalog-service.orders"

	eventOrderCreated   = "order.created"
	eventOrderCancelled = "order.cancelled"
	eventStockReserved  = "stock.reserved"
	eventStockRejected  = "stock.rejected"

	reasonOutOfStock = "out_of_stock"

	reservationRetryDelay = 5 * time.Second
	reservationPrefetch   = 20
)

type ReservationItem struct {
	VariantID int64 `json:"variant_id"`
	Quantity  int32 `json:"quantity"`
}

type stockEvent struct {
	OrderID int64  `json:"order_id"`
	Reason  string `json:"reason,omitempty"`
}

// lockProductsOfVariants locks in product order, so two orders can't wait for each other.
func lockProductsOfVariants(ctx context.Context, tx pgx.Tx, variantIDs []int64) ([]int64, error) {
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT product_id FROM product_variants WHERE id = ANY($1) ORDER BY product_id`, variantIDs)
	if err != nil {
		return nil, err
	}
	products, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	for _, id := range products {
		if err := lockProduct(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return products, nil
}

// Reserve takes the stock for all of an order's items, or none of them.
func (s *Store) Reserve(ctx context.Context, orderID int64, items []ReservationItem) error {
	needed := make(map[int64]int64, len(items))
	variantIDs := make([]int64, 0, len(items))
	for _, item := range items {
		if needed[item.VariantID] == 0 {
			variantIDs = append(variantIDs, item.VariantID)
		}
		needed[item.VariantID] += int64(item.Quantity)
	}
	body, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO stock_reservations (order_id, status, items) VALUES ($1, 'reserved', $2)
			 ON CONFLICT (order_id) DO NOTHING`, orderID, body)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		products, err := lockProductsOfVariants(ctx, tx, variantIDs)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, stock FROM product_variants WHERE id = ANY($1)`, variantIDs)
		if err != nil {
			return err
		}
		available := make(map[int64]int64, len(variantIDs))
		var id, stock int64
		if _, err := pgx.ForEachRow(rows, []any{&id, &stock}, func() error {
			available[id] = stock
			return nil
		}); err != nil {
			return err
		}
		for variantID, quantity := range needed {
			if stock, found := available[variantID]; !found || stock < quantity {
				if _, err := tx.Exec(ctx,
					`UPDATE stock_reservations SET status = 'rejected', updated_at = now() WHERE order_id = $1`, orderID); err != nil {
					return err
				}
				return addEvent(ctx, tx, eventStockRejected, stockEvent{OrderID: orderID, Reason: reasonOutOfStock})
			}
		}
		for variantID, quantity := range needed {
			if _, err := tx.Exec(ctx,
				`UPDATE product_variants SET stock = stock - $2, updated_at = now() WHERE id = $1`, variantID, quantity); err != nil {
				return err
			}
		}
		for _, productID := range products {
			if err := addProductUpdated(ctx, tx, productID); err != nil {
				return err
			}
		}
		return addEvent(ctx, tx, eventStockReserved, stockEvent{OrderID: orderID})
	})
}

// Release returns a cancelled order's stock. Variants deleted since then have nothing to return to.
func (s *Store) Release(ctx context.Context, orderID int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var body []byte
		err := tx.QueryRow(ctx,
			`UPDATE stock_reservations SET status = 'released', updated_at = now()
			 WHERE order_id = $1 AND status = 'reserved' RETURNING items`, orderID).Scan(&body)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var items []ReservationItem
		if err := json.Unmarshal(body, &items); err != nil {
			return err
		}
		variantIDs := make([]int64, len(items))
		for i, item := range items {
			variantIDs[i] = item.VariantID
		}
		products, err := lockProductsOfVariants(ctx, tx, variantIDs)
		if err != nil {
			return err
		}
		for _, item := range items {
			if _, err := tx.Exec(ctx,
				`UPDATE product_variants SET stock = LEAST(stock + $2, $3), updated_at = now() WHERE id = $1`,
				item.VariantID, item.Quantity, maxStock); err != nil {
				return err
			}
		}
		for _, productID := range products {
			if err := addProductUpdated(ctx, tx, productID); err != nil {
				return err
			}
		}
		return nil
	})
}

// handleOrderEvent only returns errors that retrying can fix. A message that can't be read
// will never become readable, so it is logged and dropped instead of blocking the queue.
func (s *Store) handleOrderEvent(ctx context.Context, body []byte) error {
	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("dropping unreadable event", "error", err)
		return nil
	}
	var data struct {
		OrderID int64             `json:"order_id"`
		Items   []ReservationItem `json:"items"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil || data.OrderID <= 0 {
		slog.Error("dropping unreadable event", "event_id", event.ID, "type", event.Type, "error", err)
		return nil
	}
	switch event.Type {
	case eventOrderCreated:
		if len(data.Items) == 0 {
			slog.Error("dropping order without items", "event_id", event.ID, "order_id", data.OrderID)
			return nil
		}
		for _, item := range data.Items {
			if item.VariantID <= 0 || item.Quantity <= 0 {
				slog.Error("dropping order with a bad item", "event_id", event.ID, "order_id", data.OrderID)
				return nil
			}
		}
		return s.Reserve(ctx, data.OrderID, data.Items)
	case eventOrderCancelled:
		return s.Release(ctx, data.OrderID)
	}
	slog.Warn("ignoring unknown event type", "event_id", event.ID, "type", event.Type)
	return nil
}

// ReservationConsumer applies order events. A failure returns unacknowledged events to the
// durable queue, so they wait until the database can take them.
type ReservationConsumer struct {
	store *Store
	url   string
}

func (c *ReservationConsumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.connectAndConsume(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Error("reading order events stopped, retrying", "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(reservationRetryDelay):
		}
	}
}

func (c *ReservationConsumer) connectAndConsume(ctx context.Context) error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return fmt.Errorf("connecting to RabbitMQ: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Qos(reservationPrefetch, 0, false); err != nil {
		return err
	}
	if err := ch.ExchangeDeclare(orderExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(orderQueue, true, false, false, false, nil); err != nil {
		return err
	}
	for _, binding := range []string{eventOrderCreated, eventOrderCancelled} {
		if err := ch.QueueBind(orderQueue, binding, orderExchange, false, nil); err != nil {
			return err
		}
	}
	deliveries, err := ch.ConsumeWithContext(ctx, orderQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	slog.Info("reading order events", "queue", orderQueue)
	for delivery := range deliveries {
		if err := c.store.handleOrderEvent(ctx, delivery.Body); err != nil {
			return err
		}
		if err := delivery.Ack(false); err != nil {
			return err
		}
	}
	return errors.New("the event stream ended")
}
