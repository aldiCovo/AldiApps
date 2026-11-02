package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testKeyID = "test-key"

type harness struct {
	*httptest.Server
	private ed25519.PrivateKey
}

func newHarness(t *testing.T) *harness {
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
	if _, err := pool.Exec(ctx, "TRUNCATE cart_items"); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":%q,"x":%q}]}`, testKeyID, base64.RawURLEncoding.EncodeToString(public))
	}))
	t.Cleanup(users.Close)
	server := httptest.NewServer(newHandler(&Store{pool: pool}, newVerifier(users.URL)))
	t.Cleanup(server.Close)
	return &harness{Server: server, private: private}
}

func (h *harness) token(t *testing.T, userID int64) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{
		Issuer:    tokenIssuer,
		Audience:  jwt.ClaimStrings{tokenAudience},
		Subject:   strconv.FormatInt(userID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	token.Header["kid"] = testKeyID
	signed, err := token.SignedString(h.private)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func (h *harness) do(t *testing.T, userID int64, method, path, body string) (int, cartResponse) {
	t.Helper()
	req, err := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if userID != 0 {
		req.Header.Set("Authorization", "Bearer "+h.token(t, userID))
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
	var cart cartResponse
	if resp.StatusCode == http.StatusOK && len(data) > 0 {
		if err := json.Unmarshal(data, &cart); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, cart
}

func quantities(cart cartResponse) map[int64]int32 {
	found := map[int64]int32{}
	for _, item := range cart.Items {
		found[item.VariantID] = item.Quantity
	}
	return found
}

func TestEveryCartRequestNeedsALogin(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/cart"}, {"POST", "/cart/merge"}, {"PUT", "/cart/items/1"}, {"DELETE", "/cart/items/1"}, {"DELETE", "/cart"},
	} {
		if code, _ := h.do(t, 0, c.method, c.path, `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status %d", c.method, c.path, code)
		}
	}
}

func TestAnEmptyCartIsAnEmptyList(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest("GET", h.URL+"/cart", nil)
	req.Header.Set("Authorization", "Bearer "+h.token(t, 1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), `"items":[]`) {
		t.Fatalf("body %s", data)
	}
}

func TestMergeAddsAndCapsQuantities(t *testing.T) {
	h := newHarness(t)
	code, cart := h.do(t, 1, "POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":10,"quantity":3},{"product_id":2,"variant_id":20,"quantity":1}]}`)
	if code != http.StatusOK || quantities(cart)[10] != 3 || quantities(cart)[20] != 1 {
		t.Fatalf("status %d, cart %+v", code, cart)
	}
	_, cart = h.do(t, 1, "POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":10,"quantity":2}]}`)
	if quantities(cart)[10] != 5 {
		t.Fatalf("cart %+v", cart)
	}
	_, cart = h.do(t, 1, "POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":10,"quantity":20}]}`)
	if quantities(cart)[10] != maxQuantity {
		t.Fatalf("cart %+v", cart)
	}
}

func TestItemsKeepTheOrderTheyWereAdded(t *testing.T) {
	h := newHarness(t)
	for _, id := range []int{30, 10, 20} {
		h.do(t, 1, "POST", "/cart/merge", fmt.Sprintf(`{"items":[{"product_id":1,"variant_id":%d,"quantity":1}]}`, id))
	}
	_, cart := h.do(t, 1, "PUT", "/cart/items/10", `{"product_id":1,"quantity":5}`)
	if cart.Items[0].VariantID != 30 || cart.Items[1].VariantID != 10 || cart.Items[2].VariantID != 20 {
		t.Fatalf("cart %+v", cart)
	}
}

func fill(t *testing.T, h *harness, userID int64, count int) {
	t.Helper()
	items := make([]string, count)
	for i := range items {
		items[i] = fmt.Sprintf(`{"product_id":1,"variant_id":%d,"quantity":1}`, i+1)
	}
	if code, _ := h.do(t, userID, "POST", "/cart/merge", `{"items":[`+strings.Join(items, ",")+`]}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
}

func TestAFullCartSkipsNewVariantsButStillAddsToExistingOnes(t *testing.T) {
	h := newHarness(t)
	fill(t, h, 1, maxItems)
	code, cart := h.do(t, 1, "POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":500,"quantity":1},{"product_id":1,"variant_id":1,"quantity":1}]}`)
	if code != http.StatusOK || cart.Skipped != 1 || len(cart.Items) != maxItems || quantities(cart)[1] != 2 {
		t.Fatalf("status %d, skipped %d, cart %+v", code, cart.Skipped, cart.Items)
	}
	if code, _ := h.do(t, 1, "PUT", "/cart/items/500", `{"product_id":1,"quantity":1}`); code != http.StatusConflict {
		t.Fatalf("new variant: status %d", code)
	}
	if code, _ := h.do(t, 1, "PUT", "/cart/items/1", `{"product_id":1,"quantity":4}`); code != http.StatusOK {
		t.Fatalf("existing variant: status %d", code)
	}
}

func TestConcurrentMergesCantPassTheLimit(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			h.do(t, 1, "POST", "/cart/merge", fmt.Sprintf(`{"items":[{"product_id":1,"variant_id":%d,"quantity":1}]}`, i+1))
		})
	}
	wg.Wait()
	if _, cart := h.do(t, 1, "GET", "/cart", ""); len(cart.Items) != maxItems {
		t.Fatalf("%d items", len(cart.Items))
	}
}

func TestSetRemoveAndClear(t *testing.T) {
	h := newHarness(t)
	fill(t, h, 1, 3)
	_, cart := h.do(t, 1, "PUT", "/cart/items/1", `{"product_id":1,"quantity":7}`)
	if quantities(cart)[1] != 7 {
		t.Fatalf("set: %+v", cart)
	}
	_, cart = h.do(t, 1, "DELETE", "/cart/items/1", "")
	if _, found := quantities(cart)[1]; found || len(cart.Items) != 2 {
		t.Fatalf("remove: %+v", cart)
	}
	if code, _ := h.do(t, 1, "DELETE", "/cart/items/1", ""); code != http.StatusOK {
		t.Fatalf("removing twice: status %d", code)
	}
	if code, _ := h.do(t, 1, "DELETE", "/cart", ""); code != http.StatusOK {
		t.Fatalf("clear: status %d", code)
	}
	if _, cart := h.do(t, 1, "GET", "/cart", ""); len(cart.Items) != 0 {
		t.Fatalf("after clear: %+v", cart)
	}
}

func TestCartsAreSeparatePerUser(t *testing.T) {
	h := newHarness(t)
	fill(t, h, 1, 2)
	fill(t, h, 2, 1)
	h.do(t, 2, "DELETE", "/cart/items/1", "")
	h.do(t, 2, "PUT", "/cart/items/2", `{"product_id":1,"quantity":9}`)
	h.do(t, 2, "DELETE", "/cart", "")
	_, cart := h.do(t, 1, "GET", "/cart", "")
	if len(cart.Items) != 2 || quantities(cart)[2] != 1 {
		t.Fatalf("cart %+v", cart)
	}
}

func TestBadInputIsRefused(t *testing.T) {
	h := newHarness(t)
	many := make([]string, maxItems+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"product_id":1,"variant_id":%d,"quantity":1}`, i+1)
	}
	for name, c := range map[string]struct{ method, path, body string }{
		"no items":      {"POST", "/cart/merge", `{"items":[]}`},
		"too many":      {"POST", "/cart/merge", `{"items":[` + strings.Join(many, ",") + `]}`},
		"repeat":        {"POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":1,"quantity":1},{"product_id":1,"variant_id":1,"quantity":1}]}`},
		"zero quantity": {"POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":1,"quantity":0}]}`},
		"big quantity":  {"POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":1,"quantity":21}]}`},
		"no product":    {"POST", "/cart/merge", `{"items":[{"variant_id":1,"quantity":1}]}`},
		"unknown field": {"POST", "/cart/merge", `{"items":[{"product_id":1,"variant_id":1,"quantity":1,"price":1}]}`},
		"not json":      {"POST", "/cart/merge", `nope`},
		"bad id":        {"PUT", "/cart/items/abc", `{"product_id":1,"quantity":1}`},
		"zero id":       {"DELETE", "/cart/items/0", ""},
		"set to zero":   {"PUT", "/cart/items/1", `{"product_id":1,"quantity":0}`},
	} {
		if code, _ := h.do(t, 1, c.method, c.path, c.body); code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", name, code)
		}
	}
}
