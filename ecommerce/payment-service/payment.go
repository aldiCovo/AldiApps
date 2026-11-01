package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	orderExchange = "order.events"
	eventQueue    = "payment-service.orders"

	eventPaymentRequested = "payment.requested"
	eventPaymentSucceeded = "payment.succeeded"
	eventPaymentFailed    = "payment.failed"

	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	outcomeSuccess  = "success"
	outcomeFailure  = "failure"
	reasonDeclined  = "declined"

	consumerRetryDelay = 5 * time.Second
	consumerPrefetch   = 20
)

type Store struct {
	pool *pgxpool.Pool
}

type paymentEvent struct {
	OrderID int64  `json:"order_id"`
	Reason  string `json:"reason,omitempty"`
}

// Process simulates a payment provider: the request says how the payment ends, and no money
// moves. The unique order_id means a repeated request charges and answers only once.
func (s *Store) Process(ctx context.Context, orderID, amount int64, outcome string) error {
	status, eventType, reason := statusSucceeded, eventPaymentSucceeded, ""
	if outcome != outcomeSuccess {
		status, eventType, reason = statusFailed, eventPaymentFailed, reasonDeclined
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO payments (order_id, amount, status) VALUES ($1, $2, $3) ON CONFLICT (order_id) DO NOTHING`,
			orderID, amount, status)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return addEvent(ctx, tx, eventType, paymentEvent{OrderID: orderID, Reason: reason})
	})
}

// handleEvent only returns errors that retrying can fix. A message that can't be read will
// never become readable, so it is logged and dropped instead of blocking the queue.
func (s *Store) handleEvent(ctx context.Context, body []byte) error {
	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("dropping unreadable event", "error", err)
		return nil
	}
	if event.Type != eventPaymentRequested {
		slog.Warn("ignoring unknown event type", "event_id", event.ID, "type", event.Type)
		return nil
	}
	var data struct {
		OrderID int64  `json:"order_id"`
		Amount  int64  `json:"amount"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil || data.OrderID <= 0 || data.Amount < 0 ||
		(data.Outcome != outcomeSuccess && data.Outcome != outcomeFailure) {
		slog.Error("dropping unreadable event", "event_id", event.ID, "error", err)
		return nil
	}
	return s.Process(ctx, data.OrderID, data.Amount, data.Outcome)
}

// Consumer applies events. A failure returns unacknowledged events to the durable queue.
type Consumer struct {
	store *Store
	url   string
}

func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.connectAndConsume(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Error("reading events stopped, retrying", "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(consumerRetryDelay):
		}
	}
}

func (c *Consumer) connectAndConsume(ctx context.Context) error {
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
	if err := ch.Qos(consumerPrefetch, 0, false); err != nil {
		return err
	}
	if err := ch.ExchangeDeclare(orderExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(eventQueue, true, false, false, false, nil); err != nil {
		return err
	}
	if err := ch.QueueBind(eventQueue, eventPaymentRequested, orderExchange, false, nil); err != nil {
		return err
	}
	deliveries, err := ch.ConsumeWithContext(ctx, eventQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	slog.Info("reading events", "queue", eventQueue)
	for delivery := range deliveries {
		if err := c.store.handleEvent(ctx, delivery.Body); err != nil {
			return err
		}
		if err := delivery.Ack(false); err != nil {
			return err
		}
	}
	return errors.New("the event stream ended")
}
