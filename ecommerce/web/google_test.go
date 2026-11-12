package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testRedirect = "https://shop.test/api/auth/google/callback"

// googleShop is the gateway with Google login, and a user-service that takes any code but "bad".
func googleShop(t *testing.T) (*httptest.Server, *[]map[string]string) {
	t.Helper()
	var asked []map[string]string
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		asked = append(asked, in)
		if r.URL.Path != "/google" || in["code"] == "bad" {
			http.Error(w, `{"error":"no"}`, http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"token":"tok","token_type":"Bearer","expires_in":3600,"user":{"id":1}}`)
	}))
	t.Cleanup(users.Close)
	shop := httptest.NewServer(newHandler("http://127.0.0.1:1", "", users.URL, "", "http://127.0.0.1:1", "", "", t.TempDir(),
		googleConfig{clientID: "shop.apps.googleusercontent.com", redirectURI: testRedirect}))
	t.Cleanup(shop.Close)
	return shop, &asked
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func get(t *testing.T, url string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestGoogleLoginRoundTrip(t *testing.T) {
	shop, asked := googleShop(t)
	resp := get(t, shop.URL+"/api/auth/google")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	to, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || to.Host != "accounts.google.com" {
		t.Fatalf("redirect to %q", resp.Header.Get("Location"))
	}
	query := to.Query()
	cookie := cookieNamed(resp, googleCookie)
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.Path != googleCookiePath {
		t.Fatalf("cookie %+v", cookie)
	}
	state, verifier, _ := strings.Cut(cookie.Value, ".")
	challenge := sha256.Sum256([]byte(verifier))
	if query.Get("state") != state || query.Get("redirect_uri") != testRedirect ||
		query.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("query %v, cookie %q", query, cookie.Value)
	}

	back := get(t, shop.URL+"/api/auth/google/callback?code=good&state="+state, cookie)
	if back.StatusCode != http.StatusSeeOther || back.Header.Get("Location") != "/" {
		t.Fatalf("status %d to %q", back.StatusCode, back.Header.Get("Location"))
	}
	if s := cookieNamed(back, sessionCookie); s == nil || s.Value != "tok" || !s.HttpOnly {
		t.Fatalf("session %+v", s)
	}
	if c := cookieNamed(back, googleCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("the Google cookie stays: %+v", c)
	}
	if len(*asked) != 1 || (*asked)[0]["code_verifier"] != verifier || (*asked)[0]["redirect_uri"] != testRedirect {
		t.Fatalf("user-service got %v", *asked)
	}
}

func TestGoogleCallbackRefusals(t *testing.T) {
	shop, asked := googleShop(t)
	cookie := cookieNamed(get(t, shop.URL+"/api/auth/google"), googleCookie)
	state, _, _ := strings.Cut(cookie.Value, ".")
	for name, tc := range map[string]struct {
		query  string
		cookie *http.Cookie
	}{
		"no cookie":       {"code=good&state=" + state, nil},
		"another state":   {"code=good&state=other", cookie},
		"cancelled":       {"error=access_denied&state=" + state, cookie},
		"code refused":    {"code=bad&state=" + state, cookie},
		"no state at all": {"code=good", cookie},
	} {
		var cookies []*http.Cookie
		if tc.cookie != nil {
			cookies = append(cookies, tc.cookie)
		}
		resp := get(t, shop.URL+"/api/auth/google/callback?"+tc.query, cookies...)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != googleFailed {
			t.Errorf("%s: status %d to %q", name, resp.StatusCode, resp.Header.Get("Location"))
		}
		if s := cookieNamed(resp, sessionCookie); s != nil {
			t.Errorf("%s: got a session", name)
		}
	}
	// Only the refused code reached user-service.
	if len(*asked) != 1 {
		t.Fatalf("user-service was asked %d times", len(*asked))
	}
}

func TestGoogleOffWithoutClientID(t *testing.T) {
	shop := httptest.NewServer(newHandler("http://127.0.0.1:1", "", "", "", "http://127.0.0.1:1", "", "", t.TempDir(), googleConfig{}))
	t.Cleanup(shop.Close)
	if resp := get(t, shop.URL+"/api/auth/google"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp, err := http.Get(shop.URL + "/api/auth/options")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var options map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&options); err != nil || options["google"] {
		t.Fatalf("options %v, error %v", options, err)
	}
}
