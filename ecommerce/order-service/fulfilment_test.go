package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

const testInternalToken = "internal-test-token-0123456789abcdef"

// paidOrder makes an order for the user and takes it through the saga to paid.
func (h *harness) paidOrder(t *testing.T, userID int64) Order {
	t.Helper()
	order := h.create(t, userID, oneDress)
	h.apply(t, eventStockReserved, order.ID)
	if status, data := h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", order.ID), `{"simulate":"success"}`, h.token(t, userID)); status != http.StatusAccepted {
		t.Fatalf("status %d: %s", status, data)
	}
	h.apply(t, eventPaymentSucceeded, order.ID)
	return order
}

func (h *harness) advance(t *testing.T, id int64, body string) (int, AdminOrder) {
	t.Helper()
	status, data := h.do(t, http.MethodPost, fmt.Sprintf("/internal/orders/%d/status", id), body, testInternalToken)
	var order AdminOrder
	if status == http.StatusOK {
		if err := json.Unmarshal(data, &order); err != nil {
			t.Fatal(err)
		}
	}
	return status, order
}

func TestInternalRoutesNeedTheInternalToken(t *testing.T) {
	h := newHarness(t)
	order := h.paidOrder(t, 1)
	for _, token := range []string{"", "wrong", h.token(t, 1)} {
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/internal/orders", ""},
			{http.MethodGet, fmt.Sprintf("/internal/orders/%d", order.ID), ""},
			{http.MethodPost, fmt.Sprintf("/internal/orders/%d/status", order.ID), `{"status":"packed"}`},
		} {
			if status, _ := h.do(t, req.method, req.path, req.body, token); status != http.StatusUnauthorized {
				t.Fatalf("%s %s with %q: status %d", req.method, req.path, token, status)
			}
		}
	}
	if got := h.status(t, 1, order.ID); got.Status != statusPaid {
		t.Fatalf("order %+v", got)
	}
}

func TestAdminListsEveryonesOrdersWithTheirOwner(t *testing.T) {
	h := newHarness(t)
	first := h.paidOrder(t, 1)
	second := h.create(t, 2, oneDress)
	status, data := h.do(t, http.MethodGet, "/internal/orders", "", testInternalToken)
	var got struct{ Orders []AdminOrder }
	if status != http.StatusOK || json.Unmarshal(data, &got) != nil {
		t.Fatalf("status %d: %s", status, data)
	}
	if len(got.Orders) != 2 || got.Orders[0].ID != second.ID || got.Orders[0].UserID != 2 || got.Orders[1].UserID != 1 || len(got.Orders[1].Items) != 1 || got.Orders[1].ShippingAddress == nil {
		t.Fatalf("orders %+v", got.Orders)
	}
	status, data = h.do(t, http.MethodGet, "/internal/orders?status=paid", "", testInternalToken)
	if status != http.StatusOK || json.Unmarshal(data, &got) != nil || len(got.Orders) != 1 || got.Orders[0].ID != first.ID {
		t.Fatalf("status %d: %s", status, data)
	}
	for _, query := range []string{"status=lost", "limit=0", "limit=101", "offset=-1"} {
		if status, _ := h.do(t, http.MethodGet, "/internal/orders?"+query, "", testInternalToken); status != http.StatusBadRequest {
			t.Fatalf("%s: status %d", query, status)
		}
	}
	if status, _ := h.do(t, http.MethodGet, "/internal/orders/999", "", testInternalToken); status != http.StatusNotFound {
		t.Fatalf("unknown order: status %d", status)
	}
}

func TestPaidOrderIsPackedShippedAndCompleted(t *testing.T) {
	h := newHarness(t)
	order := h.paidOrder(t, 1)
	status, got := h.advance(t, order.ID, `{"status":"packed"}`)
	if status != http.StatusOK || got.Status != statusPacked || got.PackedAt == nil || got.ShippedAt != nil || got.UserID != 1 {
		t.Fatalf("status %d, order %+v", status, got)
	}
	status, got = h.advance(t, order.ID, `{"status":"shipped","tracking_number":" jp-1234567890 "}`)
	if status != http.StatusOK || got.Status != statusShipped || got.ShippedAt == nil || got.TrackingNumber != "JP-1234567890" {
		t.Fatalf("status %d, order %+v", status, got)
	}
	status, got = h.advance(t, order.ID, `{"status":"arrived"}`)
	if status != http.StatusOK || got.Status != statusArrived || got.ArrivedAt == nil || got.ArrivedBy != "admin" || got.AutoCompleteAt == nil ||
		got.AutoCompleteAt.Sub(*got.ArrivedAt) != receiveWindow {
		t.Fatalf("status %d, order %+v", status, got)
	}
	status, got = h.advance(t, order.ID, `{"status":"completed"}`)
	if status != http.StatusOK || got.Status != statusCompleted || got.CompletedAt == nil || got.CompletedBy != "admin" || got.AutoCompleteAt != nil ||
		got.PackedAt == nil || got.TrackingNumber != "JP-1234567890" {
		t.Fatalf("status %d, order %+v", status, got)
	}
	// The customer sees it too.
	if mine := h.status(t, 1, order.ID); mine.Status != statusCompleted || mine.TrackingNumber != "JP-1234567890" || mine.ShippedAt == nil {
		t.Fatalf("order %+v", mine)
	}
	want := []string{eventOrderCreated, eventPaymentRequested, eventOrderPaid, eventOrderShipped, eventOrderArrived, eventOrderCompleted}
	if got := h.outbox(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %v", got)
	}
	var event orderFinishedEvent
	if err := h.store.pool.QueryRow(t.Context(), `SELECT data FROM outbox_events WHERE type = $1`, eventOrderShipped).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if event.OrderID != order.ID || event.UserID != 1 || event.TrackingNumber != "JP-1234567890" || event.Shipping == nil || event.Shipping.Courier != "JNE" || len(event.Items) != 1 {
		t.Fatalf("event %+v", event)
	}
}

func TestStepsCannotBeSkippedRepeatedOrBadlyFormed(t *testing.T) {
	h := newHarness(t)
	order := h.paidOrder(t, 1)
	unpaid := h.create(t, 1, oneDress)
	for name, c := range map[string]struct {
		id     int64
		body   string
		status int
	}{
		"ship before packing":     {order.ID, `{"status":"shipped","tracking_number":"JP1234567"}`, http.StatusConflict},
		"complete before packing": {order.ID, `{"status":"completed"}`, http.StatusConflict},
		"arrive before packing":   {order.ID, `{"status":"arrived"}`, http.StatusConflict},
		"pack an unpaid order":    {unpaid.ID, `{"status":"packed"}`, http.StatusConflict},
		"back to paid":            {order.ID, `{"status":"paid"}`, http.StatusBadRequest},
		"cancel":                  {order.ID, `{"status":"cancelled"}`, http.StatusBadRequest},
		"tracking when packing":   {order.ID, `{"status":"packed","tracking_number":"JP1234567"}`, http.StatusBadRequest},
		"unknown field":           {order.ID, `{"status":"packed","note":"x"}`, http.StatusBadRequest},
		"unknown order":           {999, `{"status":"packed"}`, http.StatusNotFound},
	} {
		if status, _ := h.advance(t, c.id, c.body); status != c.status {
			t.Fatalf("%s: status %d, want %d", name, status, c.status)
		}
	}
	if status, _ := h.advance(t, order.ID, `{"status":"packed"}`); status != http.StatusOK {
		t.Fatalf("packing: status %d", status)
	}
	for name, body := range map[string]string{
		"no tracking":    `{"status":"shipped"}`,
		"short tracking": `{"status":"shipped","tracking_number":"AB12"}`,
		"odd tracking":   `{"status":"shipped","tracking_number":"JP 1234567"}`,
		"html tracking":  `{"status":"shipped","tracking_number":"<b>123456</b>"}`,
	} {
		if status, _ := h.advance(t, order.ID, body); status != http.StatusBadRequest {
			t.Fatalf("%s: status %d", name, status)
		}
	}
	if status, _ := h.advance(t, order.ID, `{"status":"packed"}`); status != http.StatusConflict {
		t.Fatalf("packing twice: status %d", status)
	}
	if got := h.status(t, 1, order.ID); got.Status != statusPacked {
		t.Fatalf("order %+v", got)
	}
}

func TestTwoAdminsPackingAtOnceChangeItOnce(t *testing.T) {
	h := newHarness(t)
	order := h.paidOrder(t, 1)
	var wg sync.WaitGroup
	results := make(chan int, 10)
	for range 10 {
		wg.Go(func() {
			status, _ := h.advance(t, order.ID, `{"status":"packed"}`)
			results <- status
		})
	}
	wg.Wait()
	close(results)
	ok := 0
	for status := range results {
		if status == http.StatusOK {
			ok++
		} else if status != http.StatusConflict {
			t.Fatalf("status %d", status)
		}
	}
	if ok != 1 {
		t.Fatalf("%d succeeded", ok)
	}
}

func TestLatePaymentEventsCannotUndoShipping(t *testing.T) {
	h := newHarness(t)
	order := h.paidOrder(t, 1)
	h.advance(t, order.ID, `{"status":"packed"}`)
	h.apply(t, eventPaymentSucceeded, order.ID)
	h.apply(t, eventPaymentFailed, order.ID)
	if got := h.status(t, 1, order.ID); got.Status != statusPacked {
		t.Fatalf("order %+v", got)
	}
}
