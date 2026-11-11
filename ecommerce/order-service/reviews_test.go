package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// arrivedOrder takes a paid order through packing and shipping to arrived.
func (h *harness) arrivedOrder(t *testing.T, userID int64) Order {
	t.Helper()
	order := h.paidOrder(t, userID)
	for _, body := range []string{`{"status":"packed"}`, `{"status":"shipped","tracking_number":"JP1234567890"}`, `{"status":"arrived"}`} {
		if status, _ := h.advance(t, order.ID, body); status != http.StatusOK {
			t.Fatalf("%s: status %d", body, status)
		}
	}
	return order
}

func (h *harness) receive(t *testing.T, userID, id int64) (int, Order) {
	t.Helper()
	status, data := h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/receive", id), "", h.token(t, userID))
	var order Order
	if status == http.StatusOK {
		if err := json.Unmarshal(data, &order); err != nil {
			t.Fatal(err)
		}
	}
	return status, order
}

func (h *harness) review(t *testing.T, userID, orderID, productID int64, body string) int {
	t.Helper()
	status, _ := h.do(t, http.MethodPut, fmt.Sprintf("/orders/%d/reviews/%d", orderID, productID), body, h.token(t, userID))
	return status
}

func TestCustomerReceivesAnArrivedOrder(t *testing.T) {
	h := newHarness(t)
	shipped := h.paidOrder(t, 1)
	if status, _ := h.receive(t, 1, shipped.ID); status != http.StatusConflict {
		t.Fatalf("receiving a paid order: status %d", status)
	}
	order := h.arrivedOrder(t, 1)
	if status, _ := h.receive(t, 2, order.ID); status != http.StatusNotFound {
		t.Fatalf("someone else's order: status %d", status)
	}
	status, got := h.receive(t, 1, order.ID)
	if status != http.StatusOK || got.Status != statusCompleted || got.CompletedBy != completedByCustomer || got.CompletedAt == nil || len(got.Items) != 1 {
		t.Fatalf("status %d, order %+v", status, got)
	}
	if status, _ := h.receive(t, 1, order.ID); status != http.StatusConflict {
		t.Fatalf("receiving twice: status %d", status)
	}
	completed := 0
	for _, event := range h.outbox(t) {
		if event == eventOrderCompleted {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("%d completed events", completed)
	}
}

func TestArrivedOrdersCompleteByThemselvesAfterTheWindow(t *testing.T) {
	h := newHarness(t)
	late := h.arrivedOrder(t, 1)
	recent := h.arrivedOrder(t, 2)
	if _, err := h.store.pool.Exec(t.Context(), `UPDATE orders SET arrived_at = now() - interval '72 hours 1 minute' WHERE id = $1`, late.ID); err != nil {
		t.Fatal(err)
	}
	done, err := h.store.CompleteArrived(t.Context())
	if err != nil || done != 1 {
		t.Fatalf("completed %d, %v", done, err)
	}
	if got := h.status(t, 1, late.ID); got.Status != statusCompleted || got.CompletedBy != completedBySystem {
		t.Fatalf("late order %+v", got)
	}
	if got := h.status(t, 2, recent.ID); got.Status != statusArrived || got.AutoCompleteAt == nil {
		t.Fatalf("recent order %+v", got)
	}
	if done, err := h.store.CompleteArrived(t.Context()); err != nil || done != 0 {
		t.Fatalf("second run completed %d, %v", done, err)
	}
}

func TestOnlyBuyersOfCompletedOrdersReview(t *testing.T) {
	h := newHarness(t)
	order := h.arrivedOrder(t, 1)
	if status := h.review(t, 1, order.ID, 10, `{"rating":5}`); status != http.StatusConflict {
		t.Fatalf("reviewing before receiving: status %d", status)
	}
	h.receive(t, 1, order.ID)
	for name, c := range map[string]struct {
		userID, productID int64
		body              string
		status            int
	}{
		"someone else":   {2, 10, `{"rating":5}`, http.StatusNotFound},
		"not bought":     {1, 20, `{"rating":5}`, http.StatusNotFound},
		"no stars":       {1, 10, `{"rating":0}`, http.StatusBadRequest},
		"six stars":      {1, 10, `{"rating":6}`, http.StatusBadRequest},
		"too long":       {1, 10, fmt.Sprintf(`{"rating":4,"body":%q}`, strings.Repeat("a", 1001)), http.StatusBadRequest},
		"unknown fields": {1, 10, `{"rating":4,"author":"Admin"}`, http.StatusBadRequest},
	} {
		if status := h.review(t, c.userID, order.ID, c.productID, c.body); status != c.status {
			t.Fatalf("%s: status %d, want %d", name, status, c.status)
		}
	}
	if status := h.review(t, 1, order.ID, 10, `{"rating":3,"body":"  Bahannya adem  "}`); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	// Changing it keeps one review.
	if status := h.review(t, 1, order.ID, 10, `{"rating":5,"body":"Bagus sekali"}`); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	mine := h.status(t, 1, order.ID)
	if review, ok := mine.Reviews[10]; !ok || review.Rating != 5 || review.Body != "Bagus sekali" {
		t.Fatalf("my reviews %+v", mine.Reviews)
	}

	status, data := h.do(t, http.MethodGet, "/products/10/reviews", "", "")
	var page ReviewPage
	if status != http.StatusOK || json.Unmarshal(data, &page) != nil {
		t.Fatalf("status %d: %s", status, data)
	}
	if page.Count != 1 || page.Average != 5 || page.Counts[4] != 1 || len(page.Reviews) != 1 ||
		page.Reviews[0].Author != "S***i" || page.Reviews[0].Variant != "M · Merah" || page.Reviews[0].Body != "Bagus sekali" {
		t.Fatalf("page %+v", page)
	}
	status, data = h.do(t, http.MethodGet, "/products/10/reviews?rating=1", "", "")
	if status != http.StatusOK || json.Unmarshal(data, &page) != nil || len(page.Reviews) != 0 || page.Matching != 0 || page.Count != 1 {
		t.Fatalf("status %d: %s", status, data)
	}
	for _, query := range []string{"rating=6", "limit=0", "limit=51", "offset=-1"} {
		if status, _ := h.do(t, http.MethodGet, "/products/10/reviews?"+query, "", ""); status != http.StatusBadRequest {
			t.Fatalf("%s: status %d", query, status)
		}
	}

	h.paidOrder(t, 2)
	status, data = h.do(t, http.MethodGet, "/ratings", "", "")
	var ratings struct{ Ratings map[int64]Rating }
	if status != http.StatusOK || json.Unmarshal(data, &ratings) != nil {
		t.Fatalf("status %d: %s", status, data)
	}
	// Two dresses in each of the two paid orders.
	if got := ratings.Ratings[10]; got.Average != 5 || got.Count != 1 || got.Sold != 4 {
		t.Fatalf("ratings %+v", ratings.Ratings)
	}
}

func TestMaskName(t *testing.T) {
	for name, want := range map[string]string{"Sari Dewi": "S***i", "rina": "R***a", "Al": "A***", "": "Pembeli", "  Bayu  ": "B***u", "Éva": "É***a"} {
		if got := maskName(name); got != want {
			t.Errorf("maskName(%q) = %q, want %q", name, got, want)
		}
	}
}
