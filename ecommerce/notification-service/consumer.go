package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	eventQueue    = "notification-service.orders"
	orderExchange = "order.events"

	consumerRetryDelay = 5 * time.Second
	consumerPrefetch   = 5
)

// Consumer sends emails from the durable queue. A failure, such as the mail server being down,
// returns unacknowledged events to the queue, so their emails wait instead of being lost.
type Consumer struct {
	notifier *Notifier
	url      string
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
	if err := ch.ExchangeDeclare(orderExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return err
	}
	for _, binding := range []string{eventOrderPaid, eventOrderRejected, eventOrderCancelled} {
		if err := ch.QueueBind(eventQueue, binding, orderExchange, false, nil); err != nil {
			return err
		}
	}
	deliveries, err := ch.ConsumeWithContext(ctx, eventQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	slog.Info("reading events", "queue", eventQueue)
	for delivery := range deliveries {
		if err := c.notifier.handle(ctx, delivery.Body); err != nil {
			return err
		}
		if err := delivery.Ack(false); err != nil {
			return err
		}
	}
	return errors.New("the event stream ended")
}
