package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	attachProduct = "product"
	attachOrder   = "order"
)

var (
	// The product is gone, or the order isn't the customer's.
	errCannotAttach = errors.New("that product or order can't be attached")
	// catalog-service or order-service didn't answer.
	errLookupFailed = errors.New("the product or order could not be checked")
)

// Attachment is a product or order card sent with a message, kept as it was when sent so the
// conversation still reads right after prices or statuses change.
type Attachment struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
	// The product's name, or the first item of the order.
	Name string `json:"name"`
	// Products: the price when sent, and the category whose photo stands in for missing ones.
	Price      int64  `json:"price,omitempty"`
	CategoryID *int64 `json:"category_id,omitempty"`
	// Orders: the status and total when sent, and how many other items it has beside Name.
	Status string `json:"status,omitempty"`
	Total  int64  `json:"total,omitempty"`
	More   int    `json:"more,omitempty"`
}

// attachInput is what a sender asks to attach.
type attachInput struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

// attacher builds cards. Tests replace it so they need neither catalog nor orders.
type attacher interface {
	Product(ctx context.Context, id int64) (*Attachment, error)
	// Order looks the order up as the customer, so another customer's order is not found.
	Order(ctx context.Context, id int64, bearer string) (*Attachment, error)
}

// Lookup asks catalog-service and order-service.
type Lookup struct {
	catalogURL string
	orderURL   string
	client     *http.Client
}

func newLookup(catalogURL, orderURL string) *Lookup {
	return &Lookup{catalogURL: catalogURL, orderURL: orderURL, client: &http.Client{Timeout: 5 * time.Second}}
}

func (l *Lookup) get(ctx context.Context, url, bearer string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errLookupFailed, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errCannotAttach
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: %s answered %d", errLookupFailed, url, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into); err != nil {
		return fmt.Errorf("%w: %v", errLookupFailed, err)
	}
	return nil
}

func (l *Lookup) Product(ctx context.Context, id int64) (*Attachment, error) {
	var product struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Price      int64  `json:"price"`
		CategoryID *int64 `json:"category_id"`
	}
	if err := l.get(ctx, l.catalogURL+"/products/"+strconv.FormatInt(id, 10), "", &product); err != nil {
		return nil, err
	}
	return &Attachment{Kind: attachProduct, ID: product.ID, Name: product.Name, Price: product.Price, CategoryID: product.CategoryID}, nil
}

func (l *Lookup) Order(ctx context.Context, id int64, bearer string) (*Attachment, error) {
	var order struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Total  int64  `json:"total"`
		Items  []struct {
			ProductName string `json:"product_name"`
		} `json:"items"`
	}
	if err := l.get(ctx, l.orderURL+"/orders/"+strconv.FormatInt(id, 10), bearer, &order); err != nil {
		return nil, err
	}
	card := &Attachment{Kind: attachOrder, ID: order.ID, Status: order.Status, Total: order.Total}
	if len(order.Items) > 0 {
		card.Name = order.Items[0].ProductName
		card.More = len(order.Items) - 1
	}
	return card, nil
}
