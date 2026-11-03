package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func pngPhoto(t *testing.T, shade uint8) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 5))
	img.Set(0, 0, color.RGBA{shade, 0, 0, 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func jpegPhoto(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	if err := jpeg.Encode(&out, image.NewRGBA(image.Rect(0, 0, 8, 10)), nil); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func slots(t *testing.T, body string) []string {
	t.Helper()
	var out struct{ Photos []string }
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Photos) != 4 {
		t.Fatalf("photos answer %q", body)
	}
	return out.Photos
}

func TestProductPhotos(t *testing.T) {
	server, f := newTestServer(t)
	dir := filepath.Join(f.photos, "products", "5")
	red, blue := pngPhoto(t, 200), pngPhoto(t, 20)

	// Only admins, only real photos, only places 1 to 4 of a product that exists.
	refused := []struct {
		path, token, body string
		status            int
	}{
		{"/photos/products/5/1", "", red, http.StatusUnauthorized},
		{"/photos/products/5/1", "customer-token", red, http.StatusForbidden},
		{"/photos/products/5/5", "admin-token", red, http.StatusNotFound},
		{"/photos/products/5/0", "admin-token", red, http.StatusNotFound},
		{"/photos/products/x/1", "admin-token", red, http.StatusNotFound},
		{"/photos/products/7/1", "admin-token", red, http.StatusNotFound},
		{"/photos/products/5/1", "admin-token", "<svg onload=alert(1)>", http.StatusBadRequest},
		{"/photos/products/5/1", "admin-token", "GIF89a" + strings.Repeat("x", 30), http.StatusBadRequest},
		{"/photos/products/5/1", "admin-token", "\x89PNG\r\n\x1a\nbroken", http.StatusBadRequest},
		{"/photos/products/5/1", "admin-token", "", http.StatusBadRequest},
		{"/photos/products/5/1", "admin-token", red + strings.Repeat("x", maxPhotoBytes), http.StatusRequestEntityTooLarge},
	}
	for _, c := range refused {
		if status, body := do(t, server, http.MethodPut, c.path, c.token, c.body); status != c.status {
			t.Errorf("PUT %s with %q…: status %d (%s), want %d", c.path, c.token, status, body, c.status)
		}
	}
	if got := files(t, filepath.Join(f.photos, "products")); got != nil {
		t.Fatalf("refused uploads left %v", got)
	}

	status, body := do(t, server, http.MethodPut, "/photos/products/5/2", "admin-token", red)
	if status != http.StatusOK {
		t.Fatalf("upload: %d %s", status, body)
	}
	if got := slots(t, body); got[0] != "" || !strings.HasPrefix(got[1], "/photos/products/5/2.png?v=") {
		t.Fatalf("after upload: %v", got)
	}
	// A JPEG in the same place replaces the PNG rather than sitting next to it.
	do(t, server, http.MethodPut, "/photos/products/5/2", "admin-token", jpegPhoto(t))
	do(t, server, http.MethodPut, "/photos/products/5/3", "admin-token", blue)
	if got := files(t, dir); strings.Join(got, ",") != "2.jpg,3.png" {
		t.Fatalf("files %v", got)
	}
	info, _ := os.Stat(filepath.Join(dir, "3.png"))
	if info.Mode().Perm() != 0o644 {
		t.Errorf("photo mode %v, want 0644 so the shop can read it", info.Mode().Perm())
	}

	// Making the third photo the first swaps it with what was first.
	status, body = do(t, server, http.MethodPost, "/photos/products/5/2/first", "admin-token", "")
	if status != http.StatusOK || files(t, dir)[0] != "1.jpg" {
		t.Fatalf("first: %d %s %v", status, body, files(t, dir))
	}
	status, body = do(t, server, http.MethodPost, "/photos/products/5/3/first", "admin-token", "")
	if got := files(t, dir); status != http.StatusOK || strings.Join(got, ",") != "1.png,3.jpg" {
		t.Fatalf("swap: %d %s %v", status, body, got)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "1.png")); string(data) != blue {
		t.Fatal("the chosen photo is not first")
	}
	if status, _ := do(t, server, http.MethodPost, "/photos/products/5/4/first", "admin-token", ""); status != http.StatusNotFound {
		t.Errorf("an empty place made first: %d", status)
	}

	status, body = do(t, server, http.MethodGet, "/photos/products", "admin-token", "")
	var all struct{ Photos map[string][]string }
	if json.Unmarshal([]byte(body), &all); status != http.StatusOK || len(all.Photos["5"]) != 4 || all.Photos["5"][2] == "" {
		t.Fatalf("list: %d %s", status, body)
	}
	if status, _ := do(t, server, http.MethodGet, "/photos/products", "customer-token", ""); status != http.StatusForbidden {
		t.Errorf("a customer listed photos: %d", status)
	}

	status, body = do(t, server, http.MethodDelete, "/photos/products/5/1", "admin-token", "")
	if got := slots(t, body); status != http.StatusOK || got[0] != "" || got[2] == "" {
		t.Fatalf("delete: %d %v", status, got)
	}
	do(t, server, http.MethodDelete, "/photos/products/5/3", "admin-token", "")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the empty folder stayed")
	}

	// Deleting the product removes its photos; the catalog fake accepts deleting 5.
	do(t, server, http.MethodPut, "/photos/products/5/1", "admin-token", red)
	if status, _ := do(t, server, http.MethodDelete, "/products/5", "admin-token", ""); status != http.StatusNoContent {
		t.Fatalf("delete product: %d", status)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a deleted product kept its photos")
	}
}

func TestPhotosNeverFollowLinks(t *testing.T) {
	server, f := newTestServer(t)
	outside := filepath.Join(t.TempDir(), "secret.png")
	os.WriteFile(outside, []byte("secret"), 0o600)
	dir := filepath.Join(f.photos, "products", "5")
	os.MkdirAll(dir, 0o755)
	if err := os.Symlink(outside, filepath.Join(dir, "2.png")); err != nil {
		t.Fatal(err)
	}
	if status, _ := do(t, server, http.MethodPost, "/photos/products/5/2/first", "admin-token", ""); status != http.StatusNotFound {
		t.Errorf("a link was made first: %d", status)
	}
	do(t, server, http.MethodPut, "/photos/products/5/2", "admin-token", pngPhoto(t, 1))
	if data, _ := os.ReadFile(outside); string(data) != "secret" {
		t.Fatal("writing through a link changed a file outside the folder")
	}
}

func TestCategoryPhotos(t *testing.T) {
	server, f := newTestServer(t)
	dir := filepath.Join(f.photos, "categories")
	photo := pngPhoto(t, 90)

	status, body := do(t, server, http.MethodPut, "/photos/categories/1", "admin-token", photo)
	if status != http.StatusOK || !strings.Contains(body, `"/photos/categories/office-on-style.png?v=`) {
		t.Fatalf("upload: %d %s", status, body)
	}
	if status, body := do(t, server, http.MethodPut, "/photos/categories/2", "admin-token", photo); status != http.StatusBadRequest {
		t.Errorf("a name the shop can't serve: %d %s", status, body)
	}
	if status, _ := do(t, server, http.MethodPut, "/photos/categories/9", "admin-token", photo); status != http.StatusNotFound {
		t.Errorf("missing category: %d", status)
	}
	if status, _ := do(t, server, http.MethodPut, "/photos/categories/1", "customer-token", photo); status != http.StatusForbidden {
		t.Errorf("customer upload: %d", status)
	}
	status, body = do(t, server, http.MethodGet, "/photos/categories", "admin-token", "")
	if status != http.StatusOK || !strings.Contains(body, `"1":"/photos/categories/office-on-style.png?v=`) || strings.Contains(body, `"2"`) {
		t.Fatalf("list: %d %s", status, body)
	}

	// Renaming the category moves its photo to the new name, which the shop looks for.
	if status, body := do(t, server, http.MethodPut, "/categories/1", "admin-token", `{"name":"Kantor  Formal"}`); status != http.StatusOK {
		t.Fatalf("rename: %d %s", status, body)
	}
	if got := files(t, dir); strings.Join(got, ",") != "kantor-formal.png" {
		t.Fatalf("after rename: %v", got)
	}
	if status, _ := do(t, server, http.MethodDelete, "/photos/categories/1", "admin-token", ""); status != http.StatusNoContent || files(t, dir) != nil {
		t.Fatalf("delete photo: %d %v", status, files(t, dir))
	}
	do(t, server, http.MethodPut, "/photos/categories/1", "admin-token", photo)
	if status, _ := do(t, server, http.MethodDelete, "/categories/1", "admin-token", ""); status != http.StatusNoContent || files(t, dir) != nil {
		t.Fatalf("a deleted category kept its photo: %d %v", status, files(t, dir))
	}
}

func TestCategorySlugMatchesTheShop(t *testing.T) {
	for name, want := range map[string]string{"Dress": "dress", " Office On  Style ": "office-on-style", "Rok\tPanjang": "rok-panjang"} {
		if got := categorySlug(name); got != want {
			t.Errorf("categorySlug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestWebpIsAccepted(t *testing.T) {
	webp := "RIFF\x24\x00\x00\x00WEBPVP8 " + strings.Repeat("\x00", 24)
	if extension, err := checkPhoto([]byte(webp)); err != nil || extension != ".webp" {
		t.Fatalf("webp: %q %v", extension, err)
	}
}
