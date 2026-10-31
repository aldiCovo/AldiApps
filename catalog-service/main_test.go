package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestServer needs a throwaway database, because it empties the products table.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE products RESTART IDENTITY"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(&Store{pool: pool}))
	t.Cleanup(server.Close)
	return server
}

func request(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, buf.Bytes()
}

func createProduct(t *testing.T, server *httptest.Server, stock int) Product {
	t.Helper()
	status, body := request(t, http.MethodPost, server.URL+"/products",
		fmt.Sprintf(`{"name":"Kopi","description":"Arabika","price":25000,"stock":%d}`, stock))
	if status != http.StatusCreated {
		t.Fatalf("create: status %d: %s", status, body)
	}
	var p Product
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProductLifecycle(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server, 5)
	url := fmt.Sprintf("%s/products/%d", server.URL, p.ID)

	status, body := request(t, http.MethodPut, url, `{"name":"Kopi Gayo","price":30000}`)
	if status != http.StatusOK {
		t.Fatalf("update: status %d: %s", status, body)
	}

	status, body = request(t, http.MethodGet, url, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d: %s", status, body)
	}
	var got Product
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Kopi Gayo" || got.Price != 30000 || got.Stock != 5 {
		t.Fatalf("unexpected product after update: %+v", got)
	}

	status, body = request(t, http.MethodGet, server.URL+"/products?limit=1", "")
	if status != http.StatusOK || !strings.Contains(string(body), "Kopi Gayo") {
		t.Fatalf("list: status %d: %s", status, body)
	}

	if status, body = request(t, http.MethodDelete, url, ""); status != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", status, body)
	}
	if status, _ = request(t, http.MethodGet, url, ""); status != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", status)
	}
	if status, _ = request(t, http.MethodDelete, url, ""); status != http.StatusNotFound {
		t.Fatalf("second delete: status %d", status)
	}
}

func TestEmptyListIsAnArray(t *testing.T) {
	server := newTestServer(t)
	status, body := request(t, http.MethodGet, server.URL+"/products", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"products":[]}` {
		t.Fatalf("status %d: %s", status, body)
	}
}

func TestInvalidRequests(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server, 1)
	product := fmt.Sprintf("%s/products/%d", server.URL, p.ID)
	products := server.URL + "/products"

	tests := []struct {
		name, method, url, body string
		want                    int
	}{
		{"malformed JSON", http.MethodPost, products, `{`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, products, `{"name":"a","price":1,"sku":"x"}`, http.StatusBadRequest},
		{"blank name", http.MethodPost, products, `{"name":"  ","price":1}`, http.StatusBadRequest},
		{"long name", http.MethodPost, products, `{"name":"` + strings.Repeat("a", 201) + `","price":1}`, http.StatusBadRequest},
		{"NUL in name", http.MethodPost, products, `{"name":"a\u0000b","price":1}`, http.StatusBadRequest},
		{"missing price", http.MethodPost, products, `{"name":"a"}`, http.StatusBadRequest},
		{"negative price", http.MethodPost, products, `{"name":"a","price":-1}`, http.StatusBadRequest},
		{"negative stock", http.MethodPost, products, `{"name":"a","price":1,"stock":-1}`, http.StatusBadRequest},
		{"stock too large", http.MethodPost, products, `{"name":"a","price":1,"stock":1000000001}`, http.StatusBadRequest},
		{"stock in update", http.MethodPut, product, `{"name":"a","price":1,"stock":9}`, http.StatusBadRequest},
		{"zero delta", http.MethodPatch, product + "/stock", `{"delta":0}`, http.StatusBadRequest},
		{"delta overflows int32", http.MethodPatch, product + "/stock", `{"delta":3000000000}`, http.StatusBadRequest},
		{"bad id", http.MethodGet, products + "/abc", "", http.StatusBadRequest},
		{"zero id", http.MethodGet, products + "/0", "", http.StatusBadRequest},
		{"bad limit", http.MethodGet, products + "?limit=0", "", http.StatusBadRequest},
		{"limit too large", http.MethodGet, products + "?limit=101", "", http.StatusBadRequest},
		{"bad offset", http.MethodGet, products + "?offset=-1", "", http.StatusBadRequest},
		{"body too large", http.MethodPost, products, `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
		{"update missing product", http.MethodPut, products + "/999999", `{"name":"a","price":1}`, http.StatusNotFound},
		{"stock of missing product", http.MethodPatch, products + "/999999/stock", `{"delta":1}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, body := request(t, tt.method, tt.url, tt.body); status != tt.want {
				t.Fatalf("status %d, want %d: %s", status, tt.want, body)
			}
		})
	}
}

func TestStockLimits(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server, 2)
	url := fmt.Sprintf("%s/products/%d/stock", server.URL, p.ID)

	if status, body := request(t, http.MethodPatch, url, `{"delta":-3}`); status != http.StatusConflict {
		t.Fatalf("oversell: status %d: %s", status, body)
	}
	if status, body := request(t, http.MethodPatch, url, `{"delta":-2}`); status != http.StatusOK || !strings.Contains(string(body), `"stock":0`) {
		t.Fatalf("sell all: status %d: %s", status, body)
	}
	maxDelta := fmt.Sprintf(`{"delta":%d}`, maxStock)
	if status, body := request(t, http.MethodPatch, url, maxDelta); status != http.StatusOK {
		t.Fatalf("up to maximum: status %d: %s", status, body)
	}
	// The largest stock plus the largest delta must not overflow INTEGER inside the query.
	if status, body := request(t, http.MethodPatch, url, maxDelta); status != http.StatusConflict {
		t.Fatalf("largest delta above maximum: status %d: %s", status, body)
	}
	if status, body := request(t, http.MethodPatch, url, `{"delta":1}`); status != http.StatusConflict {
		t.Fatalf("above maximum: status %d: %s", status, body)
	}
}

func TestConcurrentOrdersCannotOversell(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server, 10)
	url := fmt.Sprintf("%s/products/%d/stock", server.URL, p.ID)

	var wg sync.WaitGroup
	statuses := make(chan int, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPatch, url, strings.NewReader(`{"delta":-1}`))
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
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)

	sold := 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			sold++
		case http.StatusConflict:
		default:
			t.Errorf("unexpected status %d", status)
		}
	}
	if sold != 10 {
		t.Fatalf("sold %d, want 10", sold)
	}
}
