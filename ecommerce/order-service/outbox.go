package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	eventExchange = "order.events"

	outboxBatchSize    = 100
	outboxPollInterval = time.Second
	outboxRetryDelay   = 5 * time.Second
	outboxConfirmWait  = 10 * time.Second

	// outboxLockKey lets one Pod publish at a time, which keeps events in order during a rollout.
	outboxLockKey = 7246001
)

// Event is the message body.
type Event struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

func addEvent(ctx context.Context, tx pgx.Tx, eventType string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events (type, data) VALUES ($1, $2)`, eventType, body)
	return err
}

// Publisher sends the outbox to RabbitMQ. A failure leaves the events in the outbox, so
// RabbitMQ being down delays events without affecting the API. Consumers can get an event
// twice if a Pod stops between RabbitMQ's confirmation and the delete.
type Publisher struct {
	pool *pgxpool.Pool
	url  string
}

// Run publishes until ctx is cancelled, reconnecting whenever RabbitMQ is unavailable.
func (p *Publisher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := p.connectAndPublish(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Error("event publishing stopped, retrying", "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(outboxRetryDelay):
		}
	}
}

func (p *Publisher) connectAndPublish(ctx context.Context) error {
	conn, err := amqp.Dial(p.url)
	if err != nil {
		return fmt.Errorf("connecting to RabbitMQ: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Confirm(false); err != nil {
		return err
	}
	if err := ch.ExchangeDeclare(eventExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return err
	}
	slog.Info("publishing events", "exchange", eventExchange)

	for {
		published, err := p.publishBatch(ctx, ch)
		if err != nil {
			return err
		}
		// A full batch means there may be more waiting.
		if published == outboxBatchSize {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(outboxPollInterval):
		}
	}
}

// publishBatch only deletes events after RabbitMQ has confirmed all of them.
func (p *Publisher) publishBatch(ctx context.Context, ch *amqp.Channel) (int, error) {
	published := 0
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, outboxLockKey).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return nil
		}

		rows, err := tx.Query(ctx,
			`SELECT id, type, created_at, data FROM outbox_events ORDER BY id LIMIT $1`, outboxBatchSize)
		if err != nil {
			return err
		}
		events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
			var e Event
			err := row.Scan(&e.ID, &e.Type, &e.OccurredAt, &e.Data)
			return e, err
		})
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}

		confirmCtx, cancel := context.WithTimeout(ctx, outboxConfirmWait)
		defer cancel()
		confirmations := make([]*amqp.DeferredConfirmation, len(events))
		ids := make([]int64, len(events))
		for i, e := range events {
			body, err := json.Marshal(e)
			if err != nil {
				return err
			}
			confirmations[i], err = ch.PublishWithDeferredConfirmWithContext(confirmCtx, eventExchange, e.Type, false, false, amqp.Publishing{
				ContentType:  "application/json",
				DeliveryMode: amqp.Persistent,
				MessageId:    strconv.FormatInt(e.ID, 10),
				Type:         e.Type,
				Timestamp:    e.OccurredAt,
				Body:         body,
			})
			if err != nil {
				return err
			}
			ids[i] = e.ID
		}
		for _, confirmation := range confirmations {
			acked, err := confirmation.WaitContext(confirmCtx)
			if err != nil {
				return err
			}
			if !acked {
				return errors.New("RabbitMQ refused an event")
			}
		}

		if _, err := tx.Exec(ctx, `DELETE FROM outbox_events WHERE id = ANY($1)`, ids); err != nil {
			return err
		}
		published = len(events)
		return nil
	})
	return published, err
}
