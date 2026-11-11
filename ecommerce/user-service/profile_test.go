package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"
)

func encodedJPEG(t *testing.T, width, height int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func readProfile(t *testing.T, body []byte) Profile {
	t.Helper()
	var p Profile
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProfileInputValidate(t *testing.T) {
	today := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in    profileInput
		valid bool
	}{
		{profileInput{Name: "Sari"}, true},
		{profileInput{Name: " Sari ", Phone: "+62 812-3456-7890", BirthDate: "1996-04-18", Gender: "female"}, true},
		{profileInput{Name: "  "}, false},
		{profileInput{Name: strings.Repeat("a", maxNameLength+1)}, false},
		{profileInput{Name: "Sari", Phone: "12345"}, false},
		{profileInput{Name: "Sari", BirthDate: "18-04-1996"}, false},
		{profileInput{Name: "Sari", BirthDate: "1899-12-31"}, false},
		{profileInput{Name: "Sari", BirthDate: "2026-11-12"}, false},
		{profileInput{Name: "Sari", Gender: "other"}, false},
	} {
		err := tc.in.validate(today)
		if (err == nil) != tc.valid {
			t.Errorf("%+v: error %v, want valid %v", tc.in, err, tc.valid)
		}
	}
}

func TestCheckPhoto(t *testing.T) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 100, 100))); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		encoded string
		valid   bool
	}{
		"square jpeg":  {encodedJPEG(t, 256, 256), true},
		"not square":   {encodedJPEG(t, 256, 200), false},
		"too small":    {encodedJPEG(t, 32, 32), false},
		"png":          {base64.StdEncoding.EncodeToString(pngBuf.Bytes()), false},
		"not base64":   {"%%%", false},
		"not an image": {base64.StdEncoding.EncodeToString([]byte("<svg></svg>")), false},
	} {
		if _, err := checkPhoto(tc.encoded); (err == nil) != tc.valid {
			t.Errorf("%s: error %v, want valid %v", name, err, tc.valid)
		}
	}
}

func TestProfileUpdate(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	token := s.login(t, "sari@example.com", "password123").Token

	status, body := request(t, http.MethodGet, s.URL+"/me", "", token)
	if p := readProfile(t, body); status != http.StatusOK || p.Name != "Sari" || p.Phone != "" || p.BirthDate != "" || p.PhotoVersion != 0 {
		t.Fatalf("GET /me: status %d, body %s", status, body)
	}

	status, body = request(t, http.MethodPut, s.URL+"/me",
		`{"name":"Sari Dewi","phone":"0812 3456 7890","birth_date":"1996-04-18","gender":"female"}`, token)
	p := readProfile(t, body)
	if status != http.StatusOK || p.Name != "Sari Dewi" || p.Phone != "081234567890" || p.BirthDate != "1996-04-18" || p.Gender != "female" || p.Email != "sari@example.com" {
		t.Fatalf("PUT /me: status %d, body %s", status, body)
	}

	// Emptying the optional fields clears them.
	status, body = request(t, http.MethodPut, s.URL+"/me", `{"name":"Sari Dewi","phone":"","birth_date":"","gender":""}`, token)
	if p := readProfile(t, body); status != http.StatusOK || p.Phone != "" || p.BirthDate != "" || p.Gender != "" {
		t.Fatalf("clearing: status %d, body %s", status, body)
	}

	// Email and role can't be changed this way.
	if status, _ := request(t, http.MethodPut, s.URL+"/me", `{"name":"Sari","role":"admin"}`, token); status != http.StatusBadRequest {
		t.Fatalf("role in body: status %d, want 400", status)
	}
	if status, _ := request(t, http.MethodPut, s.URL+"/me", `{"name":"Sari","phone":"999"}`, token); status != http.StatusBadRequest {
		t.Fatalf("bad phone: status %d, want 400", status)
	}
	if status, _ := request(t, http.MethodPut, s.URL+"/me", `{"name":"Sari"}`, ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", status)
	}
}

func TestPasswordChange(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	token := s.login(t, "sari@example.com", "password123").Token

	status, body := request(t, http.MethodPut, s.URL+"/me/password", `{"current_password":"wrong-one","new_password":"newpassword1"}`, token)
	if status != http.StatusForbidden {
		t.Fatalf("wrong current password: status %d, body %s, want 403", status, body)
	}
	if status, _ := request(t, http.MethodPut, s.URL+"/me/password", `{"current_password":"password123","new_password":"short"}`, token); status != http.StatusBadRequest {
		t.Fatalf("short new password: status %d, want 400", status)
	}
	if status, _ := request(t, http.MethodPut, s.URL+"/me/password", `{"current_password":"","new_password":"newpassword1"}`, token); status != http.StatusBadRequest {
		t.Fatalf("no current password: status %d, want 400", status)
	}

	status, body = request(t, http.MethodPut, s.URL+"/me/password", `{"current_password":"password123","new_password":"newpassword1"}`, token)
	if status != http.StatusNoContent {
		t.Fatalf("change: status %d, body %s", status, body)
	}
	s.login(t, "sari@example.com", "newpassword1")
	status, _ = request(t, http.MethodPost, s.URL+"/login", `{"email":"sari@example.com","password":"password123"}`, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("old password still works: status %d", status)
	}
}

func TestPhoto(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	other := s.register(t, "dewi@example.com", "password123")
	token := s.login(t, "sari@example.com", "password123").Token
	otherToken := s.login(t, other.Email, "password123").Token

	if status, _ := request(t, http.MethodGet, s.URL+"/me/photo", "", token); status != http.StatusNotFound {
		t.Fatalf("no photo yet: status %d, want 404", status)
	}
	encoded := encodedJPEG(t, 256, 256)
	status, body := request(t, http.MethodPut, s.URL+"/me/photo", fmt.Sprintf(`{"image":%q}`, encoded), token)
	if p := readProfile(t, body); status != http.StatusOK || p.PhotoVersion == 0 {
		t.Fatalf("upload: status %d, body %s", status, body)
	}
	status, body = request(t, http.MethodGet, s.URL+"/me/photo", "", token)
	want, _ := base64.StdEncoding.DecodeString(encoded)
	if status != http.StatusOK || !bytes.Equal(body, want) {
		t.Fatalf("download: status %d, %d bytes", status, len(body))
	}
	// Each account sees only its own photo.
	if status, _ := request(t, http.MethodGet, s.URL+"/me/photo", "", otherToken); status != http.StatusNotFound {
		t.Fatalf("other account: status %d, want 404", status)
	}
	if status, _ := request(t, http.MethodPut, s.URL+"/me/photo", fmt.Sprintf(`{"image":%q}`, encodedJPEG(t, 300, 200)), token); status != http.StatusBadRequest {
		t.Fatalf("not square: status %d, want 400", status)
	}

	if status, _ := request(t, http.MethodDelete, s.URL+"/me/photo", "", token); status != http.StatusNoContent {
		t.Fatalf("delete: status %d", status)
	}
	status, body = request(t, http.MethodGet, s.URL+"/me", "", token)
	if p := readProfile(t, body); status != http.StatusOK || p.PhotoVersion != 0 {
		t.Fatalf("after delete: status %d, body %s", status, body)
	}
}
