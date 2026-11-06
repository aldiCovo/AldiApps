package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func saleBody(price int64, starts, ends time.Time) string {
	return fmt.Sprintf(`{"price":%d,"starts_at":%q,"ends_at":%q}`, price, starts.Format(time.RFC3339), ends.Format(time.RFC3339))
}

func decodeProduct(t *testing.T, body []byte) Product {
	t.Helper()
	var p Product
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func getTestProduct(t *testing.T, url string) Product {
	t.Helper()
	status, body := request(t, http.MethodGet, url, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", url, status, body)
	}
	return decodeProduct(t, body)
}

// lastEventPrice is the price in the newest product event, as search-service would index it.
func lastEventPrice(t *testing.T, pool *pgxpool.Pool) (eventType string, price int64, original *int64) {
	t.Helper()
	events := outbox(t, pool)
	last := events[len(events)-1]
	p := decodeProduct(t, last.Data)
	return last.Type, p.Price, p.OriginalPrice
}

func announce(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	n, err := (&Store{pool: pool}).AnnounceSales(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaleRunsAndEnds(t *testing.T) {
	server, pool := newEventServer(t)
	p := createProduct(t, server) // Rp 350.000
	url := fmt.Sprintf("%s/products/%d", server.URL, p.ID)
	now := time.Now()

	// A sale that runs now changes the price at once, and its event already carries it.
	status, body := request(t, http.MethodPut, url+"/sale", saleBody(280000, now.Add(-time.Minute), now.Add(time.Hour)))
	if status != http.StatusOK {
		t.Fatalf("set sale: status %d: %s", status, body)
	}
	got := decodeProduct(t, body)
	if got.Price != 280000 || got.RegularPrice != 350000 || got.OriginalPrice == nil || *got.OriginalPrice != 350000 ||
		got.Sale == nil || got.Sale.Price != 280000 {
		t.Fatalf("product on sale: %+v", got)
	}
	if kind, price, original := lastEventPrice(t, pool); kind != eventProductUpdated || price != 280000 || original == nil {
		t.Fatalf("sale event: %s %d %v", kind, price, original)
	}
	if n := announce(t, pool); n != 0 {
		t.Fatalf("the sale was already announced, got %d", n)
	}

	// The regular price can't drop to the sale price, but can change above it.
	if status, body = request(t, http.MethodPut, url, `{"name":"Dress Batik","price":280000}`); status != http.StatusConflict {
		t.Fatalf("price at the sale price: status %d: %s", status, body)
	}
	if status, body = request(t, http.MethodPut, url, `{"name":"Dress Batik","price":300000}`); status != http.StatusOK {
		t.Fatalf("price above the sale price: status %d: %s", status, body)
	}
	if got = decodeProduct(t, body); got.Price != 280000 || *got.OriginalPrice != 300000 {
		t.Fatalf("after changing the regular price: %+v", got)
	}

	// The sale ends: the price goes back, and the announcer tells search-service once.
	if _, err := pool.Exec(context.Background(),
		`UPDATE products SET sale_starts_at = now() - interval '2 hours', sale_ends_at = now() - interval '1 hour' WHERE id = $1`,
		p.ID); err != nil {
		t.Fatal(err)
	}
	got = getTestProduct(t, url)
	if got.Price != 300000 || got.OriginalPrice != nil || got.Sale == nil {
		t.Fatalf("after the sale ended: %+v", got)
	}
	if n := announce(t, pool); n != 1 {
		t.Fatalf("announced %d products, want 1", n)
	}
	if _, price, original := lastEventPrice(t, pool); price != 300000 || original != nil {
		t.Fatalf("end-of-sale event: %d %v", price, original)
	}
	if n := announce(t, pool); n != 0 {
		t.Fatalf("announced the end twice: %d", n)
	}

	// An ended sale no longer holds the regular price up.
	if status, body = request(t, http.MethodPut, url, `{"name":"Dress Batik","price":250000}`); status != http.StatusOK {
		t.Fatalf("price below an ended sale: status %d: %s", status, body)
	}
}

func TestScheduledSaleStartsLater(t *testing.T) {
	server, pool := newEventServer(t)
	p := createProduct(t, server)
	url := fmt.Sprintf("%s/products/%d", server.URL, p.ID)
	now := time.Now()

	status, body := request(t, http.MethodPut, url+"/sale", saleBody(300000, now.Add(time.Hour), now.Add(2*time.Hour)))
	if status != http.StatusOK {
		t.Fatalf("set sale: status %d: %s", status, body)
	}
	if got := decodeProduct(t, body); got.Price != 350000 || got.OriginalPrice != nil || got.Sale == nil {
		t.Fatalf("scheduled sale shouldn't change the price yet: %+v", got)
	}
	if n := announce(t, pool); n != 0 {
		t.Fatalf("nothing started yet, announced %d", n)
	}
	// A sale that hasn't started also holds the regular price up.
	if status, _ = request(t, http.MethodPut, url, `{"name":"Dress Batik","price":290000}`); status != http.StatusConflict {
		t.Fatalf("price below a scheduled sale: status %d", status)
	}

	if _, err := pool.Exec(context.Background(),
		`UPDATE products SET sale_starts_at = now() - interval '1 second' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if n := announce(t, pool); n != 1 {
		t.Fatalf("announced %d products, want 1", n)
	}
	if kind, price, original := lastEventPrice(t, pool); kind != eventProductUpdated || price != 300000 || original == nil || *original != 350000 {
		t.Fatalf("start-of-sale event: %s %d %v", kind, price, original)
	}
	if got := getTestProduct(t, url); got.Price != 300000 {
		t.Fatalf("after the sale started: %+v", got)
	}
}

func TestClearSale(t *testing.T) {
	server, pool := newEventServer(t)
	p := createProduct(t, server)
	url := fmt.Sprintf("%s/products/%d", server.URL, p.ID)
	now := time.Now()
	if status, body := request(t, http.MethodPut, url+"/sale", saleBody(300000, now, now.Add(time.Hour))); status != http.StatusOK {
		t.Fatalf("set sale: status %d: %s", status, body)
	}

	status, body := request(t, http.MethodDelete, url+"/sale", "")
	if status != http.StatusOK {
		t.Fatalf("clear sale: status %d: %s", status, body)
	}
	if got := decodeProduct(t, body); got.Price != 350000 || got.OriginalPrice != nil || got.Sale != nil {
		t.Fatalf("after clearing: %+v", got)
	}
	if _, price, original := lastEventPrice(t, pool); price != 350000 || original != nil {
		t.Fatalf("clear event: %d %v", price, original)
	}
	events := len(outbox(t, pool))
	if status, _ = request(t, http.MethodDelete, url+"/sale", ""); status != http.StatusOK {
		t.Fatalf("clearing again: status %d", status)
	}
	if len(outbox(t, pool)) != events {
		t.Fatal("clearing a product without a sale shouldn't send an event")
	}
	if n := announce(t, pool); n != 0 {
		t.Fatalf("announced %d after clearing", n)
	}
}

func TestInvalidSales(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server)
	url := fmt.Sprintf("%s/products/%d/sale", server.URL, p.ID)
	now := time.Now()
	cases := []struct {
		name, url, body string
		want            int
	}{
		{"not below the regular price", url, saleBody(350000, now, now.Add(time.Hour)), http.StatusConflict},
		{"zero price", url, saleBody(0, now, now.Add(time.Hour)), http.StatusBadRequest},
		{"no price", url, fmt.Sprintf(`{"starts_at":%q,"ends_at":%q}`, now.Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)), http.StatusBadRequest},
		{"no period", url, `{"price":1000}`, http.StatusBadRequest},
		{"ends before it starts", url, saleBody(1000, now.Add(time.Hour), now), http.StatusBadRequest},
		{"already over", url, saleBody(1000, now.Add(-2*time.Hour), now.Add(-time.Hour)), http.StatusBadRequest},
		{"not a time", url, `{"price":1000,"starts_at":"besok","ends_at":"lusa"}`, http.StatusBadRequest},
		{"unknown product", server.URL + "/products/999999/sale", saleBody(1000, now, now.Add(time.Hour)), http.StatusNotFound},
	}
	for _, c := range cases {
		if status, body := request(t, http.MethodPut, c.url, c.body); status != c.want {
			t.Errorf("%s: status %d, want %d: %s", c.name, status, c.want, body)
		}
	}
	if status, _ := request(t, http.MethodDelete, server.URL+"/products/999999/sale", ""); status != http.StatusNotFound {
		t.Errorf("clearing an unknown product's sale: status %d", status)
	}
	if got := getTestProduct(t, fmt.Sprintf("%s/products/%d", server.URL, p.ID)); got.Sale != nil {
		t.Fatalf("a refused sale was kept: %+v", got.Sale)
	}
}
