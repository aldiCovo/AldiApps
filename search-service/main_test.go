package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const testIndex = "products-test"

func int64Ptr(n int64) *int64 { return &n }

func product(id int64, categoryID *int64, name, description string, variants ...Variant) Product {
	return Product{ID: id, CategoryID: categoryID, Name: name, Description: description, Price: 100000, Variants: variants}
}

func variant(size, color string, stock int32) Variant {
	return Variant{Size: size, Color: color, Stock: stock}
}

// fakeCatalog answers GET /products like catalog-service does.
func fakeCatalog(t *testing.T, products []Product) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		page := []Product{}
		if offset < len(products) {
			page = products[offset:min(offset+limit, len(products))]
		}
		writeJSON(w, http.StatusOK, map[string]any{"products": page})
	}))
	t.Cleanup(server.Close)
	return server
}

type testEnv struct {
	elastic  *Elastic
	search   *httptest.Server
	rabbitmq string
	ch       *amqp.Channel
}

// newTestEnv needs throwaway servers, because it deletes the test index and queue.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	elasticURL, rabbitmqURL := os.Getenv("TEST_ELASTICSEARCH_URL"), os.Getenv("TEST_RABBITMQ_URL")
	if elasticURL == "" || rabbitmqURL == "" {
		t.Skip("TEST_ELASTICSEARCH_URL and TEST_RABBITMQ_URL are not set")
	}
	elastic := newElastic(elasticURL, testIndex)
	if _, _, err := elastic.do(context.Background(), http.MethodDelete, "/"+testIndex, nil); err != nil {
		t.Fatal(err)
	}

	conn, err := amqp.Dial(rabbitmqURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.ExchangeDeclare(eventExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.QueueDelete(eventQueue, false, false, false); err != nil {
		t.Fatal(err)
	}

	search := httptest.NewServer(newHandler(elastic))
	t.Cleanup(search.Close)
	return &testEnv{elastic: elastic, search: search, rabbitmq: rabbitmqURL, ch: ch}
}

// startIndexer returns a function that stops it and waits until it has.
func (env *testEnv) startIndexer(t *testing.T, catalog *httptest.Server) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Indexer{elastic: env.elastic, rabbitmqURL: env.rabbitmq, catalogURL: catalog.URL}).Run(ctx)
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop
}

func (env *testEnv) publish(t *testing.T, id int64, eventType string, data any) {
	t.Helper()
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	env.publishRaw(t, eventType, mustJSON(t, Event{ID: id, Type: eventType, Data: body}))
}

func (env *testEnv) publishRaw(t *testing.T, routingKey string, body []byte) {
	t.Helper()
	err := env.ch.PublishWithContext(context.Background(), eventExchange, routingKey, false, false,
		amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body})
	if err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type searchResult struct {
	Total    int64     `json:"total"`
	Products []Product `json:"products"`
}

func (env *testEnv) find(t *testing.T, query string) searchResult {
	t.Helper()
	resp, err := http.Get(env.search.URL + "/search?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return searchResult{Total: -1}
	}
	var result searchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func ids(result searchResult) []int64 {
	got := make([]int64, len(result.Products))
	for i, p := range result.Products {
		got[i] = p.ID
	}
	sort.Slice(got, func(a, b int) bool { return got[a] < got[b] })
	return got
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// waitFor retries because Elasticsearch makes changes searchable within about a second.
func (env *testEnv) waitFor(t *testing.T, query string, want ...int64) searchResult {
	t.Helper()
	if want == nil {
		want = []int64{}
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		result := env.find(t, query)
		if sameIDs(ids(result), want) {
			return result
		}
		if time.Now().After(deadline) {
			t.Fatalf("search %q returned %v, want %v", query, ids(result), want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func fashionCatalog() []Product {
	dress, atasan := int64Ptr(1), int64Ptr(2)
	return []Product{
		product(1, dress, "Dress Batik Wanita", "Dress midi lengan pendek", variant("M", "Merah", 4), variant("L", "Navy", 0)),
		product(2, atasan, "Blouse Satin Wanita", "Blouse kerja lengan panjang", variant("S", "Putih", 2)),
		product(3, nil, "Rok Plisket Wanita", "Rok panjang bahan ringan", variant("All Size", "Hitam", 0)),
	}
}

func TestSearchFindsSyncedProducts(t *testing.T) {
	env := newTestEnv(t)
	env.startIndexer(t, fakeCatalog(t, fashionCatalog()))
	env.waitFor(t, "", 1, 2, 3)

	tests := []struct {
		name, query string
		want        []int64
	}{
		{"word in the name", "q=dress", []int64{1}},
		{"different case", "q=DRESS", []int64{1}},
		{"typo", "q=dres", []int64{1}},
		{"word in the description", "q=midi", []int64{1}},
		{"word on many products", "q=wanita", []int64{1, 2, 3}},
		{"name and color together", "q=dress+merah", []int64{1}},
		{"all words must match", "q=dress+putih", []int64{}},
		{"no match", "q=sepatu", []int64{}},
		{"category", "category_id=2", []int64{2}},
		{"unknown category", "category_id=99", []int64{}},
		{"size", "size=m", []int64{1}},
		{"color", "color=PUTIH", []int64{2}},
		{"size with a space", "size=" + url.QueryEscape("all size"), []int64{3}},
		{"size and color on one variant", "size=M&color=Merah", []int64{1}},
		{"size and color on different variants", "size=M&color=Navy", []int64{}},
		{"in stock", "in_stock=true", []int64{1, 2}},
		{"color that has no stock", "color=Navy&in_stock=true", []int64{}},
		{"not limited to stock", "in_stock=false&color=Navy", []int64{1}},
		{"text with a filter", "q=wanita&category_id=1", []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(env.find(t, tt.query)); !sameIDs(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("paging keeps the total", func(t *testing.T) {
		first := env.find(t, "limit=2")
		second := env.find(t, "limit=2&offset=2")
		if first.Total != 3 || second.Total != 3 || len(first.Products) != 2 || len(second.Products) != 1 {
			t.Fatalf("first %+v, second %+v", first, second)
		}
	})

	t.Run("the search text stays internal", func(t *testing.T) {
		resp, err := http.Get(env.search.URL + "/search?q=dress")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw struct {
			Products []map[string]any `json:"products"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if _, leaked := raw.Products[0]["search_text"]; leaked || raw.Products[0]["name"] != "Dress Batik Wanita" {
			t.Fatalf("unexpected document: %v", raw.Products[0])
		}
	})
}

func TestInvalidSearches(t *testing.T) {
	env := newTestEnv(t)
	long := url.QueryEscape(fmt.Sprintf("%0201d", 0))
	for _, query := range []string{
		"limit=0", "limit=101", "limit=abc", "offset=-1", "offset=9901",
		"category_id=0", "category_id=abc", "in_stock=maybe", "q=" + long, "size=" + long, "color=" + long,
	} {
		resp, err := http.Get(env.search.URL + "/search?" + query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d", query, resp.StatusCode)
		}
	}
}

func TestSearchBeforeTheFirstSyncIsUnavailable(t *testing.T) {
	env := newTestEnv(t)
	resp, err := http.Get(env.search.URL + "/search")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestEventsKeepTheIndexCurrent(t *testing.T) {
	env := newTestEnv(t)
	env.startIndexer(t, fakeCatalog(t, fashionCatalog()))
	env.waitFor(t, "", 1, 2, 3)

	env.publish(t, 10, eventProductCreated, product(4, nil, "Hijab Voal Premium", "Hijab segi empat", variant("All Size", "Krem", 5)))
	env.waitFor(t, "q=hijab", 4)

	env.publish(t, 20, eventProductUpdated, product(1, int64Ptr(1), "Gaun Batik Wanita", "Gaun midi"))
	env.waitFor(t, "q=gaun", 1)
	env.waitFor(t, "q=dress")

	// A duplicate and an older event must not undo the newer one.
	env.publish(t, 20, eventProductUpdated, product(1, int64Ptr(1), "Duplikat Sama", ""))
	env.publish(t, 15, eventProductUpdated, product(1, int64Ptr(1), "Event Lama", ""))

	env.publish(t, 30, eventProductDeleted, map[string]int64{"id": 2})
	env.waitFor(t, "q=blouse")

	// Neither may an older event bring back a deleted product, nor a repeated delete fail.
	env.publish(t, 25, eventProductUpdated, product(2, nil, "Blouse Lama", ""))
	env.publish(t, 30, eventProductDeleted, map[string]int64{"id": 2})
	env.publish(t, 31, eventProductDeleted, map[string]int64{"id": 999})

	// Events the service can't use are dropped and don't block what follows.
	env.publishRaw(t, "product.updated", []byte(`not json`))
	env.publish(t, 32, eventProductUpdated, "not a product")
	env.publish(t, 33, "product.renamed", map[string]int64{"id": 1})
	env.publish(t, 34, eventProductCreated, product(5, nil, "Kemeja Linen", ""))
	env.waitFor(t, "q=kemeja", 5)

	env.waitFor(t, "q=gaun", 1)
	env.waitFor(t, "q=lama")
	env.waitFor(t, "q=duplikat")
}

func TestEventsWaitInTheQueueWhileTheServiceIsDown(t *testing.T) {
	env := newTestEnv(t)
	catalog := fakeCatalog(t, fashionCatalog())
	stop := env.startIndexer(t, catalog)
	env.waitFor(t, "", 1, 2, 3)
	stop()

	// These two change the same product, so the second must win after the restart.
	env.publish(t, 40, eventProductUpdated, product(3, nil, "Rok Pertama", ""))
	env.publish(t, 41, eventProductUpdated, product(3, nil, "Rok Terakhir", ""))
	env.publish(t, 42, eventProductDeleted, map[string]int64{"id": 2})

	// The catalog still lists the old rok and the deleted blouse, as a stale read could.
	env.startIndexer(t, catalog)
	env.waitFor(t, "q=terakhir", 3)
	env.waitFor(t, "q=pertama")
	env.waitFor(t, "", 1, 3)
}

func TestSyncDoesNotReplaceNewerEventsAndHealsAnEmptyIndex(t *testing.T) {
	env := newTestEnv(t)
	catalog := fakeCatalog(t, fashionCatalog())
	stop := env.startIndexer(t, catalog)
	env.waitFor(t, "", 1, 2, 3)
	env.publish(t, 50, eventProductUpdated, product(1, nil, "Gaun Baru", ""))
	env.waitFor(t, "q=baru", 1)
	stop()

	stop = env.startIndexer(t, catalog)
	env.waitFor(t, "", 1, 2, 3)
	time.Sleep(1500 * time.Millisecond)
	env.waitFor(t, "q=baru", 1)
	stop()

	if _, _, err := env.elastic.do(context.Background(), http.MethodDelete, "/"+testIndex, nil); err != nil {
		t.Fatal(err)
	}
	env.startIndexer(t, catalog)
	env.waitFor(t, "", 1, 2, 3)
}

func TestSyncLoadsEveryPage(t *testing.T) {
	env := newTestEnv(t)
	products := make([]Product, syncPageSize+5)
	want := make([]int64, len(products))
	for i := range products {
		products[i] = product(int64(i+1), nil, "Produk", "")
		want[i] = int64(i + 1)
	}
	env.startIndexer(t, fakeCatalog(t, products))
	deadline := time.Now().Add(30 * time.Second)
	for env.find(t, "limit=1").Total != int64(len(want)) {
		if time.Now().After(deadline) {
			t.Fatalf("indexed %d of %d", env.find(t, "limit=1").Total, len(want))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestIndexerStopsCleanlyWithoutDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Indexer{
			elastic:     newElastic("http://127.0.0.1:1", testIndex),
			rabbitmqURL: "amqp://guest:guest@127.0.0.1:1/",
			catalogURL:  "http://127.0.0.1:1",
		}).Run(ctx)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("indexer did not stop")
	}
}
