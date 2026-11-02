package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	eventQueue      = "order-service.events"
	catalogExchange = "catalog.events"
	paymentExchange = "payment.events"

	eventOrderCreated     = "order.created"
	eventOrderCancelled   = "order.cancelled"
	eventOrderPaid        = "order.paid"
	eventOrderRejected    = "order.rejected"
	eventPaymentRequested = "payment.requested"
	eventStockReserved    = "stock.reserved"
	eventStockRejected    = "stock.rejected"
	eventPaymentSucceeded = "payment.succeeded"
	eventPaymentFailed    = "payment.failed"

	consumerRetryDelay = 5 * time.Second
	consumerPrefetch   = 20
)

// handleEvent moves an order along. Each step only applies from the status before it, so a
// repeated event changes nothing. It only returns errors that retrying can fix.
func (s *Store) handleEvent(ctx context.Context, body []byte) error {
	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("dropping unreadable event", "error", err)
		return nil
	}
	var data struct {
		OrderID int64  `json:"order_id"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil || data.OrderID <= 0 {
		slog.Error("dropping unreadable event", "event_id", event.ID, "type", event.Type, "error", err)
		return nil
	}
	switch event.Type {
	case eventStockReserved:
		return s.MarkReserved(ctx, data.OrderID)
	case eventStockRejected:
		return s.MarkRejected(ctx, data.OrderID, data.Reason)
	case eventPaymentSucceeded:
		return s.MarkPaid(ctx, data.OrderID)
	case eventPaymentFailed:
		return s.CancelPaying(ctx, data.OrderID)
	}
	slog.Warn("ignoring unknown event type", "event_id", event.ID, "type", event.Type)
	return nil
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
	if _, err := ch.QueueDeclare(eventQueue, true, false, false, false, nil); err != nil {
		return err
	}
	for exchange, bindings := range map[string][]string{
		catalogExchange: {eventStockReserved, eventStockRejected},
		paymentExchange: {eventPaymentSucceeded, eventPaymentFailed},
	} {
		if err := ch.ExchangeDeclare(exchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
			return err
		}
		for _, binding := range bindings {
			if err := ch.QueueBind(eventQueue, binding, exchange, false, nil); err != nil {
				return err
			}
		}
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
