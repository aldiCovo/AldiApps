package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testKeyID = "test-key"

type harness struct {
	*httptest.Server
	store      *Store
	private    ed25519.PrivateKey
	keyFetches *atomic.Int32
	catalog    *httptest.Server
}

func newHarness(t *testing.T) *harness {
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
	ctx := context.Background()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE orders, order_items, outbox_events RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fetches := &atomic.Int32{}
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":%q,"x":%q}]}`, testKeyID, base64.RawURLEncoding.EncodeToString(public))
	}))
	t.Cleanup(users.Close)
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/variants/1":
			io.WriteString(w, `{"id":1,"product_id":10,"size":"M","color":"Merah","stock":5}`)
		case "/variants/2":
			io.WriteString(w, `{"id":2,"product_id":20,"size":"L","color":"Navy","stock":5}`)
		case "/products/10":
			io.WriteString(w, `{"id":10,"name":"Dress","price":350000}`)
		case "/products/20":
			io.WriteString(w, `{"id":20,"name":"Blouse","price":100000}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(catalog.Close)
	store := &Store{pool: pool, paymentWindow: time.Minute}
	server := httptest.NewServer(newHandler(store, newVerifier(users.URL), newCatalog(catalog.URL)))
	t.Cleanup(server.Close)
	return &harness{Server: server, store: store, private: private, keyFetches: fetches, catalog: catalog}
}

func claims(userID int64) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		Issuer:    tokenIssuer,
		Audience:  jwt.ClaimStrings{tokenAudience},
		Subject:   strconv.FormatInt(userID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
}

func sign(t *testing.T, key ed25519.PrivateKey, c jwt.RegisteredClaims, kid string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func (h *harness) token(t *testing.T, userID int64) string {
	t.Helper()
	return sign(t, h.private, claims(userID), testKeyID)
}

func (h *harness) do(t *testing.T, method, path, body, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func (h *harness) create(t *testing.T, userID int64, body string) Order {
	t.Helper()
	status, data := h.do(t, http.MethodPost, "/orders", body, h.token(t, userID))
	if status != http.StatusCreated {
		t.Fatalf("status %d: %s", status, data)
	}
	var order Order
	if err := json.Unmarshal(data, &order); err != nil {
		t.Fatal(err)
	}
	return order
}

func (h *harness) status(t *testing.T, userID, id int64) Order {
	t.Helper()
	status, data := h.do(t, http.MethodGet, fmt.Sprintf("/orders/%d", id), "", h.token(t, userID))
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, data)
	}
	var order Order
	if err := json.Unmarshal(data, &order); err != nil {
		t.Fatal(err)
	}
	return order
}

func (h *harness) outbox(t *testing.T) []string {
	t.Helper()
	rows, err := h.store.pool.Query(context.Background(), `SELECT type FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		types = append(types, eventType)
	}
	return types
}

func event(t *testing.T, eventType string, orderID int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"id": 1, "type": eventType, "data": map[string]any{"order_id": orderID, "reason": "out_of_stock"}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (h *harness) apply(t *testing.T, eventType string, orderID int64) {
	t.Helper()
	// Twice, because RabbitMQ can deliver an event twice.
	for range 2 {
		if err := h.store.handleEvent(context.Background(), event(t, eventType, orderID)); err != nil {
			t.Fatal(err)
		}
	}
}

const oneDress = `{"items":[{"variant_id":1,"quantity":2}]}`

func TestCreateCopiesPricesAndAsksForStock(t *testing.T) {
	h := newHarness(t)
	order := h.create(t, 1, `{"items":[{"variant_id":1,"quantity":2},{"variant_id":2,"quantity":1}]}`)
	if order.Status != statusPending || order.Total != 800000 || len(order.Items) != 2 {
		t.Fatalf("order %+v", order)
	}
	if item := order.Items[0]; item.ProductName != "Dress" || item.Size != "M" || item.Color != "Merah" || item.Price != 350000 {
		t.Fatalf("item %+v", item)
	}
	var data string
	if err := h.store.pool.QueryRow(context.Background(), `SELECT data::text FROM outbox_events WHERE type = $1`, eventOrderCreated).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, `"variant_id": 1`) || !strings.Contains(data, `"quantity": 2`) {
		t.Fatalf("event %s", data)
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)
	many := make([]string, maxItems+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"variant_id":%d,"quantity":1}`, i+1)
	}
	for name, body := range map[string]string{
		"no items":       `{"items":[]}`,
		"too many items": `{"items":[` + strings.Join(many, ",") + `]}`,
		"zero quantity":  `{"items":[{"variant_id":1,"quantity":0}]}`,
		"large quantity": `{"items":[{"variant_id":1,"quantity":21}]}`,
		"bad variant":    `{"items":[{"variant_id":0,"quantity":1}]}`,
		"same twice":     `{"items":[{"variant_id":1,"quantity":1},{"variant_id":1,"quantity":1}]}`,
		"unknown":        `{"items":[{"variant_id":999,"quantity":1}]}`,
		"price sent":     `{"items":[{"variant_id":1,"quantity":1,"price":1}]}`,
		"user sent":      `{"user_id":9,"items":[{"variant_id":1,"quantity":1}]}`,
		"not JSON":       `nope`,
	} {
		t.Run(name, func(t *testing.T) {
			if status, data := h.do(t, http.MethodPost, "/orders", body, h.token(t, 1)); status != http.StatusBadRequest {
				t.Fatalf("status %d: %s", status, data)
			}
		})
	}
	if got := h.outbox(t); len(got) != 0 {
		t.Fatalf("events %v", got)
	}
}

func TestSuccessfulSaga(t *testing.T) {
	h := newHarness(t)
	order := h.create(t, 1, oneDress)
	if status, _ := h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", order.ID), `{"simulate":"success"}`, h.token(t, 1)); status != http.StatusConflict {
		t.Fatalf("paying before the stock is reserved: status %d", status)
	}
	h.apply(t, eventStockReserved, order.ID)
	got := h.status(t, 1, order.ID)
	if got.Status != statusAwaitingPayment || got.ExpiresAt == nil {
		t.Fatalf("order %+v", got)
	}
	status, data := h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", order.ID), `{"simulate":"success"}`, h.token(t, 1))
	if status != http.StatusAccepted {
		t.Fatalf("status %d: %s", status, data)
	}
	if status, _ := h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", order.ID), `{"simulate":"success"}`, h.token(t, 1)); status != http.StatusConflict {
		t.Fatalf("second payment: status %d", status)
	}
	h.apply(t, eventPaymentSucceeded, order.ID)
	if got := h.status(t, 1, order.ID); got.Status != statusPaid {
		t.Fatalf("order %+v", got)
	}
	want := []string{eventOrderCreated, eventPaymentRequested}
	if got := h.outbox(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %v", got)
	}
}

func TestFailedPaymentReturnsStock(t *testing.T) {
	h := newHarness(t)
	order := h.create(t, 1, oneDress)
	h.apply(t, eventStockReserved, order.ID)
	if status, data := h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", order.ID), `{"simulate":"failure"}`, h.token(t, 1)); status != http.StatusAccepted {
		t.Fatalf("status %d: %s", status, data)
	}
	h.apply(t, eventPaymentFailed, order.ID)
	if got := h.status(t, 1, order.ID); got.Status != statusCancelled || got.Reason != reasonPaymentFailed {
		t.Fatalf("order %+v", got)
	}
	want := []string{eventOrderCreated, eventPaymentRequested, eventOrderCancelled}
	if got := h.outbox(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %v", got)
	}
}

func TestRejectedStockEndsTheOrder(t *testing.T) {
	h := newHarness(t)
	order := h.create(t, 1, oneDress)
	h.apply(t, eventStockRejected, order.ID)
	if got := h.status(t, 1, order.ID); got.Status != statusRejected || got.Reason != "out_of_stock" {
		t.Fatalf("order %+v", got)
	}
	// Nothing was reserved, so there is nothing to release.
	if got := h.outbox(t); len(got) != 1 {
		t.Fatalf("events %v", got)
	}
}

func TestLateEventsCannotUndoAFinalStatus(t *testing.T) {
	h := newHarness(t)
	order := h.create(t, 1, oneDress)
	h.apply(t, eventStockReserved, order.ID)
	h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", order.ID), `{"simulate":"success"}`, h.token(t, 1))
	h.apply(t, eventPaymentSucceeded, order.ID)
	for _, late := range []string{eventStockReserved, eventStockRejected, eventPaymentFailed} {
		h.apply(t, late, order.ID)
	}
	if got := h.status(t, 1, order.ID); got.Status != statusPaid {
		t.Fatalf("order %+v", got)
	}
}

func TestUnpaidOrderExpiresAndPayingOneDoesNot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	unpaid := h.create(t, 1, oneDress)
	paying := h.create(t, 1, oneDress)
	waiting := h.create(t, 1, oneDress)
	for _, order := range []Order{unpaid, paying, waiting} {
		h.apply(t, eventStockReserved, order.ID)
	}
	h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", paying.ID), `{"simulate":"success"}`, h.token(t, 1))
	if _, err := h.store.pool.Exec(ctx, `UPDATE orders SET expires_at = now() - interval '1 second' WHERE id = $1`, unpaid.ID); err != nil {
		t.Fatal(err)
	}
	// A late payment must not beat the expiry check.
	if status, _ := h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/pay", unpaid.ID), `{"simulate":"success"}`, h.token(t, 1)); status != http.StatusConflict {
		t.Fatalf("late payment: status %d", status)
	}
	expired, err := h.store.ExpireUnpaid(ctx)
	if err != nil || expired != 1 {
		t.Fatalf("expired %d, error %v", expired, err)
	}
	if got := h.status(t, 1, unpaid.ID); got.Status != statusCancelled || got.Reason != reasonExpired {
		t.Fatalf("order %+v", got)
	}
	if got := h.status(t, 1, paying.ID); got.Status != statusPaying {
		t.Fatalf("paying order %+v", got)
	}
	if got := h.status(t, 1, waiting.ID); got.Status != statusAwaitingPayment {
		t.Fatalf("waiting order %+v", got)
	}
	cancels := 0
	for _, eventType := range h.outbox(t) {
		if eventType == eventOrderCancelled {
			cancels++
		}
	}
	if cancels != 1 {
		t.Fatalf("%d cancel events", cancels)
	}
}

func TestOrdersBelongToTheirUser(t *testing.T) {
	h := newHarness(t)
	order := h.create(t, 1, oneDress)
	h.apply(t, eventStockReserved, order.ID)
	path := fmt.Sprintf("/orders/%d", order.ID)
	if status, _ := h.do(t, http.MethodGet, path, "", h.token(t, 2)); status != http.StatusNotFound {
		t.Fatalf("get: status %d", status)
	}
	if status, _ := h.do(t, http.MethodPost, path+"/pay", `{"simulate":"success"}`, h.token(t, 2)); status != http.StatusNotFound {
		t.Fatalf("pay: status %d", status)
	}
	_, data := h.do(t, http.MethodGet, "/orders", "", h.token(t, 2))
	if strings.TrimSpace(string(data)) != `{"orders":[]}` {
		t.Fatalf("list %s", data)
	}
	_, data = h.do(t, http.MethodGet, "/orders", "", h.token(t, 1))
	if !strings.Contains(string(data), `"product_name":"Dress"`) {
		t.Fatalf("list %s", data)
	}
	if got := h.status(t, 1, order.ID); got.Status != statusAwaitingPayment {
		t.Fatalf("order %+v", got)
	}
}

func TestOpenOrderLimitHoldsUnderConcurrency(t *testing.T) {
	h := newHarness(t)
	const attempts = maxOpenOrders + 5
	statuses := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() { statuses[i], _ = h.do(t, http.MethodPost, "/orders", oneDress, h.token(t, 1)) })
	}
	wg.Wait()
	created := 0
	for _, status := range statuses {
		if status == http.StatusCreated {
			created++
		} else if status != http.StatusConflict {
			t.Fatalf("statuses %v", statuses)
		}
	}
	if created != maxOpenOrders {
		t.Fatalf("created %d", created)
	}
	// Another user has their own limit.
	h.create(t, 2, oneDress)
}

func TestBadTokensAreRefused(t *testing.T) {
	h := newHarness(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	expired := claims(1)
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	noExpiry := claims(1)
	noExpiry.ExpiresAt = nil
	wrongIssuer := claims(1)
	wrongIssuer.Issuer = "other"
	wrongAudience := claims(1)
	wrongAudience.Audience = jwt.ClaimStrings{"other"}
	badSubject := claims(1)
	badSubject.Subject = "abc"
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims(1)).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{
		"none":           "",
		"garbage":        "nope",
		"other key":      sign(t, other, claims(1), testKeyID),
		"unknown key id": sign(t, h.private, claims(1), "other"),
		"alg none":       none,
		"expired":        sign(t, h.private, expired, testKeyID),
		"no expiry":      sign(t, h.private, noExpiry, testKeyID),
		"wrong issuer":   sign(t, h.private, wrongIssuer, testKeyID),
		"wrong audience": sign(t, h.private, wrongAudience, testKeyID),
		"bad subject":    sign(t, h.private, badSubject, testKeyID),
	} {
		t.Run(name, func(t *testing.T) {
			if status, data := h.do(t, http.MethodGet, "/orders", "", token); status != http.StatusUnauthorized {
				t.Fatalf("status %d: %s", status, data)
			}
		})
	}
}

func TestUnknownKeyIDsCannotFloodUserService(t *testing.T) {
	h := newHarness(t)
	for i := range 20 {
		h.do(t, http.MethodGet, "/orders", "", sign(t, h.private, claims(1), fmt.Sprintf("unknown-%d", i)))
	}
	if got := h.keyFetches.Load(); got != 1 {
		t.Fatalf("%d fetches", got)
	}
}

func TestCatalogDownAnswers503(t *testing.T) {
	h := newHarness(t)
	h.token(t, 1)
	h.catalog.Close()
	if status, data := h.do(t, http.MethodPost, "/orders", oneDress, h.token(t, 1)); status != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", status, data)
	}
	if got := h.outbox(t); len(got) != 0 {
		t.Fatalf("events %v", got)
	}
}

func TestBadEventsAreDropped(t *testing.T) {
	h := newHarness(t)
	for _, body := range [][]byte{
		[]byte(`nope`),
		event(t, eventStockReserved, 0),
		event(t, "stock.other", 1),
		event(t, eventStockReserved, 999),
	} {
		if err := h.store.handleEvent(context.Background(), body); err != nil {
			t.Fatal(err)
		}
	}
}

type sentSignal struct {
	userID, orderID int64
	status          string
}

func TestStatusChangesSendOneSignalToTheOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var signals []sentSignal
	h.store.notify = func(userID, orderID int64, status string) {
		signals = append(signals, sentSignal{userID, orderID, status})
	}
	expect := func(want ...sentSignal) {
		t.Helper()
		if !slices.Equal(signals, want) {
			t.Errorf("signals = %v, want %v", signals, want)
		}
		signals = nil
	}
	create := func() int64 {
		t.Helper()
		order, err := h.store.Create(ctx, 7, []OrderItem{{VariantID: 1, Price: 100, Quantity: 1}})
		if err != nil {
			t.Fatal(err)
		}
		expect(sentSignal{7, order.ID, statusPending})
		return order.ID
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	paid := create()
	check(h.store.MarkReserved(ctx, paid))
	check(h.store.MarkReserved(ctx, paid))
	expect(sentSignal{7, paid, statusAwaitingPayment})
	_, err := h.store.Pay(ctx, 7, paid, outcomeOK)
	check(err)
	expect(sentSignal{7, paid, statusPaying})
	check(h.store.MarkPaid(ctx, paid))
	check(h.store.MarkPaid(ctx, paid))
	expect(sentSignal{7, paid, statusPaid})

	rejected := create()
	check(h.store.MarkRejected(ctx, rejected, "out_of_stock"))
	expect(sentSignal{7, rejected, statusRejected})

	failed := create()
	check(h.store.MarkReserved(ctx, failed))
	_, err = h.store.Pay(ctx, 7, failed, outcomeFailed)
	check(err)
	signals = nil
	check(h.store.CancelPaying(ctx, failed))
	check(h.store.CancelPaying(ctx, failed))
	expect(sentSignal{7, failed, statusCancelled})

	expired := create()
	check(h.store.MarkReserved(ctx, expired))
	signals = nil
	if _, err := h.store.pool.Exec(ctx, `UPDATE orders SET expires_at = now() - interval '1 second' WHERE id = $1`, expired); err != nil {
		t.Fatal(err)
	}
	count, err := h.store.ExpireUnpaid(ctx)
	check(err)
	if count != 1 {
		t.Errorf("expired %d orders, want 1", count)
	}
	expect(sentSignal{7, expired, statusCancelled})
}

func TestARefusedOrderSendsNoSignal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.notify = func(userID, orderID int64, status string) {
		t.Errorf("unexpected signal for order %d: %s", orderID, status)
	}
	if _, err := h.store.Pay(ctx, 7, 999, outcomeOK); !errors.Is(err, errNotFound) {
		t.Errorf("want errNotFound, got %v", err)
	}
}
