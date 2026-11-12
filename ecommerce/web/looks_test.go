package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShownLook(t *testing.T) {
	photos := t.TempDir()
	dir := filepath.Join(photos, "looks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler("http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", photos, googleConfig{}))
	t.Cleanup(server.Close)
	look := func() *shownLook {
		t.Helper()
		resp, err := http.Get(server.URL + "/api/look")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct{ Look *shownLook }
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Look
	}
	if got := look(); got != nil {
		t.Fatalf("no file: %+v", got)
	}
	os.WriteFile(filepath.Join(dir, "1.jpg"), []byte("jpg"), 0o644)
	os.WriteFile(filepath.Join(dir, "3.jpg"), []byte("jpg"), 0o644)
	item := `"items":[{"product_id":5,"x":10,"y":20}]`
	os.WriteFile(filepath.Join(dir, "looks.json"), []byte(`{"next_id":5,"looks":[
		{"id":1,"title":"Lama","active":true,`+item+`,"photo":"1.jpg","updated_at":"2026-11-01T00:00:00Z"},
		{"id":2,"title":"Tanpa foto","active":true,`+item+`,"photo":"","updated_at":"2026-11-09T00:00:00Z"},
		{"id":3,"title":"Mati","active":false,`+item+`,"photo":"3.jpg","updated_at":"2026-11-09T00:00:00Z"},
		{"id":4,"title":"Kosong","active":true,"items":[],"photo":"3.jpg","updated_at":"2026-11-09T00:00:00Z"},
		{"id":5,"title":"Foto hilang","active":true,`+item+`,"photo":"5.jpg","updated_at":"2026-11-09T00:00:00Z"},
		{"id":6,"title":"Jalan keluar","active":true,`+item+`,"photo":"../secret.jpg","updated_at":"2026-11-10T00:00:00Z"}]}`), 0o644)
	got := look()
	if got == nil || got.Title != "Lama" || !strings.HasPrefix(got.PhotoURL, "/photos/looks/1.jpg?v=") || len(got.Items) != 1 || got.Items[0].X != 10 {
		t.Fatalf("look %+v", got)
	}
	resp, err := http.Get(server.URL + "/photos/looks/1.jpg")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("photo: %v %v", resp, err)
	}
	resp.Body.Close()
	if resp, _ := http.Get(server.URL + "/photos/looks/looks.json"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("looks.json is served: %d", resp.StatusCode)
	}
}
