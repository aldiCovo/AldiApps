package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPhotoRoutesPass(t *testing.T) {
	var mu sync.Mutex
	var got []string
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Content-Type")+" "+r.Header.Get("Authorization")+" "+string(body))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/photos/categories/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, `{"photos":[]}`)
	}))
	defer admin.Close()
	server := httptest.NewServer(newHandler(admin.URL))
	defer server.Close()

	send := func(method, path, contentType, body, cookie string) int {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cookie := "admin_session=tok-123"
	for _, c := range []struct{ method, path, contentType, body, want string }{
		{"GET", "/admin/api/photos/products", "", "", "GET /photos/products  Bearer tok-123 "},
		{"PUT", "/admin/api/photos/products/5/2", "image/jpeg", "JPEGDATA", "PUT /photos/products/5/2 image/jpeg Bearer tok-123 JPEGDATA"},
		{"PUT", "/admin/api/photos/products/5/4", "image/webp", "W", "PUT /photos/products/5/4 image/webp Bearer tok-123 W"},
		{"DELETE", "/admin/api/photos/products/5/2", "application/json", "", "DELETE /photos/products/5/2  Bearer tok-123 "},
		{"POST", "/admin/api/photos/products/5/3/first", "application/json", "", "POST /photos/products/5/3/first  Bearer tok-123 "},
		{"GET", "/admin/api/photos/categories", "", "", "GET /photos/categories  Bearer tok-123 "},
		{"PUT", "/admin/api/photos/categories/7", "image/png; charset=binary", "PNG", "PUT /photos/categories/7 image/png Bearer tok-123 PNG"},
		{"DELETE", "/admin/api/photos/categories/7", "application/json", "", "DELETE /photos/categories/7  Bearer tok-123 "},
	} {
		if status := send(c.method, c.path, c.contentType, c.body, cookie); status != http.StatusOK && status != http.StatusNoContent {
			t.Fatalf("%s %s: %d", c.method, c.path, status)
		}
		if last := got[len(got)-1]; last != c.want {
			t.Fatalf("sent %q, want %q", last, c.want)
		}
	}

	before := len(got)
	for _, c := range []struct {
		method, path, contentType, body, cookie string
		status                                  int
	}{
		// A form on another site can only send these types, so they never pass.
		{"PUT", "/admin/api/photos/products/5/1", "multipart/form-data; boundary=x", "x", cookie, http.StatusUnsupportedMediaType},
		{"PUT", "/admin/api/photos/products/5/1", "text/plain", "x", cookie, http.StatusUnsupportedMediaType},
		{"PUT", "/admin/api/photos/products/5/1", "image/svg+xml", "<svg/>", cookie, http.StatusUnsupportedMediaType},
		{"PUT", "/admin/api/photos/products/5/1", "", "x", cookie, http.StatusUnsupportedMediaType},
		{"DELETE", "/admin/api/photos/products/5/1", "", "", cookie, http.StatusUnsupportedMediaType},
		{"POST", "/admin/api/photos/products/5/1/first", "text/plain", "", cookie, http.StatusUnsupportedMediaType},
		{"PUT", "/admin/api/photos/products/5/5", "image/jpeg", "x", cookie, http.StatusNotFound},
		{"PUT", "/admin/api/photos/products/0/1", "image/jpeg", "x", cookie, http.StatusNotFound},
		{"PUT", "/admin/api/photos/categories/x", "image/jpeg", "x", cookie, http.StatusNotFound},
		{"PUT", "/admin/api/photos/products/5/1", "image/jpeg", strings.Repeat("x", maxPhotoBytes+1), cookie, http.StatusRequestEntityTooLarge},
		{"PUT", "/admin/api/photos/products/5/1", "image/jpeg", "x", "", http.StatusUnauthorized},
	} {
		if status := send(c.method, c.path, c.contentType, c.body, c.cookie); status != c.status {
			t.Errorf("%s %s (%s): %d, want %d", c.method, c.path, c.contentType, status, c.status)
		}
	}
	if len(got) != before {
		t.Fatalf("admin-service was called: %v", got[before:])
	}
}
