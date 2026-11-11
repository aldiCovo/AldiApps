package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func (h *harness) favorites(t *testing.T, userID int64, method, path, body string) (int, favoritesResponse) {
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
	var got favoritesResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, got
}

func TestEveryFavoritesRequestNeedsALogin(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/favorites"}, {"POST", "/favorites/merge"}, {"PUT", "/favorites/1"}, {"DELETE", "/favorites/1"},
	} {
		if code, _ := h.favorites(t, 0, c.method, c.path, `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status %d", c.method, c.path, code)
		}
	}
}

func TestFavorites(t *testing.T) {
	h := newHarness(t)
	if code, got := h.favorites(t, 1, "GET", "/favorites", ""); code != http.StatusOK || got.ProductIDs == nil || len(got.ProductIDs) != 0 {
		t.Fatalf("empty: %d %+v", code, got)
	}
	h.favorites(t, 1, "PUT", "/favorites/5", "")
	h.favorites(t, 1, "PUT", "/favorites/7", "")
	// Saving again keeps its place.
	code, got := h.favorites(t, 1, "PUT", "/favorites/5", "")
	if code != http.StatusOK || !slices.Equal(got.ProductIDs, []int64{7, 5}) {
		t.Fatalf("after adding: %d %+v", code, got)
	}
	// Another account's list is its own.
	if _, got := h.favorites(t, 2, "GET", "/favorites", ""); len(got.ProductIDs) != 0 {
		t.Fatalf("account 2 sees %v", got.ProductIDs)
	}
	if _, got := h.favorites(t, 1, "DELETE", "/favorites/7", ""); !slices.Equal(got.ProductIDs, []int64{5}) {
		t.Fatalf("after removing: %v", got.ProductIDs)
	}
	// Removing one that isn't there is fine.
	if code, _ := h.favorites(t, 1, "DELETE", "/favorites/7", ""); code != http.StatusOK {
		t.Fatalf("removing again: %d", code)
	}
	// A guest's list joins, its newest staying newest, and one already saved isn't doubled.
	code, got = h.favorites(t, 1, "POST", "/favorites/merge", `{"product_ids":[8,5,9]}`)
	if code != http.StatusOK || !slices.Equal(got.ProductIDs, []int64{8, 9, 5}) || got.Skipped != 0 {
		t.Fatalf("after merge: %d %+v", code, got)
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/favorites/0", ""}, {"PUT", "/favorites/x", ""}, {"DELETE", "/favorites/-1", ""},
		{"POST", "/favorites/merge", `{"product_ids":[]}`}, {"POST", "/favorites/merge", `{"product_ids":[3,3]}`},
		{"POST", "/favorites/merge", `{"product_ids":[0]}`}, {"POST", "/favorites/merge", `{"ids":[1]}`},
	} {
		if code, _ := h.favorites(t, 1, c.method, c.path, c.body); code != http.StatusBadRequest {
			t.Errorf("%s %s %s: status %d", c.method, c.path, c.body, code)
		}
	}
}

func TestFavoritesLimit(t *testing.T) {
	h := newHarness(t)
	ids := make([]string, maxFavorites)
	for i := range ids {
		ids[i] = fmt.Sprint(i + 1)
	}
	code, got := h.favorites(t, 1, "POST", "/favorites/merge", `{"product_ids":[`+strings.Join(ids, ",")+`]}`)
	if code != http.StatusOK || len(got.ProductIDs) != maxFavorites {
		t.Fatalf("filling: %d, %d saved", code, len(got.ProductIDs))
	}
	if code, _ := h.favorites(t, 1, "PUT", "/favorites/1000", ""); code != http.StatusConflict {
		t.Fatalf("past the limit: status %d", code)
	}
	// One already saved can still be saved again.
	if code, _ := h.favorites(t, 1, "PUT", "/favorites/1", ""); code != http.StatusOK {
		t.Fatalf("saving one already saved when full: status %d", code)
	}
	code, got = h.favorites(t, 1, "POST", "/favorites/merge", `{"product_ids":[1000,1001,2]}`)
	if code != http.StatusOK || got.Skipped != 2 || len(got.ProductIDs) != maxFavorites {
		t.Fatalf("merging into a full list: %d skipped %d", code, got.Skipped)
	}
}
