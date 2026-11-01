package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
)

// newEventServer is newTestServer that also hands back the pool, to read the outbox.
func newEventServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	server := newTestServer(t)
	pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return server, pool
}

type storedEvent struct {
	Type string
	Data json.RawMessage
}

func outbox(t *testing.T, pool *pgxpool.Pool) []storedEvent {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT type, data FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []storedEvent
	for rows.Next() {
		var e storedEvent
		if err := rows.Scan(&e.Type, &e.Data); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}

func TestEveryChangeAddsAnEvent(t *testing.T) {
	server, pool := newEventServer(t)
	c := createCategory(t, server, "Dress")
	p := createProduct(t, server)
	v := createVariant(t, server, p.ID, "M", "Merah", 5)
	category := fmt.Sprintf("%s/categories/%d", server.URL, c.ID)
	product := fmt.Sprintf("%s/products/%d", server.URL, p.ID)
	variant := fmt.Sprintf("%s/variants/%d", server.URL, v.ID)

	for _, step := range []struct{ method, url, body string }{
		{http.MethodPut, category, `{"name":"Gaun"}`},
		{http.MethodPut, product, `{"name":"Dress Gayo","price":1}`},
		{http.MethodPut, variant, `{"size":"L","color":"Navy"}`},
		{http.MethodPatch, variant + "/stock", `{"delta":-2}`},
		{http.MethodDelete, variant, ""},
		{http.MethodDelete, product, ""},
		{http.MethodDelete, category, ""},
	} {
		if status, body := request(t, step.method, step.url, step.body); status >= 300 {
			t.Fatalf("%s %s: status %d: %s", step.method, step.url, status, body)
		}
	}

	want := []string{
		eventCategoryCreated, eventProductCreated, eventProductUpdated, // create variant
		eventCategoryUpdated, eventProductUpdated, eventProductUpdated, // rename variant
		eventProductUpdated, // stock
		eventProductUpdated, // delete variant
		eventProductDeleted, eventCategoryDeleted,
	}
	events := outbox(t, pool)
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d is %s, want %s", i, events[i].Type, want[i])
		}
	}

	var afterStock Product
	if err := json.Unmarshal(events[6].Data, &afterStock); err != nil {
		t.Fatal(err)
	}
	if afterStock.Name != "Dress Gayo" || len(afterStock.Variants) != 1 || afterStock.Variants[0].Stock != 3 {
		t.Fatalf("stock event should carry the whole product: %s", events[6].Data)
	}
	var deleted deletedEvent
	if err := json.Unmarshal(events[8].Data, &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.ID != p.ID {
		t.Fatalf("delete event: %s", events[8].Data)
	}
}

func TestFailedChangesAddNoEvent(t *testing.T) {
	server, pool := newEventServer(t)
	c := createCategory(t, server, "Dress")
	var p Product
	mustCreate(t, server.URL+"/products", fmt.Sprintf(`{"name":"Dress","price":1,"category_id":%d}`, c.ID), &p)
	v := createVariant(t, server, p.ID, "M", "Merah", 1)
	before := len(outbox(t, pool))

	for _, step := range []struct {
		method, url, body string
		want              int
	}{
		{http.MethodPost, server.URL + "/categories", `{"name":"dress"}`, http.StatusConflict},
		{http.MethodDelete, fmt.Sprintf("%s/categories/%d", server.URL, c.ID), "", http.StatusConflict},
		{http.MethodPost, server.URL + "/products", `{"name":"a","price":1,"category_id":999999}`, http.StatusBadRequest},
		{http.MethodPut, server.URL + "/products/999999", `{"name":"a","price":1}`, http.StatusNotFound},
		{http.MethodDelete, server.URL + "/products/999999", "", http.StatusNotFound},
		{http.MethodPost, fmt.Sprintf("%s/products/%d/variants", server.URL, p.ID), `{"size":"m","color":"merah"}`, http.StatusConflict},
		{http.MethodPatch, fmt.Sprintf("%s/variants/%d/stock", server.URL, v.ID), `{"delta":-2}`, http.StatusConflict},
		{http.MethodDelete, server.URL + "/variants/999999", "", http.StatusNotFound},
	} {
		if status, body := request(t, step.method, step.url, step.body); status != step.want {
			t.Fatalf("%s %s: status %d, want %d: %s", step.method, step.url, status, step.want, body)
		}
	}
	if after := len(outbox(t, pool)); after != before {
		t.Fatalf("failed changes added %d events", after-before)
	}
}

// Each event carries the whole product, so the last one must include every change.
func TestLastEventSeesConcurrentVariantChanges(t *testing.T) {
	server, pool := newEventServer(t)
	p := createProduct(t, server)
	const variants = 8
	ids := make([]int64, variants)
	for i := range ids {
		ids[i] = createVariant(t, server, p.ID, strconv.Itoa(i), "Merah", 100).ID
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/variants/%d/stock", server.URL, id),
					strings.NewReader(`{"delta":-1}`))
				if err != nil {
					t.Error(err)
					return
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("status %d", resp.StatusCode)
				}
			}
		}()
	}
	wg.Wait()

	events := outbox(t, pool)
	var last Product
	if err := json.Unmarshal(events[len(events)-1].Data, &last); err != nil {
		t.Fatal(err)
	}
	for _, v := range last.Variants {
		if v.Stock != 95 {
			t.Fatalf("last event missed a concurrent change: %s", events[len(events)-1].Data)
		}
	}
}

func TestPublisherDeliversInOrderAndEmptiesOutbox(t *testing.T) {
	rabbitmqURL := os.Getenv("TEST_RABBITMQ_URL")
	if rabbitmqURL == "" {
		t.Skip("TEST_RABBITMQ_URL is not set")
	}
	server, pool := newEventServer(t)

	conn, err := amqp.Dial(rabbitmqURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if err := ch.ExchangeDeclare(eventExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	queue, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind(queue.Name, "#", eventExchange, false, nil); err != nil {
		t.Fatal(err)
	}
	deliveries, err := ch.Consume(queue.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	// More than one batch, written before the publisher starts, as after an outage.
	const products = outboxBatchSize + 20
	for range products {
		createProduct(t, server)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Publisher{pool: pool, url: rabbitmqURL}).Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	var lastID int64
	for i := range products {
		select {
		case d := <-deliveries:
			var e Event
			if err := json.Unmarshal(d.Body, &e); err != nil {
				t.Fatal(err)
			}
			if e.ID <= lastID {
				t.Fatalf("event %d arrived after event %d", e.ID, lastID)
			}
			lastID = e.ID
			if e.Type != eventProductCreated || d.RoutingKey != eventProductCreated || d.DeliveryMode != amqp.Persistent {
				t.Fatalf("unexpected delivery: %+v", d)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("got %d of %d events", i, products)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(outbox(t, pool)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d events still in the outbox", len(outbox(t, pool)))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPublisherKeepsEventsWhileRabbitMQIsDown(t *testing.T) {
	server, pool := newEventServer(t)
	createProduct(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Publisher{pool: pool, url: "amqp://guest:guest@127.0.0.1:1/"}).Run(ctx)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not stop")
	}
	if got := len(outbox(t, pool)); got != 1 {
		t.Fatalf("got %d events in the outbox, want 1", got)
	}
}
