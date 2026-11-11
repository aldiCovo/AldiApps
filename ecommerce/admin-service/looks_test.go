package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func decodeLook(t *testing.T, body string) lookView {
	t.Helper()
	var v lookView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("look %q: %v", body, err)
	}
	return v
}

func TestLooks(t *testing.T) {
	server, f := newTestServer(t)
	for _, token := range []string{"", "customer-token"} {
		for _, route := range []struct{ method, path string }{
			{"GET", "/looks"}, {"POST", "/looks"}, {"PUT", "/looks/1"}, {"DELETE", "/looks/1"}, {"PUT", "/looks/1/photo"},
		} {
			if status, _ := do(t, server, route.method, route.path, token, `{}`); status != http.StatusUnauthorized && status != http.StatusForbidden {
				t.Fatalf("%s %s with %q: %d", route.method, route.path, token, status)
			}
		}
	}
	if status, body := do(t, server, "GET", "/looks", "admin-token", ""); status != http.StatusOK || body != "{\"looks\":[]}\n" {
		t.Fatalf("empty: %d %s", status, body)
	}

	status, body := do(t, server, "POST", "/looks", "admin-token",
		`{"eyebrow":" Shop the look ","title":"Rapi ke kantor","accent":"tetap nyaman","active":true,"items":[{"product_id":5,"x":72.04,"y":46}]}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	look := decodeLook(t, body)
	if look.ID != 1 || look.Eyebrow != "Shop the look" || look.Photo != "" || look.PhotoURL != "" ||
		!reflect.DeepEqual(look.Items, []LookItem{{ProductID: 5, X: 72, Y: 46}}) {
		t.Fatalf("created %+v", look)
	}

	// A product the catalog doesn't know can't be pointed at; nor can bad input pass.
	for _, bad := range []string{
		`{"title":"A","items":[{"product_id":404,"x":1,"y":1}]}`,
		`{"title":"","items":[]}`,
		`{"title":"A","items":[{"product_id":5,"x":101,"y":1}]}`,
		`{"title":"A","items":[{"product_id":5,"x":1,"y":1},{"product_id":5,"x":2,"y":2}]}`,
		`{"title":"A","photo":"../../x"}`,
		`{"title":"` + strings.Repeat("a", 81) + `"}`,
	} {
		if status, body := do(t, server, "PUT", "/looks/1", "admin-token", bad); status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Errorf("%s: %d %s", bad, status, body)
		}
	}

	if status, body := do(t, server, "PUT", "/looks/1/photo", "admin-token", "not a photo"); status != http.StatusBadRequest {
		t.Fatalf("not a photo: %d %s", status, body)
	}
	status, body = do(t, server, "PUT", "/looks/1/photo", "admin-token", pngPhoto(t, 1))
	if status != http.StatusOK || !strings.HasPrefix(decodeLook(t, body).PhotoURL, "/photos/looks/1.png?v=") {
		t.Fatalf("photo: %d %s", status, body)
	}
	// A JPEG replaces the PNG.
	status, body = do(t, server, "PUT", "/looks/1/photo", "admin-token", jpegPhoto(t))
	if status != http.StatusOK || decodeLook(t, body).Photo != "1.jpg" {
		t.Fatalf("jpeg: %d %s", status, body)
	}
	dir := filepath.Join(f.photos, "looks")
	if got := files(t, dir); !reflect.DeepEqual(got, []string{"1.jpg", "looks.json"}) {
		t.Fatalf("files %v", got)
	}

	status, body = do(t, server, "PUT", "/looks/1", "admin-token", `{"title":"Baru","active":false,"items":[]}`)
	if look := decodeLook(t, body); status != http.StatusOK || look.Title != "Baru" || look.Active || look.Photo != "1.jpg" || len(look.Items) != 0 {
		t.Fatalf("change: %d %s", status, body)
	}
	if status, _ := do(t, server, "PUT", "/looks/9", "admin-token", `{"title":"A","items":[]}`); status != http.StatusNotFound {
		t.Fatalf("missing look: %d", status)
	}

	// The file is what web reads.
	data, err := os.ReadFile(filepath.Join(dir, "looks.json"))
	if err != nil || !strings.Contains(string(data), `"title": "Baru"`) {
		t.Fatalf("file %s %v", data, err)
	}

	if status, _ := do(t, server, "DELETE", "/looks/1", "admin-token", ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if got := files(t, dir); !reflect.DeepEqual(got, []string{"looks.json"}) {
		t.Fatalf("files after delete %v", got)
	}
	// Ids aren't reused, so the shop never shows an old photo for a new look.
	status, body = do(t, server, "POST", "/looks", "admin-token", `{"title":"Lagi","items":[]}`)
	if status != http.StatusCreated || decodeLook(t, body).ID != 2 {
		t.Fatalf("second: %d %s", status, body)
	}
}
