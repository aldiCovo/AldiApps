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
	"github.com/jackc/pgx/v5/stdlib"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// newTestServer needs a throwaway database, because it empties the tables.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	pool := newTestPool(t)
	ctx := context.Background()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE categories, products, product_variants, outbox_events RESTART IDENTITY"); err != nil {
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

// mustCreate posts body to url, expects 201, and decodes the result into dst.
func mustCreate(t *testing.T, url, body string, dst any) {
	t.Helper()
	status, got := request(t, http.MethodPost, url, body)
	if status != http.StatusCreated {
		t.Fatalf("POST %s: status %d: %s", url, status, got)
	}
	if err := json.Unmarshal(got, dst); err != nil {
		t.Fatal(err)
	}
}

func createCategory(t *testing.T, server *httptest.Server, name string) Category {
	t.Helper()
	var c Category
	mustCreate(t, server.URL+"/categories", fmt.Sprintf(`{"name":%q}`, name), &c)
	return c
}

func createProduct(t *testing.T, server *httptest.Server) Product {
	t.Helper()
	var p Product
	mustCreate(t, server.URL+"/products", `{"name":"Dress Batik","description":"Midi","price":350000}`, &p)
	return p
}

func createVariant(t *testing.T, server *httptest.Server, productID int64, size, color string, stock int) Variant {
	t.Helper()
	var v Variant
	mustCreate(t, fmt.Sprintf("%s/products/%d/variants", server.URL, productID),
		fmt.Sprintf(`{"size":%q,"color":%q,"stock":%d}`, size, color, stock), &v)
	return v
}

// TestMigrationKeepsExistingStock replays what happens to the products already on the cluster.
func TestMigrationKeepsExistingStock(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO products (name, price, stock) VALUES ('Dress', 350000, 10), ('Rok', 150000, 0)`); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	products, err := (&Store{pool: pool}).List(ctx, nil, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 2 {
		t.Fatalf("got %d products, want 2", len(products))
	}
	for i, wantStock := range []int32{10, 0} {
		p := products[i]
		if p.CategoryID != nil || len(p.Variants) != 1 {
			t.Fatalf("unexpected product after migration: %+v", p)
		}
		v := p.Variants[0]
		if v.Size != "All Size" || v.Color != "Default" || v.Stock != wantStock {
			t.Fatalf("unexpected variant after migration: %+v", v)
		}
	}

	// Going back must add the variants' stock up again.
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_variants (product_id, size, color, stock) VALUES (1, 'M', 'Merah', 5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var stock int
	if err := pool.QueryRow(ctx, `SELECT stock FROM products WHERE id = 1`).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 15 {
		t.Fatalf("stock after rollback is %d, want 15", stock)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCategoryLifecycle(t *testing.T) {
	server := newTestServer(t)
	c := createCategory(t, server, "Dress")
	url := fmt.Sprintf("%s/categories/%d", server.URL, c.ID)
	createCategory(t, server, "atasan")

	status, body := request(t, http.MethodGet, server.URL+"/categories", "")
	if status != http.StatusOK || strings.Index(string(body), "atasan") > strings.Index(string(body), "Dress") {
		t.Fatalf("list should sort by name ignoring case: status %d: %s", status, body)
	}

	if status, body := request(t, http.MethodPut, url, `{"name":"Gaun"}`); status != http.StatusOK || !strings.Contains(string(body), "Gaun") {
		t.Fatalf("update: status %d: %s", status, body)
	}
	if status, body := request(t, http.MethodPut, url, `{"name":"ATASAN"}`); status != http.StatusConflict {
		t.Fatalf("rename to an existing name: status %d: %s", status, body)
	}
	if status, body := request(t, http.MethodPost, server.URL+"/categories", `{"name":"GAUN"}`); status != http.StatusConflict {
		t.Fatalf("duplicate ignoring case: status %d: %s", status, body)
	}
	if status, body := request(t, http.MethodDelete, url, ""); status != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", status, body)
	}
	if status, _ := request(t, http.MethodGet, url, ""); status != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", status)
	}
	if status, _ := request(t, http.MethodDelete, url, ""); status != http.StatusNotFound {
		t.Fatalf("second delete: status %d", status)
	}
}

func TestProductsInACategory(t *testing.T) {
	server := newTestServer(t)
	dress := createCategory(t, server, "Dress")
	rok := createCategory(t, server, "Rok")

	var inDress, inRok Product
	mustCreate(t, server.URL+"/products", fmt.Sprintf(`{"name":"Dress Batik","price":1,"category_id":%d}`, dress.ID), &inDress)
	mustCreate(t, server.URL+"/products", fmt.Sprintf(`{"name":"Rok Plisket","price":1,"category_id":%d}`, rok.ID), &inRok)
	createProduct(t, server)

	status, body := request(t, http.MethodGet, fmt.Sprintf("%s/products?category_id=%d", server.URL, dress.ID), "")
	if status != http.StatusOK {
		t.Fatalf("filter: status %d: %s", status, body)
	}
	var list struct{ Products []Product }
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Products) != 1 || list.Products[0].ID != inDress.ID {
		t.Fatalf("filter returned %s", body)
	}

	url := fmt.Sprintf("%s/categories/%d", server.URL, dress.ID)
	if status, body := request(t, http.MethodDelete, url, ""); status != http.StatusConflict {
		t.Fatalf("delete category in use: status %d: %s", status, body)
	}

	productURL := fmt.Sprintf("%s/products/%d", server.URL, inDress.ID)
	if status, body := request(t, http.MethodPut, productURL, `{"name":"Dress Batik","price":1,"category_id":999999}`); status != http.StatusBadRequest {
		t.Fatalf("update to unknown category: status %d: %s", status, body)
	}
	// Leaving category_id out of a PUT removes the product from its category.
	status, body = request(t, http.MethodPut, productURL, `{"name":"Dress Batik","price":1}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"category_id":null`) {
		t.Fatalf("clear category: status %d: %s", status, body)
	}
	if status, body := request(t, http.MethodDelete, url, ""); status != http.StatusNoContent {
		t.Fatalf("delete empty category: status %d: %s", status, body)
	}
}

func TestProductLifecycle(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server)
	if p.Variants == nil || len(p.Variants) != 0 {
		t.Fatalf("new product should have an empty variants array: %+v", p)
	}
	url := fmt.Sprintf("%s/products/%d", server.URL, p.ID)
	v := createVariant(t, server, p.ID, "M", "Merah", 5)

	status, body := request(t, http.MethodPut, url, `{"name":"Dress Gayo","price":400000}`)
	if status != http.StatusOK {
		t.Fatalf("update: status %d: %s", status, body)
	}
	var got Product
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Dress Gayo" || got.Price != 400000 || len(got.Variants) != 1 || got.Variants[0].Stock != 5 {
		t.Fatalf("unexpected product after update: %+v", got)
	}

	status, body = request(t, http.MethodGet, server.URL+"/products?limit=1", "")
	if status != http.StatusOK || !strings.Contains(string(body), "Dress Gayo") || !strings.Contains(string(body), "Merah") {
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
	if status, _ = request(t, http.MethodGet, fmt.Sprintf("%s/variants/%d", server.URL, v.ID), ""); status != http.StatusNotFound {
		t.Fatalf("variant should be deleted with its product: status %d", status)
	}
}

func TestVariantLifecycle(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server)
	v := createVariant(t, server, p.ID, "M", "Merah", 5)
	createVariant(t, server, p.ID, "L", "Merah", 3)
	url := fmt.Sprintf("%s/variants/%d", server.URL, v.ID)

	// Another product may use the same size and color.
	other := createProduct(t, server)
	createVariant(t, server, other.ID, "M", "Merah", 1)

	variants := fmt.Sprintf("%s/products/%d/variants", server.URL, p.ID)
	if status, body := request(t, http.MethodPost, variants, `{"size":"m","color":"MERAH"}`); status != http.StatusConflict {
		t.Fatalf("duplicate ignoring case: status %d: %s", status, body)
	}
	if status, body := request(t, http.MethodPut, url, `{"size":"L","color":"merah"}`); status != http.StatusConflict {
		t.Fatalf("rename to an existing variant: status %d: %s", status, body)
	}

	status, body := request(t, http.MethodPut, url, `{"size":"S","color":"Navy"}`)
	if status != http.StatusOK {
		t.Fatalf("update: status %d: %s", status, body)
	}
	var got Variant
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Size != "S" || got.Color != "Navy" || got.Stock != 5 {
		t.Fatalf("unexpected variant after update: %+v", got)
	}

	if status, body := request(t, http.MethodDelete, url, ""); status != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", status, body)
	}
	if status, _ := request(t, http.MethodGet, url, ""); status != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", status)
	}
	if status, _ := request(t, http.MethodDelete, url, ""); status != http.StatusNotFound {
		t.Fatalf("second delete: status %d", status)
	}
}

func TestEmptyListsAreArrays(t *testing.T) {
	server := newTestServer(t)
	for path, want := range map[string]string{
		"/products":   `{"products":[]}`,
		"/categories": `{"categories":[]}`,
	} {
		status, body := request(t, http.MethodGet, server.URL+path, "")
		if status != http.StatusOK || strings.TrimSpace(string(body)) != want {
			t.Fatalf("%s: status %d: %s", path, status, body)
		}
	}
}

func TestInvalidRequests(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server)
	v := createVariant(t, server, p.ID, "M", "Merah", 1)
	products := server.URL + "/products"
	categories := server.URL + "/categories"
	variants := fmt.Sprintf("%s/%d/variants", products, p.ID)
	variant := fmt.Sprintf("%s/variants/%d", server.URL, v.ID)

	tests := []struct {
		name, method, url, body string
		want                    int
	}{
		{"malformed JSON", http.MethodPost, products, `{`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, products, `{"name":"a","price":1,"sku":"x"}`, http.StatusBadRequest},
		{"stock on a product", http.MethodPost, products, `{"name":"a","price":1,"stock":1}`, http.StatusBadRequest},
		{"blank name", http.MethodPost, products, `{"name":"  ","price":1}`, http.StatusBadRequest},
		{"long name", http.MethodPost, products, `{"name":"` + strings.Repeat("a", 201) + `","price":1}`, http.StatusBadRequest},
		{"NUL in name", http.MethodPost, products, `{"name":"a\u0000b","price":1}`, http.StatusBadRequest},
		{"NUL in description", http.MethodPost, products, `{"name":"a","description":"a\u0000b","price":1}`, http.StatusBadRequest},
		{"missing price", http.MethodPost, products, `{"name":"a"}`, http.StatusBadRequest},
		{"negative price", http.MethodPost, products, `{"name":"a","price":-1}`, http.StatusBadRequest},
		{"zero category_id", http.MethodPost, products, `{"name":"a","price":1,"category_id":0}`, http.StatusBadRequest},
		{"unknown category_id", http.MethodPost, products, `{"name":"a","price":1,"category_id":999999}`, http.StatusBadRequest},
		{"bad id", http.MethodGet, products + "/abc", "", http.StatusBadRequest},
		{"zero id", http.MethodGet, products + "/0", "", http.StatusBadRequest},
		{"bad limit", http.MethodGet, products + "?limit=0", "", http.StatusBadRequest},
		{"limit too large", http.MethodGet, products + "?limit=101", "", http.StatusBadRequest},
		{"bad offset", http.MethodGet, products + "?offset=-1", "", http.StatusBadRequest},
		{"bad category filter", http.MethodGet, products + "?category_id=abc", "", http.StatusBadRequest},
		{"body too large", http.MethodPost, products, `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
		{"update missing product", http.MethodPut, products + "/999999", `{"name":"a","price":1}`, http.StatusNotFound},

		{"blank category", http.MethodPost, categories, `{"name":" "}`, http.StatusBadRequest},
		{"update missing category", http.MethodPut, categories + "/999999", `{"name":"a"}`, http.StatusNotFound},

		{"missing size", http.MethodPost, variants, `{"color":"Merah"}`, http.StatusBadRequest},
		{"missing color", http.MethodPost, variants, `{"size":"M"}`, http.StatusBadRequest},
		{"long size", http.MethodPost, variants, `{"size":"` + strings.Repeat("a", 51) + `","color":"Merah"}`, http.StatusBadRequest},
		{"negative variant stock", http.MethodPost, variants, `{"size":"S","color":"Merah","stock":-1}`, http.StatusBadRequest},
		{"variant stock too large", http.MethodPost, variants, `{"size":"S","color":"Merah","stock":1000000001}`, http.StatusBadRequest},
		{"variant of missing product", http.MethodPost, products + "/999999/variants", `{"size":"S","color":"Merah"}`, http.StatusNotFound},
		{"stock in variant update", http.MethodPut, variant, `{"size":"M","color":"Merah","stock":9}`, http.StatusBadRequest},
		{"update missing variant", http.MethodPut, server.URL + "/variants/999999", `{"size":"S","color":"Merah"}`, http.StatusNotFound},
		{"zero delta", http.MethodPatch, variant + "/stock", `{"delta":0}`, http.StatusBadRequest},
		{"delta overflows int32", http.MethodPatch, variant + "/stock", `{"delta":3000000000}`, http.StatusBadRequest},
		{"stock of missing variant", http.MethodPatch, server.URL + "/variants/999999/stock", `{"delta":1}`, http.StatusNotFound},
		{"old stock endpoint", http.MethodPatch, fmt.Sprintf("%s/%d/stock", products, p.ID), `{"delta":1}`, http.StatusNotFound},
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
	p := createProduct(t, server)
	v := createVariant(t, server, p.ID, "M", "Merah", 2)
	url := fmt.Sprintf("%s/variants/%d/stock", server.URL, v.ID)

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

func TestStockIsSeparatePerVariant(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server)
	m := createVariant(t, server, p.ID, "M", "Merah", 5)
	l := createVariant(t, server, p.ID, "L", "Merah", 3)

	url := fmt.Sprintf("%s/variants/%d/stock", server.URL, m.ID)
	if status, body := request(t, http.MethodPatch, url, `{"delta":-5}`); status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	status, body := request(t, http.MethodGet, fmt.Sprintf("%s/variants/%d", server.URL, l.ID), "")
	if status != http.StatusOK || !strings.Contains(string(body), `"stock":3`) {
		t.Fatalf("other variant changed: status %d: %s", status, body)
	}
}

func TestConcurrentOrdersCannotOversell(t *testing.T) {
	server := newTestServer(t)
	p := createProduct(t, server)
	v := createVariant(t, server, p.ID, "M", "Merah", 10)
	url := fmt.Sprintf("%s/variants/%d/stock", server.URL, v.ID)

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
