package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func flashBody(price int64, quota int, starts, ends time.Time) string {
	return fmt.Sprintf(`{"price":%d,"quota":%d,"starts_at":%q,"ends_at":%q}`, price, quota, starts.Format(time.RFC3339), ends.Format(time.RFC3339))
}

func flashList(t *testing.T, server string) []Product {
	t.Helper()
	status, body := request(t, http.MethodGet, server+"/flash-sale", "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got struct {
		Products []Product `json:"products"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Products == nil {
		t.Fatalf("products is not a list: %s", body)
	}
	return got.Products
}

func lastStockEvent(t *testing.T, events []storedEvent) (string, stockEvent) {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventStockReserved || events[i].Type == eventStockRejected {
			var e stockEvent
			if err := json.Unmarshal(events[i].Data, &e); err != nil {
				t.Fatal(err)
			}
			return events[i].Type, e
		}
	}
	t.Fatal("no stock event")
	return "", stockEvent{}
}

func TestFlashSaleQuota(t *testing.T) {
	server, pool := newEventServer(t)
	store := &Store{pool: pool}
	ctx := context.Background()
	p := createProduct(t, server) // Rp 350.000
	v := createVariant(t, server, p.ID, "M", "Merah", 20)
	url := fmt.Sprintf("%s/products/%d", server.URL, p.ID)
	now := time.Now()
	starts, ends := now.Add(-time.Minute), now.Add(time.Hour)

	if got := flashList(t, server.URL); len(got) != 0 {
		t.Fatalf("flash list before any sale: %+v", got)
	}
	// A plain sale isn't a Flash Sale.
	if status, body := request(t, http.MethodPut, url+"/sale", saleBody(300000, starts, ends)); status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if got := flashList(t, server.URL); len(got) != 0 {
		t.Fatalf("a plain sale is listed: %+v", got)
	}
	status, body := request(t, http.MethodPut, url+"/sale", flashBody(280000, 3, starts, ends))
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if got := decodeProduct(t, body); got.Price != 280000 || got.Sale == nil || !got.Sale.Flash || *got.Sale.Quota != 3 || got.Sale.Sold != 0 {
		t.Fatalf("product %+v sale %+v", got, got.Sale)
	}
	if got := flashList(t, server.URL); len(got) != 1 || got[0].ID != p.ID || len(got[0].Variants) != 1 {
		t.Fatalf("flash list %+v", got)
	}

	reserve := func(orderID int64, quantity int32, price int64) (string, stockEvent) {
		t.Helper()
		event := orderEvent(t, eventOrderCreated, orderID, []ReservationItem{{VariantID: v.ID, Quantity: quantity, Price: price}})
		if err := store.handleOrderEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
		return lastStockEvent(t, outbox(t, pool))
	}
	// Two at the sale price take from the quota. One at the regular price doesn't, and neither
	// does an order from before prices were sent.
	if kind, _ := reserve(1, 2, 280000); kind != eventStockReserved {
		t.Fatalf("order 1: %s", kind)
	}
	if kind, _ := reserve(2, 1, 350000); kind != eventStockReserved {
		t.Fatalf("order 2: %s", kind)
	}
	if kind, _ := reserve(3, 1, 0); kind != eventStockReserved {
		t.Fatalf("order 3: %s", kind)
	}
	if got := getTestProduct(t, url); got.Sale.Sold != 2 || got.Price != 280000 {
		t.Fatalf("after 2 sold: price %d sale %+v", got.Price, got.Sale)
	}
	// Two more would pass the quota, so the order is refused and takes no stock.
	kind, e := reserve(4, 2, 280000)
	if kind != eventStockRejected || e.Reason != reasonSaleSoldOut {
		t.Fatalf("order 4: %s %+v", kind, e)
	}
	if got := stockOf(t, server.URL, v.ID); got != 16 {
		t.Fatalf("stock %d, want 16", got)
	}
	// The last one sells out the sale: the regular price is back and the product event says so.
	if kind, _ := reserve(5, 1, 280000); kind != eventStockReserved {
		t.Fatalf("order 5: %s", kind)
	}
	if got := getTestProduct(t, url); got.Price != 350000 || got.OriginalPrice != nil || got.Sale.Sold != 3 {
		t.Fatalf("sold out: price %d sale %+v", got.Price, got.Sale)
	}
	if got := flashList(t, server.URL); len(got) != 0 {
		t.Fatalf("a sold out sale is listed: %+v", got)
	}
	events := outbox(t, pool)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventProductUpdated {
			if p := decodeProduct(t, events[i].Data); p.Price != 350000 {
				t.Fatalf("last product event price %d", p.Price)
			}
			break
		}
	}
	if n := announce(t, pool); n != 0 {
		t.Fatalf("announcer sent %d, the order's event already had the price", n)
	}

	// Cancelling a sale-price order gives its quota back, and the sale runs again.
	if err := store.handleOrderEvent(ctx, orderEvent(t, eventOrderCancelled, 1, nil)); err != nil {
		t.Fatal(err)
	}
	if got := getTestProduct(t, url); got.Price != 280000 || got.Sale.Sold != 1 {
		t.Fatalf("after cancel: price %d sale %+v", got.Price, got.Sale)
	}
	if n := announce(t, pool); n != 1 {
		t.Fatalf("announcer sent %d, want 1 for the sale running again", n)
	}
	// Cancelling one at the regular price leaves the quota alone.
	if err := store.handleOrderEvent(ctx, orderEvent(t, eventOrderCancelled, 2, nil)); err != nil {
		t.Fatal(err)
	}
	if got := getTestProduct(t, url); got.Sale.Sold != 1 {
		t.Fatalf("after cancelling a regular order: sale %+v", got.Sale)
	}

	// Changing the quota keeps what was sold; a new start counts from zero.
	status, body = request(t, http.MethodPut, url+"/sale", flashBody(280000, 5, starts, ends))
	if got := decodeProduct(t, body); status != http.StatusOK || got.Sale.Sold != 1 {
		t.Fatalf("changed quota: %d %+v", status, got.Sale)
	}
	status, body = request(t, http.MethodPut, url+"/sale", flashBody(280000, 5, starts.Add(time.Second), ends))
	if got := decodeProduct(t, body); status != http.StatusOK || got.Sale.Sold != 0 {
		t.Fatalf("new sale: %d %+v", status, got.Sale)
	}
	// Turning it into a plain sale drops the quota.
	status, body = request(t, http.MethodPut, url+"/sale", saleBody(280000, starts, ends))
	if got := decodeProduct(t, body); status != http.StatusOK || got.Sale.Flash || got.Sale.Quota != nil {
		t.Fatalf("plain again: %d %+v", status, got.Sale)
	}
	for _, quota := range []int{0, -1, maxFlashQuota + 1} {
		if status, _ := request(t, http.MethodPut, url+"/sale", flashBody(280000, quota, starts, ends)); status != http.StatusBadRequest {
			t.Errorf("quota %d: status %d", quota, status)
		}
	}
}
