package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testGoogleClientID = "shop.apps.googleusercontent.com"

func idToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".c2lnbmF0dXJl"
}

func googleClaims(sub, email string) map[string]any {
	return map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            testGoogleClientID,
		"exp":            time.Now().Add(time.Hour).Unix(),
		"sub":            sub,
		"email":          email,
		"email_verified": true,
		"name":           "Sari Google",
	}
}

// fakeGoogle answers every code with an ID token for these claims, or with 400 if nil.
func (s *testServer) fakeGoogle(t *testing.T, claims map[string]any) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_secret") != "secret" || r.FormValue("code_verifier") == "" {
			http.Error(w, "bad client", http.StatusUnauthorized)
			return
		}
		if claims == nil {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": idToken(t, claims)})
	}))
	t.Cleanup(server.Close)
	s.google.tokenURL = server.URL
}

func (s *testServer) googleLogin(t *testing.T) (int, loginResponse) {
	t.Helper()
	status, body := request(t, http.MethodPost, s.URL+"/google",
		`{"code":"c","code_verifier":"v","redirect_uri":"https://shop.test/api/auth/google/callback"}`, "")
	var login loginResponse
	if status == http.StatusOK {
		if err := json.Unmarshal(body, &login); err != nil {
			t.Fatal(err)
		}
	}
	return status, login
}

func TestGoogleMakesAnAccountWithoutPassword(t *testing.T) {
	s := newTestServer(t, nil)
	s.fakeGoogle(t, googleClaims("g-1", "Sari@Example.com"))
	status, login := s.googleLogin(t)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	user := login.User
	if user.Email != "sari@example.com" || user.Name != "Sari Google" || !user.EmailVerified || !user.Google || user.HasPassword {
		t.Fatalf("user %+v", user)
	}
	if _, again := s.googleLogin(t); again.User.ID != user.ID {
		t.Fatalf("a second login made account %d", again.User.ID)
	}
	// No password matches the empty one.
	if status := s.loginStatus(t, "sari@example.com", "x"); status != http.StatusBadRequest && status != http.StatusUnauthorized {
		t.Fatalf("password login: status %d", status)
	}
	status, _ = request(t, http.MethodPut, s.URL+"/me/password", `{"current_password":"anything","new_password":"rahasia123"}`, login.Token)
	if status != http.StatusConflict {
		t.Fatalf("changing a missing password: status %d", status)
	}
	// A reset link sets the first password.
	s.forgot(t, "sari@example.com")
	if status := s.reset(t, s.lastToken(t), "rahasia123"); status != http.StatusNoContent {
		t.Fatalf("reset: status %d", status)
	}
	if status := s.loginStatus(t, "sari@example.com", "rahasia123"); status != http.StatusOK {
		t.Fatalf("password login after reset: status %d", status)
	}
}

func TestGoogleLinksTheAccountWithItsEmail(t *testing.T) {
	s := newTestServer(t, nil)
	s.signUp(t, "sari@example.com", "rahasia123")
	s.fakeGoogle(t, googleClaims("g-1", "sari@example.com"))
	status, login := s.googleLogin(t)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if login.User.ID != 1 || login.User.Name != "Sari" || !login.User.EmailVerified || !login.User.HasPassword {
		t.Fatalf("user %+v", login.User)
	}
	// Google proved the email, so the password works without a code now.
	if status := s.loginStatus(t, "sari@example.com", "rahasia123"); status != http.StatusOK {
		t.Fatalf("password login: status %d", status)
	}
	// Another Google account with the same email can't take it.
	s.fakeGoogle(t, googleClaims("g-2", "sari@example.com"))
	if status, _ := s.googleLogin(t); status != http.StatusConflict {
		t.Fatalf("another Google account: status %d", status)
	}
}

func TestGoogleRefusals(t *testing.T) {
	s := newTestServer(t, nil)
	s.fakeGoogle(t, nil)
	if status, _ := s.googleLogin(t); status != http.StatusUnauthorized {
		t.Fatalf("a refused code: status %d", status)
	}
	unverified := googleClaims("g-1", "sari@example.com")
	unverified["email_verified"] = false
	s.fakeGoogle(t, unverified)
	if status, _ := s.googleLogin(t); status != http.StatusUnauthorized {
		t.Fatalf("an unverified email: status %d", status)
	}
	if status, _ := request(t, http.MethodPost, s.URL+"/google", `{"code":"c"}`, ""); status != http.StatusBadRequest {
		t.Fatalf("missing fields: status %d", status)
	}
}

func TestReadIDToken(t *testing.T) {
	g := newGoogle(testGoogleClientID, "secret")
	now := time.Now()
	for name, change := range map[string]func(map[string]any){
		"another app":     func(c map[string]any) { c["aud"] = "other.apps.googleusercontent.com" },
		"another issuer":  func(c map[string]any) { c["iss"] = "https://evil.example.com" },
		"expired":         func(c map[string]any) { c["exp"] = now.Add(-time.Minute).Unix() },
		"no subject":      func(c map[string]any) { c["sub"] = "" },
		"bad email":       func(c map[string]any) { c["email"] = "Sari <sari@example.com>" },
		"email unchecked": func(c map[string]any) { delete(c, "email_verified") },
	} {
		claims := googleClaims("g-1", "sari@example.com")
		change(claims)
		if _, err := g.readIDToken(idToken(t, claims), now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	claims := googleClaims("g-1", "sari@example.com")
	delete(claims, "name")
	identity, err := g.readIDToken(idToken(t, claims), now)
	if err != nil || identity.Name != "sari" || identity.Subject != "g-1" {
		t.Fatalf("identity %+v, error %v", identity, err)
	}
	if _, err := g.readIDToken("not.a-jwt", now); err == nil {
		t.Error("accepted a broken token")
	}
}
