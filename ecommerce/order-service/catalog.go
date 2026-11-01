package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var errUnknownVariant = errors.New("a variant in this order doesn't exist")

// Catalog reads prices when an order is made. The stock is checked later, by the saga.
type Catalog struct {
	url    string
	client *http.Client
}

func newCatalog(url string) *Catalog {
	return &Catalog{url: url, client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *Catalog) get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errUnknownVariant
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("catalog-service answered %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (c *Catalog) Item(ctx context.Context, variantID int64, quantity int32) (OrderItem, error) {
	var variant struct {
		ProductID int64  `json:"product_id"`
		Size      string `json:"size"`
		Color     string `json:"color"`
	}
	if err := c.get(ctx, fmt.Sprintf("/variants/%d", variantID), &variant); err != nil {
		return OrderItem{}, err
	}
	var product struct {
		Name  string `json:"name"`
		Price int64  `json:"price"`
	}
	if err := c.get(ctx, fmt.Sprintf("/products/%d", variant.ProductID), &product); err != nil {
		return OrderItem{}, err
	}
	return OrderItem{
		VariantID:   variantID,
		ProductID:   variant.ProductID,
		ProductName: product.Name,
		Size:        variant.Size,
		Color:       variant.Color,
		Price:       product.Price,
		Quantity:    quantity,
	}, nil
}
