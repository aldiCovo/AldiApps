package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeVoucher creates a voucher through the admin route. Fields in extra replace the
// defaults: 10% off, valid from an hour ago for a day, once per account.
func (h *harness) makeVoucher(t *testing.T, extra map[string]any) Voucher {
	t.Helper()
	body := map[string]any{
		"code": "HEMAT10", "kind": "percent", "value": 10, "per_user_limit": 1, "active": true,
		"starts_at": time.Now().Add(-time.Hour), "ends_at": time.Now().Add(24 * time.Hour),
	}
	for key, value := range extra {
		body[key] = value
	}
	data, _ := json.Marshal(body)
	status, answer := h.do(t, http.MethodPost, "/internal/vouchers", string(data), testInternalToken)
	if status != http.StatusCreated {
		t.Fatalf("status %d: %s", status, answer)
	}
	var v Voucher
	if err := json.Unmarshal(answer, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// withVoucher adds a voucher code to an order body.
func withVoucher(body, code string) string {
	return strings.Replace(body, "{", fmt.Sprintf(`{"voucher_code":%q,`, code), 1)
}

// refusal answers the reason a voucher was refused, or fails when it wasn't.
func refusal(t *testing.T, status int, data []byte) string {
	t.Helper()
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", status, data)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	return body.Reason
}

func TestVoucherDiscounts(t *testing.T) {
	limit := func(n int64) *int64 { return &n }
	for _, c := range []struct {
		name     string
		voucher  Voucher
		subtotal int64
		shipping int64
		want     int64
	}{
		{"percent", Voucher{Kind: voucherPercent, Value: 10}, 700000, 10000, 70000},
		{"percent capped", Voucher{Kind: voucherPercent, Value: 50, MaxDiscount: limit(50000)}, 700000, 10000, 50000},
		{"percent rounds down", Voucher{Kind: voucherPercent, Value: 15}, 999, 0, 149},
		{"amount", Voucher{Kind: voucherAmount, Value: 25000}, 700000, 10000, 25000},
		{"amount never more than the items", Voucher{Kind: voucherAmount, Value: 900000}, 700000, 10000, 700000},
		{"free shipping", Voucher{Kind: voucherFreeShipping}, 700000, 30000, 30000},
		{"free shipping capped", Voucher{Kind: voucherFreeShipping, MaxDiscount: limit(20000)}, 700000, 30000, 20000},
		{"free shipping before a courier", Voucher{Kind: voucherFreeShipping, MaxDiscount: limit(20000)}, 700000, 0, 0},
	} {
		if got := c.voucher.discount(c.subtotal, c.shipping); got != c.want {
			t.Errorf("%s: discount %d, want %d", c.name, got, c.want)
		}
	}
}

func TestAdminManagesVouchers(t *testing.T) {
	h := newHarness(t)
	v := h.makeVoucher(t, map[string]any{"code": " hemat10 ", "description": "Promo November"})
	if v.Code != "HEMAT10" || v.Description != "Promo November" || !v.Active {
		t.Fatalf("voucher %+v", v)
	}

	// Each refused for its own reason.
	for _, bad := range []map[string]any{
		{"code": "a!"},
		{"code": "OTHER", "value": 101},
		{"code": "OTHER", "kind": "amount", "value": 10000, "max_discount": 5000},
		{"code": "OTHER", "kind": "free_shipping", "value": 5},
		{"code": "OTHER", "kind": "cashback"},
		{"code": "OTHER", "per_user_limit": 0},
		{"code": "OTHER", "quota": 0},
		{"code": "OTHER", "ends_at": time.Now().Add(-2 * time.Hour)},
	} {
		body := map[string]any{"kind": "percent", "value": 10, "per_user_limit": 1, "starts_at": time.Now().Add(-time.Hour), "ends_at": time.Now().Add(time.Hour)}
		for key, value := range bad {
			body[key] = value
		}
		data, _ := json.Marshal(body)
		if status, answer := h.do(t, http.MethodPost, "/internal/vouchers", string(data), testInternalToken); status != http.StatusBadRequest {
			t.Errorf("%v: status %d: %s", bad, status, answer)
		}
	}
	data, _ := json.Marshal(map[string]any{"code": "hemat10", "kind": "amount", "value": 5000, "per_user_limit": 1,
		"starts_at": time.Now(), "ends_at": time.Now().Add(time.Hour)})
	if status, _ := h.do(t, http.MethodPost, "/internal/vouchers", string(data), testInternalToken); status != http.StatusConflict {
		t.Fatalf("a second HEMAT10: status %d", status)
	}

	data, _ = json.Marshal(map[string]any{"code": "HEMAT20", "kind": "percent", "value": 20, "per_user_limit": 2, "active": false,
		"starts_at": v.StartsAt, "ends_at": v.EndsAt})
	status, answer := h.do(t, http.MethodPut, fmt.Sprintf("/internal/vouchers/%d", v.ID), string(data), testInternalToken)
	if status != http.StatusOK || !strings.Contains(string(answer), `"code":"HEMAT20"`) || !strings.Contains(string(answer), `"active":false`) {
		t.Fatalf("update: status %d: %s", status, answer)
	}
	if status, _ := h.do(t, http.MethodPut, "/internal/vouchers/999", string(data), testInternalToken); status != http.StatusNotFound {
		t.Fatalf("updating a missing voucher: status %d", status)
	}

	status, answer = h.do(t, http.MethodGet, "/internal/vouchers", "", testInternalToken)
	var list struct{ Vouchers []Voucher }
	if status != http.StatusOK || json.Unmarshal(answer, &list) != nil || len(list.Vouchers) != 1 || list.Vouchers[0].Code != "HEMAT20" {
		t.Fatalf("list: status %d: %s", status, answer)
	}

	if status, _ := h.do(t, http.MethodDelete, fmt.Sprintf("/internal/vouchers/%d", v.ID), "", testInternalToken); status != http.StatusNoContent {
		t.Fatalf("delete: status %d", status)
	}
	if status, _ := h.do(t, http.MethodDelete, fmt.Sprintf("/internal/vouchers/%d", v.ID), "", testInternalToken); status != http.StatusNotFound {
		t.Fatalf("delete again: status %d", status)
	}
	for _, path := range []string{"/internal/vouchers", fmt.Sprintf("/internal/vouchers/%d", v.ID)} {
		if status, _ := h.do(t, http.MethodGet, path, "", h.token(t, 1)); status != http.StatusUnauthorized && status != http.StatusMethodNotAllowed {
			t.Fatalf("%s with a customer token: status %d", path, status)
		}
	}
}

func TestOrderWithVoucher(t *testing.T) {
	h := newHarness(t)
	h.makeVoucher(t, map[string]any{"code": "HEMAT10", "max_discount": 50000})
	h.makeVoucher(t, map[string]any{"code": "ONGKIR", "kind": "free_shipping", "value": 0})

	// Two dresses: 700.000, and JNE Reguler to Jawa Barat: 10.000.
	order := h.create(t, 1, withVoucher(oneDress, "hemat10"))
	if order.Discount != 50000 || order.VoucherCode != "HEMAT10" || order.Total != 660000 {
		t.Fatalf("order %+v", order)
	}
	order = h.create(t, 2, withVoucher(oneDress, "ONGKIR"))
	if order.Discount != 10000 || order.Total != 700000 {
		t.Fatalf("free shipping order %+v", order)
	}
	if got := h.status(t, 3, h.create(t, 3, oneDress).ID); got.Discount != 0 || got.VoucherCode != "" || got.Total != 710000 {
		t.Fatalf("order without a voucher %+v", got)
	}

	// The cancelled order's email says what was taken off.
	if _, err := h.store.Cancel(context.Background(), 1, 1); err == nil {
		t.Fatal("an order still pending was cancelled")
	}
	h.apply(t, eventStockReserved, 1)
	if _, err := h.store.Cancel(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := h.store.pool.QueryRow(context.Background(),
		`SELECT data FROM outbox_events WHERE type = $1 ORDER BY id DESC LIMIT 1`, eventOrderCancelled).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"discount": 50000`) || !strings.Contains(string(data), `"voucher_code": "HEMAT10"`) {
		t.Fatalf("event %s", data)
	}
}

func TestVoucherRefusals(t *testing.T) {
	h := newHarness(t)
	h.makeVoucher(t, map[string]any{"code": "OFF", "active": false})
	h.makeVoucher(t, map[string]any{"code": "LATER", "starts_at": time.Now().Add(time.Hour), "ends_at": time.Now().Add(2 * time.Hour)})
	h.makeVoucher(t, map[string]any{"code": "OVER", "starts_at": time.Now().Add(-2 * time.Hour), "ends_at": time.Now().Add(-time.Hour)})
	h.makeVoucher(t, map[string]any{"code": "BIG", "min_subtotal": 1000000})
	for code, want := range map[string]string{
		"NOPE": refusedNotFound, "OFF": refusedNotFound, "LATER": refusedNotStarted, "OVER": refusedExpired, "BIG": refusedMinSubtotal,
	} {
		status, data := h.do(t, http.MethodPost, "/orders", withVoucher(oneDress, code), h.token(t, 1))
		if got := refusal(t, status, data); got != want {
			t.Errorf("%s: reason %q, want %q", code, got, want)
		}
		if code == "BIG" && !strings.Contains(string(data), `"min_subtotal":1000000`) {
			t.Errorf("BIG doesn't say the minimum: %s", data)
		}
	}
	// None of them made an order.
	if status, data := h.do(t, http.MethodGet, "/orders", "", h.token(t, 1)); status != http.StatusOK || string(data) != "{\"orders\":[]}\n" {
		t.Fatalf("orders: %d %s", status, data)
	}
}

func TestVoucherLimitsAndGivingUsesBack(t *testing.T) {
	h := newHarness(t)
	h.makeVoucher(t, map[string]any{"code": "SEKALI", "quota": 2, "per_user_limit": 1})

	first := h.create(t, 1, withVoucher(oneDress, "SEKALI"))
	status, data := h.do(t, http.MethodPost, "/orders", withVoucher(oneDress, "SEKALI"), h.token(t, 1))
	if got := refusal(t, status, data); got != refusedAlreadyUsed {
		t.Fatalf("second use by the same account: %q", got)
	}
	h.create(t, 2, withVoucher(oneDress, "SEKALI"))
	status, data = h.do(t, http.MethodPost, "/orders", withVoucher(oneDress, "SEKALI"), h.token(t, 3))
	if got := refusal(t, status, data); got != refusedUsedUp {
		t.Fatalf("past the quota: %q", got)
	}

	// The first order runs out of stock, which gives its use back to the account and the quota.
	h.apply(t, eventStockRejected, first.ID)
	third := h.create(t, 3, withVoucher(oneDress, "SEKALI"))
	status, data = h.do(t, http.MethodPost, "/orders", withVoucher(oneDress, "SEKALI"), h.token(t, 1))
	if got := refusal(t, status, data); got != refusedUsedUp {
		t.Fatalf("account 1 after its order failed, with the quota full again: %q", got)
	}
	// A cancelled order gives its use back too.
	h.apply(t, eventStockReserved, third.ID)
	if _, err := h.store.Cancel(context.Background(), 3, third.ID); err != nil {
		t.Fatal(err)
	}
	h.create(t, 1, withVoucher(oneDress, "SEKALI"))

	// A used voucher can't be deleted, and the list counts the orders holding it.
	status, data = h.do(t, http.MethodGet, "/internal/vouchers", "", testInternalToken)
	if status != http.StatusOK || !strings.Contains(string(data), `"used":2`) {
		t.Fatalf("list: %d %s", status, data)
	}
	if status, _ := h.do(t, http.MethodDelete, "/internal/vouchers/1", "", testInternalToken); status != http.StatusConflict {
		t.Fatalf("deleting a used voucher: status %d", status)
	}
}

func TestVoucherQuotaHoldsUnderConcurrentOrders(t *testing.T) {
	h := newHarness(t)
	h.makeVoucher(t, map[string]any{"code": "REBUTAN", "quota": 1})
	var wg sync.WaitGroup
	statuses := make([]int, 8)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = h.do(t, http.MethodPost, "/orders", withVoucher(oneDress, "REBUTAN"), h.token(t, int64(i+1)))
		}()
	}
	wg.Wait()
	created := 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
		default:
			t.Fatalf("status %d", status)
		}
	}
	if created != 1 {
		t.Fatalf("%d orders got the voucher, want 1", created)
	}
}

func TestCheckVoucherShowsTheDiscountWithoutHoldingIt(t *testing.T) {
	h := newHarness(t)
	h.makeVoucher(t, map[string]any{"code": "ONGKIR", "kind": "free_shipping", "value": 0, "max_discount": 25000, "quota": 1})
	check := func(body string) (int, map[string]any) {
		status, data := h.do(t, http.MethodPost, "/vouchers/check", body, h.token(t, 1))
		var answer map[string]any
		_ = json.Unmarshal(data, &answer)
		return status, answer
	}
	// JNE Reguler to Papua is zone 4: 45.000, of which the voucher pays 25.000.
	status, answer := check(`{"voucher_code":"ongkir","items":[{"variant_id":1,"quantity":1}],"shipping_option":"jne-reg","province":"Papua"}`)
	if status != http.StatusOK || answer["discount"] != float64(25000) || answer["kind"] != "free_shipping" || answer["code"] != "ONGKIR" {
		t.Fatalf("status %d: %v", status, answer)
	}
	// Without a courier yet, there's no shipping to pay.
	if status, answer := check(`{"voucher_code":"ONGKIR","items":[{"variant_id":1,"quantity":1}]}`); status != http.StatusOK || answer["discount"] != float64(0) {
		t.Fatalf("status %d: %v", status, answer)
	}
	for _, body := range []string{
		`{"items":[{"variant_id":1,"quantity":1}]}`,
		`{"voucher_code":"ONGKIR","items":[]}`,
		`{"voucher_code":"ONGKIR","items":[{"variant_id":99,"quantity":1}]}`,
		`{"voucher_code":"ONGKIR","items":[{"variant_id":1,"quantity":1}],"shipping_option":"jne-reg","province":"Mars"}`,
	} {
		if status, _ := check(body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, status)
		}
	}
	if status, _ := h.do(t, http.MethodPost, "/vouchers/check", `{"voucher_code":"ONGKIR","items":[{"variant_id":1,"quantity":1}]}`, ""); status != http.StatusUnauthorized {
		t.Fatalf("without login: status %d", status)
	}
	// Checking held nothing: the only use is still there for an order.
	h.create(t, 1, withVoucher(oneDress, "ONGKIR"))
}

func TestBannerVoucher(t *testing.T) {
	h := newHarness(t)
	banner := func() string {
		t.Helper()
		status, data := h.do(t, http.MethodGet, "/vouchers/banner", "", "")
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, data)
		}
		var body struct {
			Voucher *struct {
				Code  string `json:"code"`
				Quota *int   `json:"quota"`
			} `json:"voucher"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		if body.Voucher == nil {
			return ""
		}
		if body.Voucher.Quota != nil {
			t.Fatalf("the banner shows the quota: %s", data)
		}
		return body.Voucher.Code
	}
	if got := banner(); got != "" {
		t.Fatalf("no vouchers, banner %q", got)
	}
	// None of these may be shown: not for the banner, turned off, not started, over.
	h.makeVoucher(t, map[string]any{"code": "BIASA"})
	h.makeVoucher(t, map[string]any{"code": "OFF", "banner": true, "active": false})
	h.makeVoucher(t, map[string]any{"code": "LATER", "banner": true, "starts_at": time.Now().Add(time.Hour), "ends_at": time.Now().Add(2 * time.Hour)})
	h.makeVoucher(t, map[string]any{"code": "OVER", "banner": true, "starts_at": time.Now().Add(-2 * time.Hour), "ends_at": time.Now().Add(-time.Hour)})
	if got := banner(); got != "" {
		t.Fatalf("banner %q, want none", got)
	}

	first := h.makeVoucher(t, map[string]any{"code": "PERTAMA", "banner": true, "quota": 1})
	if !first.Banner {
		t.Fatalf("voucher %+v", first)
	}
	if got := banner(); got != "PERTAMA" {
		t.Fatalf("banner %q, want PERTAMA", got)
	}
	// The most recently changed wins.
	h.makeVoucher(t, map[string]any{"code": "KEDUA", "banner": true})
	if got := banner(); got != "KEDUA" {
		t.Fatalf("banner %q, want KEDUA", got)
	}
	data, _ := json.Marshal(map[string]any{
		"code": "PERTAMA", "kind": "percent", "value": 10, "per_user_limit": 1, "active": true, "banner": true, "quota": 1,
		"starts_at": first.StartsAt, "ends_at": first.EndsAt,
	})
	if status, answer := h.do(t, http.MethodPut, fmt.Sprintf("/internal/vouchers/%d", first.ID), string(data), testInternalToken); status != http.StatusOK {
		t.Fatalf("status %d: %s", status, answer)
	}
	if got := banner(); got != "PERTAMA" {
		t.Fatalf("banner %q, want PERTAMA after it changed", got)
	}
	// Used up, it gives the banner back to the other.
	h.create(t, 1, withVoucher(oneDress, "PERTAMA"))
	if got := banner(); got != "KEDUA" {
		t.Fatalf("banner %q, want KEDUA once PERTAMA is used up", got)
	}
}

func myVouchers(t *testing.T, h *harness, userID int64) map[string]MyVoucher {
	t.Helper()
	status, data := h.do(t, http.MethodGet, "/vouchers/mine", "", h.token(t, userID))
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, data)
	}
	if strings.Contains(string(data), `"quota"`) || strings.Contains(string(data), `"user_id"`) {
		t.Fatalf("the customer sees more than the terms: %s", data)
	}
	var body struct {
		Vouchers []MyVoucher `json:"vouchers"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]MyVoucher{}
	for _, v := range body.Vouchers {
		out[v.Code] = v
	}
	return out
}

func TestVoucherAudience(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []map[string]any{
		{"code": "A1", "audience": "user"},
		{"code": "A2", "audience": "user", "user_id": 0},
		{"code": "A3", "audience": "all", "user_id": 2},
		{"code": "A4", "audience": "user", "user_id": 2, "banner": true},
		{"code": "A5", "audience": "friends"},
	} {
		data, _ := json.Marshal(map[string]any{
			"code": bad["code"], "kind": "percent", "value": 10, "per_user_limit": 1, "active": true,
			"starts_at": time.Now().Add(-time.Hour), "ends_at": time.Now().Add(24 * time.Hour),
			"audience": bad["audience"], "user_id": bad["user_id"], "banner": bad["banner"] == true,
		})
		if status, answer := h.do(t, http.MethodPost, "/internal/vouchers", string(data), testInternalToken); status != http.StatusBadRequest {
			t.Errorf("%v: status %d, %s", bad, status, answer)
		}
	}

	// Without an audience a voucher stays hidden, as before.
	plain := h.makeVoucher(t, map[string]any{"code": "RAHASIA"})
	if plain.Audience != audienceHidden || plain.UserID != nil {
		t.Fatalf("default audience: %+v", plain)
	}
	h.makeVoucher(t, map[string]any{"code": "SEMUA", "audience": "all", "per_user_limit": 2, "ends_at": time.Now().Add(48 * time.Hour)})
	mine := h.makeVoucher(t, map[string]any{"code": "UNTUKDUA", "audience": "user", "user_id": 2})
	if mine.Audience != audienceUser || mine.UserID == nil || *mine.UserID != 2 {
		t.Fatalf("personal voucher: %+v", mine)
	}
	h.makeVoucher(t, map[string]any{"code": "LAMA", "audience": "all", "starts_at": time.Now().Add(-48 * time.Hour), "ends_at": time.Now().Add(-time.Hour)})
	h.makeVoucher(t, map[string]any{"code": "KUNO", "audience": "all", "starts_at": time.Now().Add(-90 * 24 * time.Hour), "ends_at": time.Now().Add(-40 * 24 * time.Hour)})
	h.makeVoucher(t, map[string]any{"code": "NANTI", "audience": "all", "starts_at": time.Now().Add(time.Hour), "ends_at": time.Now().Add(2 * time.Hour)})
	h.makeVoucher(t, map[string]any{"code": "MATI", "audience": "all", "active": false})
	h.makeVoucher(t, map[string]any{"code": "HABIS", "audience": "all", "quota": 1})

	// Someone else can't use the personal one: to them it doesn't exist.
	status, data := h.do(t, http.MethodPost, "/orders", withVoucher(oneDress, "UNTUKDUA"), h.token(t, 3))
	if got := refusal(t, status, data); got != refusedNotFound {
		t.Fatalf("another customer: %q", got)
	}
	h.create(t, 3, withVoucher(oneDress, "HABIS"))
	order := h.create(t, 2, withVoucher(oneDress, "UNTUKDUA"))

	got := myVouchers(t, h, 2)
	want := map[string]string{"SEMUA": myVoucherUsable, "UNTUKDUA": myVoucherUsed, "LAMA": myVoucherEnded, "HABIS": myVoucherUsedUp}
	if len(got) != len(want) {
		t.Fatalf("customer 2 sees %v", got)
	}
	for code, state := range want {
		if got[code].State != state {
			t.Errorf("%s: state %q, want %q", code, got[code].State, state)
		}
	}
	if !got["UNTUKDUA"].ForYou || got["SEMUA"].ForYou || got["UNTUKDUA"].LastOrderID != order.ID || got["SEMUA"].UsesLeft != 2 || got["UNTUKDUA"].UsesLeft != 0 {
		t.Fatalf("details: %+v", got)
	}
	// Usable vouchers come first.
	status, data = h.do(t, http.MethodGet, "/vouchers/mine", "", h.token(t, 2))
	if status != http.StatusOK || !strings.HasPrefix(string(data), `{"vouchers":[{"code":"SEMUA"`) {
		t.Fatalf("order: %s", data)
	}
	if _, ok := myVouchers(t, h, 3)["UNTUKDUA"]; ok {
		t.Fatal("customer 3 sees customer 2's voucher")
	}
	if status, _ := h.do(t, http.MethodGet, "/vouchers/mine", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", status)
	}
	// Cancelling gives the use back.
	h.apply(t, eventStockRejected, order.ID)
	if v := myVouchers(t, h, 2)["UNTUKDUA"]; v.State != myVoucherUsable || v.UsesLeft != 1 {
		t.Fatalf("after the order failed: %+v", v)
	}
}
