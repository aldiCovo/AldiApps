package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const productIndex = "products"

// errIndexMissing means the first sync hasn't created the index yet.
var errIndexMissing = errors.New("search index does not exist yet")

// indexDefinition keeps deleted products' versions for a day (gc_deletes), so a late
// duplicate of an older event can't bring back a deleted product. Variants are nested so
// a size and a color must match on the same variant.
const indexDefinition = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "index.gc_deletes": "1d",
    "analysis": {"normalizer": {"lowercase_keyword": {"type": "custom", "filter": ["lowercase"]}}}
  },
  "mappings": {
    "dynamic": false,
    "properties": {
      "id": {"type": "long"},
      "category_id": {"type": "long"},
      "name": {"type": "text"},
      "search_text": {"type": "text"},
      "price": {"type": "long"},
      "variants": {
        "type": "nested",
        "properties": {
          "size": {"type": "keyword", "normalizer": "lowercase_keyword"},
          "color": {"type": "keyword", "normalizer": "lowercase_keyword"},
          "stock": {"type": "integer"}
        }
      }
    }
  }
}`

type Variant struct {
	ID        int64     `json:"id"`
	ProductID int64     `json:"product_id"`
	Size      string    `json:"size"`
	Color     string    `json:"color"`
	Stock     int32     `json:"stock"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Product struct {
	ID          int64     `json:"id"`
	CategoryID  *int64    `json:"category_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Price       int64     `json:"price"`
	Variants    []Variant `json:"variants"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// productDocument adds the text searched by q, so "dress merah" can match across fields.
type productDocument struct {
	Product
	SearchText string `json:"search_text"`
}

func newProductDocument(p Product) productDocument {
	words := []string{p.Name, p.Description}
	for _, v := range p.Variants {
		words = append(words, v.Size, v.Color)
	}
	return productDocument{Product: p, SearchText: strings.Join(words, " ")}
}

type Elastic struct {
	baseURL string
	index   string
	client  *http.Client
}

func newElastic(baseURL, index string) *Elastic {
	return &Elastic{
		baseURL: strings.TrimRight(baseURL, "/"),
		index:   index,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (e *Elastic) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, err
}

func (e *Elastic) Ping(ctx context.Context) error {
	status, body, err := e.do(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("elasticsearch answered %d: %s", status, body)
	}
	return nil
}

func (e *Elastic) EnsureIndex(ctx context.Context) error {
	status, body, err := e.do(ctx, http.MethodPut, "/"+e.index, []byte(indexDefinition))
	if err != nil {
		return err
	}
	if status == http.StatusOK || bytes.Contains(body, []byte("resource_already_exists_exception")) {
		return nil
	}
	return fmt.Errorf("creating the index: elasticsearch answered %d: %s", status, body)
}

// IndexProduct stores p with the event's id as its version. Elasticsearch answers 409 when
// it already has that version or a newer one, which means there is nothing to do.
func (e *Elastic) IndexProduct(ctx context.Context, p Product, version int64) error {
	body, err := json.Marshal(newProductDocument(p))
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/%s/_doc/%d?version=%d&version_type=external", e.index, p.ID, version)
	status, respBody, err := e.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusConflict:
		return nil
	}
	return fmt.Errorf("indexing product %d: elasticsearch answered %d: %s", p.ID, status, respBody)
}

// DeleteProduct treats 404 and 409 like IndexProduct treats 409: there is nothing to do.
func (e *Elastic) DeleteProduct(ctx context.Context, id, version int64) error {
	path := fmt.Sprintf("/%s/_doc/%d?version=%d&version_type=external", e.index, id, version)
	status, body, err := e.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusNotFound, http.StatusConflict:
		return nil
	}
	return fmt.Errorf("deleting product %d: elasticsearch answered %d: %s", id, status, body)
}

type SearchQuery struct {
	Text       string
	CategoryID *int64
	Size       string
	Color      string
	InStock    bool
	Limit      int
	Offset     int
}

type object = map[string]any

func (q SearchQuery) body() ([]byte, error) {
	must := []object{}
	should := []object{}
	if q.Text != "" {
		must = append(must, object{"match": object{"search_text": object{
			"query": q.Text, "operator": "and", "fuzziness": "AUTO",
		}}})
		should = append(should, object{"match": object{"name": object{"query": q.Text, "boost": 2}}})
	}

	filter := []object{}
	if q.CategoryID != nil {
		filter = append(filter, object{"term": object{"category_id": *q.CategoryID}})
	}
	variant := []object{}
	if q.Size != "" {
		variant = append(variant, object{"term": object{"variants.size": q.Size}})
	}
	if q.Color != "" {
		variant = append(variant, object{"term": object{"variants.color": q.Color}})
	}
	if q.InStock {
		variant = append(variant, object{"range": object{"variants.stock": object{"gt": 0}}})
	}
	if len(variant) > 0 {
		filter = append(filter, object{"nested": object{
			"path":  "variants",
			"query": object{"bool": object{"filter": variant}},
		}})
	}

	return json.Marshal(object{
		"query":            object{"bool": object{"must": must, "should": should, "filter": filter}},
		"sort":             []any{object{"_score": "desc"}, object{"id": "asc"}},
		"from":             q.Offset,
		"size":             q.Limit,
		"track_total_hits": true,
		"_source":          object{"excludes": []string{"search_text"}},
	})
}

func (e *Elastic) Search(ctx context.Context, q SearchQuery) (int64, []json.RawMessage, error) {
	body, err := q.body()
	if err != nil {
		return 0, nil, err
	}
	status, respBody, err := e.do(ctx, http.MethodPost, "/"+e.index+"/_search", body)
	if err != nil {
		return 0, nil, err
	}
	if status == http.StatusNotFound {
		return 0, nil, errIndexMissing
	}
	if status != http.StatusOK {
		return 0, nil, fmt.Errorf("searching: elasticsearch answered %d: %s", status, respBody)
	}
	var result struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, nil, err
	}
	products := make([]json.RawMessage, len(result.Hits.Hits))
	for i, hit := range result.Hits.Hits {
		products[i] = hit.Source
	}
	return result.Hits.Total.Value, products, nil
}
