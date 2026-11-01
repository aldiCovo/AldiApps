package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func orderEvent(t *testing.T, eventType string, orderID int64, items []ReservationItem) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"order_id": orderID, "items": items})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(Event{ID: 1, Type: eventType, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func stockOf(t *testing.T, server string, variantID int64) int32 {
	t.Helper()
	status, body := request(t, http.MethodGet, fmt.Sprintf("%s/variants/%d", server, variantID), "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var v Variant
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v.Stock
}

func countEvents(events []storedEvent, eventType string) int {
	count := 0
	for _, e := range events {
		if e.Type == eventType {
			count++
		}
	}
	return count
}

func TestReserveTakesStockOnceAndReleaseReturnsItOnce(t *testing.T) {
	server, pool := newEventServer(t)
	store := &Store{pool: pool}
	ctx := context.Background()
	p := createProduct(t, server)
	v := createVariant(t, server, p.ID, "M", "Merah", 5)
	created := orderEvent(t, eventOrderCreated, 7, []ReservationItem{{VariantID: v.ID, Quantity: 2}})

	for range 2 {
		if err := store.handleOrderEvent(ctx, created); err != nil {
			t.Fatal(err)
		}
	}
	if got := stockOf(t, server.URL, v.ID); got != 3 {
		t.Fatalf("stock after reserve %d", got)
	}
	events := outbox(t, pool)
	if countEvents(events, eventStockReserved) != 1 {
		t.Fatalf("events %+v", events)
	}
	var reserved stockEvent
	if err := json.Unmarshal(events[len(events)-1].Data, &reserved); err != nil || reserved.OrderID != 7 {
		t.Fatalf("last event %s", events[len(events)-1].Data)
	}
	// The search index learns of the new stock from this event.
	if countEvents(events, eventProductUpdated) != 2 {
		t.Fatalf("product.updated count, events %+v", events)
	}

	cancelled := orderEvent(t, eventOrderCancelled, 7, nil)
	for range 2 {
		if err := store.handleOrderEvent(ctx, cancelled); err != nil {
			t.Fatal(err)
		}
	}
	if got := stockOf(t, server.URL, v.ID); got != 5 {
		t.Fatalf("stock after release %d", got)
	}
	// A repeated order.created must not take the stock again after a release.
	if err := store.handleOrderEvent(ctx, created); err != nil {
		t.Fatal(err)
	}
	if got := stockOf(t, server.URL, v.ID); got != 5 {
		t.Fatalf("stock after late duplicate %d", got)
	}
}

func TestReserveIsAllOrNothing(t *testing.T) {
	server, pool := newEventServer(t)
	store := &Store{pool: pool}
	ctx := context.Background()
	a := createVariant(t, server, createProduct(t, server).ID, "M", "Merah", 5)
	b := createVariant(t, server, createProduct(t, server).ID, "L", "Navy", 1)

	if err := store.Reserve(ctx, 1, []ReservationItem{{VariantID: a.ID, Quantity: 2}, {VariantID: b.ID, Quantity: 2}}); err != nil {
		t.Fatal(err)
	}
	if stockOf(t, server.URL, a.ID) != 5 || stockOf(t, server.URL, b.ID) != 1 {
		t.Fatal("a rejected order changed stock")
	}
	events := outbox(t, pool)
	last := events[len(events)-1]
	if last.Type != eventStockRejected || !strings.Contains(string(last.Data), reasonOutOfStock) {
		t.Fatalf("last event %+v", last)
	}
	// Releasing a rejected order must not add stock it never took.
	if err := store.Release(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if stockOf(t, server.URL, a.ID) != 5 {
		t.Fatal("releasing a rejected order added stock")
	}

	if err := store.Reserve(ctx, 2, []ReservationItem{{VariantID: 99999, Quantity: 1}}); err != nil {
		t.Fatal(err)
	}
	events = outbox(t, pool)
	if events[len(events)-1].Type != eventStockRejected {
		t.Fatalf("unknown variant: %+v", events[len(events)-1])
	}
	// The same variant twice adds up.
	if err := store.Reserve(ctx, 3, []ReservationItem{{VariantID: b.ID, Quantity: 1}, {VariantID: b.ID, Quantity: 1}}); err != nil {
		t.Fatal(err)
	}
	if stockOf(t, server.URL, b.ID) != 1 {
		t.Fatal("repeated variant passed the stock check")
	}
}

func TestConcurrentReservationsCannotOversell(t *testing.T) {
	server, pool := newEventServer(t)
	store := &Store{pool: pool}
	ctx := context.Background()
	v1 := createVariant(t, server, createProduct(t, server).ID, "M", "Merah", 3)
	v2 := createVariant(t, server, createProduct(t, server).ID, "L", "Navy", 3)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Opposite item orders would deadlock without the sorted locks.
			items := []ReservationItem{{VariantID: v1.ID, Quantity: 1}, {VariantID: v2.ID, Quantity: 1}}
			if i%2 == 0 {
				items[0], items[1] = items[1], items[0]
			}
			errs <- store.Reserve(ctx, int64(i+1), items)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if stockOf(t, server.URL, v1.ID) != 0 || stockOf(t, server.URL, v2.ID) != 0 {
		t.Fatalf("stock left %d and %d", stockOf(t, server.URL, v1.ID), stockOf(t, server.URL, v2.ID))
	}
	events := outbox(t, pool)
	if countEvents(events, eventStockReserved) != 3 || countEvents(events, eventStockRejected) != 9 {
		t.Fatalf("reserved %d, rejected %d", countEvents(events, eventStockReserved), countEvents(events, eventStockRejected))
	}
}

func TestReleaseSurvivesADeletedVariant(t *testing.T) {
	server, pool := newEventServer(t)
	store := &Store{pool: pool}
	ctx := context.Background()
	p := createProduct(t, server)
	v := createVariant(t, server, p.ID, "M", "Merah", 5)
	if err := store.Reserve(ctx, 1, []ReservationItem{{VariantID: v.ID, Quantity: 1}}); err != nil {
		t.Fatal(err)
	}
	if status, body := request(t, http.MethodDelete, fmt.Sprintf("%s/products/%d", server.URL, p.ID), ""); status >= 300 {
		t.Fatalf("status %d: %s", status, body)
	}
	if err := store.Release(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

func TestBadOrderEventsAreDropped(t *testing.T) {
	server, pool := newEventServer(t)
	store := &Store{pool: pool}
	ctx := context.Background()
	v := createVariant(t, server, createProduct(t, server).ID, "M", "Merah", 5)
	before := len(outbox(t, pool))
	bad := [][]byte{
		[]byte(`nope`),
		orderEvent(t, eventOrderCreated, 0, []ReservationItem{{VariantID: v.ID, Quantity: 1}}),
		orderEvent(t, eventOrderCreated, 1, nil),
		orderEvent(t, eventOrderCreated, 1, []ReservationItem{{VariantID: v.ID, Quantity: -5}}),
		orderEvent(t, "order.other", 1, []ReservationItem{{VariantID: v.ID, Quantity: 1}}),
	}
	for _, body := range bad {
		if err := store.handleOrderEvent(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	if stockOf(t, server.URL, v.ID) != 5 || len(outbox(t, pool)) != before {
		t.Fatal("a bad event changed something")
	}
}
