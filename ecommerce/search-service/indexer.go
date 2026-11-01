package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	eventExchange = "catalog.events"
	eventQueue    = "search-service.catalog"
	eventBinding  = "product.*"

	eventProductCreated = "product.created"
	eventProductUpdated = "product.updated"
	eventProductDeleted = "product.deleted"

	indexerRetryDelay = 5 * time.Second
	indexerPrefetch   = 20
	syncPageSize      = 100

	// syncVersion is below every event's id. A synced product therefore never replaces
	// what an event already stored, and any later event replaces it.
	syncVersion = 0
)

// Event is the message body catalog-service publishes.
type Event struct {
	ID   int64           `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Indexer keeps the search index equal to the catalog. A failure returns unacknowledged
// events to the durable queue, so they wait until Elasticsearch can take them.
type Indexer struct {
	elastic     *Elastic
	rabbitmqURL string
	catalogURL  string
}

// Run indexes until ctx is cancelled, starting over whenever something it needs is unavailable.
func (i *Indexer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := i.connectAndIndex(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Error("indexing stopped, retrying", "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(indexerRetryDelay):
		}
	}
}

func (i *Indexer) connectAndIndex(ctx context.Context) error {
	conn, err := amqp.Dial(i.rabbitmqURL)
	if err != nil {
		return fmt.Errorf("connecting to RabbitMQ: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Qos(indexerPrefetch, 0, false); err != nil {
		return err
	}
	if err := ch.ExchangeDeclare(eventExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(eventQueue, true, false, false, false, nil); err != nil {
		return err
	}
	// Binding comes before the sync, so no change can fall between the two.
	if err := ch.QueueBind(eventQueue, eventBinding, eventExchange, false, nil); err != nil {
		return err
	}
	if err := i.elastic.EnsureIndex(ctx); err != nil {
		return err
	}
	if err := i.syncCatalog(ctx); err != nil {
		return fmt.Errorf("syncing the catalog: %w", err)
	}

	deliveries, err := ch.ConsumeWithContext(ctx, eventQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	slog.Info("indexing events", "queue", eventQueue)
	for delivery := range deliveries {
		if err := i.handle(ctx, delivery.Body); err != nil {
			return err
		}
		if err := delivery.Ack(false); err != nil {
			return err
		}
	}
	return fmt.Errorf("the event stream ended: %w", ctx.Err())
}

// handle only returns errors that retrying can fix. A message that can't be read will never
// become readable, so it is logged and dropped instead of blocking the queue.
func (i *Indexer) handle(ctx context.Context, body []byte) error {
	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("dropping unreadable event", "error", err)
		return nil
	}
	switch event.Type {
	case eventProductCreated, eventProductUpdated:
		var p Product
		if err := json.Unmarshal(event.Data, &p); err != nil || p.ID <= 0 {
			slog.Error("dropping unreadable event", "event_id", event.ID, "error", err)
			return nil
		}
		return i.elastic.IndexProduct(ctx, p, event.ID)
	case eventProductDeleted:
		var deleted struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(event.Data, &deleted); err != nil || deleted.ID <= 0 {
			slog.Error("dropping unreadable event", "event_id", event.ID, "error", err)
			return nil
		}
		return i.elastic.DeleteProduct(ctx, deleted.ID, event.ID)
	}
	slog.Warn("ignoring unknown event type", "event_id", event.ID, "type", event.Type)
	return nil
}

// syncCatalog loads what happened before the queue existed, or what an emptied index lost.
func (i *Indexer) syncCatalog(ctx context.Context) error {
	client := &http.Client{Timeout: 15 * time.Second}
	total := 0
	for offset := 0; ; offset += syncPageSize {
		products, err := fetchProducts(ctx, client, i.catalogURL, offset)
		if err != nil {
			return err
		}
		for _, p := range products {
			if err := i.elastic.IndexProduct(ctx, p, syncVersion); err != nil {
				return err
			}
		}
		total += len(products)
		if len(products) < syncPageSize {
			slog.Info("catalog synced", "products", total)
			return nil
		}
	}
}

func fetchProducts(ctx context.Context, client *http.Client, catalogURL string, offset int) ([]Product, error) {
	url := fmt.Sprintf("%s/products?limit=%d&offset=%d", strings.TrimRight(catalogURL, "/"), syncPageSize, offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog-service answered %d", resp.StatusCode)
	}
	var page struct {
		Products []Product `json:"products"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, err
	}
	return page.Products, nil
}
