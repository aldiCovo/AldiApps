package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE payments, outbox_events RESTART IDENTITY"); err != nil {
		t.Fatal(err)
	}
	return &Store{pool: pool}
}

func requested(t *testing.T, orderID, amount int64, outcome string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"order_id": orderID, "amount": amount, "outcome": outcome})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(Event{ID: 1, Type: eventPaymentRequested, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func events(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT type FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		types = append(types, eventType)
	}
	return types
}

func TestOutcomesAndDuplicates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for range 2 {
		if err := s.handleEvent(ctx, requested(t, 1, 5000, outcomeSuccess)); err != nil {
			t.Fatal(err)
		}
		if err := s.handleEvent(ctx, requested(t, 2, 7000, outcomeFailure)); err != nil {
			t.Fatal(err)
		}
	}
	got := events(t, s)
	if len(got) != 2 || got[0] != eventPaymentSucceeded || got[1] != eventPaymentFailed {
		t.Fatalf("events %v", got)
	}
	var status string
	var amount int64
	if err := s.pool.QueryRow(ctx, `SELECT status, amount FROM payments WHERE order_id = 1`).Scan(&status, &amount); err != nil {
		t.Fatal(err)
	}
	if status != statusSucceeded || amount != 5000 {
		t.Fatalf("payment %s %d", status, amount)
	}
	if err := s.pool.QueryRow(ctx, `SELECT status FROM payments WHERE order_id = 2`).Scan(&status); err != nil || status != statusFailed {
		t.Fatalf("payment %s %v", status, err)
	}
	// A repeat with another outcome can't change a finished payment.
	if err := s.handleEvent(ctx, requested(t, 2, 7000, outcomeSuccess)); err != nil {
		t.Fatal(err)
	}
	if got := events(t, s); len(got) != 2 {
		t.Fatalf("events %v", got)
	}
}

func TestBadEventsAreDropped(t *testing.T) {
	s := newTestStore(t)
	for _, body := range [][]byte{
		[]byte(`nope`),
		requested(t, 0, 1, outcomeSuccess),
		requested(t, 1, -1, outcomeSuccess),
		requested(t, 1, 1, "maybe"),
		[]byte(`{"id":1,"type":"payment.other","data":{"order_id":1}}`),
	} {
		if err := s.handleEvent(context.Background(), body); err != nil {
			t.Fatal(err)
		}
	}
	if got := events(t, s); len(got) != 0 {
		t.Fatalf("events %v", got)
	}
}
