package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
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

func newKeyPEM(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func newTestTokens(t *testing.T, lifetime time.Duration) *Tokens {
	t.Helper()
	tokens, err := newTokens(newKeyPEM(t), lifetime)
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

type testServer struct {
	*httptest.Server
	pool   *pgxpool.Pool
	tokens *Tokens
}

// newTestServer needs a throwaway database, because it empties the table.
func newTestServer(t *testing.T, passwords *Passwords) *testServer {
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
	if _, err := pool.Exec(ctx, "TRUNCATE users RESTART IDENTITY"); err != nil {
		t.Fatal(err)
	}
	if passwords == nil {
		if passwords, err = newPasswords(maxConcurrentHashes); err != nil {
			t.Fatal(err)
		}
	}
	tokens := newTestTokens(t, time.Hour)
	server := httptest.NewServer(newHandler(&Store{pool: pool}, tokens, passwords, testInternalToken))
	t.Cleanup(server.Close)
	return &testServer{Server: server, pool: pool, tokens: tokens}
}

func request(t *testing.T, method, url, body, bearer string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, buf.Bytes()
}

func (s *testServer) register(t *testing.T, email, password string) User {
	t.Helper()
	status, body := request(t, http.MethodPost, s.URL+"/register",
		fmt.Sprintf(`{"email":%q,"name":"Sari","password":%q}`, email, password), "")
	if status != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", status, body)
	}
	var user User
	if err := json.Unmarshal(body, &user); err != nil {
		t.Fatal(err)
	}
	return user
}

func (s *testServer) login(t *testing.T, email, password string) loginResponse {
	t.Helper()
	status, body := request(t, http.MethodPost, s.URL+"/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password), "")
	if status != http.StatusOK {
		t.Fatalf("login: status %d, body %s", status, body)
	}
	var got loginResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRegisterValidation(t *testing.T) {
	s := newTestServer(t, nil)
	long := strings.Repeat("a", 129)
	cases := map[string]string{
		"no email":       `{"name":"Sari","password":"password123"}`,
		"bad email":      `{"email":"sari","name":"Sari","password":"password123"}`,
		"display name":   `{"email":"Sari <sari@example.com>","name":"Sari","password":"password123"}`,
		"NUL in email":   `{"email":"sa\u0000ri@example.com","name":"Sari","password":"password123"}`,
		"no name":        `{"email":"sari@example.com","password":"password123"}`,
		"NUL in name":    `{"email":"sari@example.com","name":"Sa\u0000ri","password":"password123"}`,
		"short password": `{"email":"sari@example.com","name":"Sari","password":"short12"}`,
		"long password":  `{"email":"sari@example.com","name":"Sari","password":"` + long + `"}`,
		"role sent":      `{"email":"sari@example.com","name":"Sari","password":"password123","role":"admin"}`,
		"not JSON":       `nope`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if status, got := request(t, http.MethodPost, s.URL+"/register", body, ""); status != http.StatusBadRequest {
				t.Fatalf("status %d, body %s", status, got)
			}
		})
	}
}

func TestRegisterStoresOnlyAHash(t *testing.T) {
	s := newTestServer(t, nil)
	status, body := request(t, http.MethodPost, s.URL+"/register",
		`{"email":" Sari@Example.COM ","name":" Sari ","password":"  password 123 "}`, "")
	if status != http.StatusCreated {
		t.Fatalf("status %d, body %s", status, body)
	}
	if strings.Contains(string(body), "password") || strings.Contains(string(body), "argon2") {
		t.Fatalf("response exposes the password: %s", body)
	}
	var user User
	if err := json.Unmarshal(body, &user); err != nil {
		t.Fatal(err)
	}
	if user.Email != "sari@example.com" || user.Name != "Sari" || user.Role != "customer" {
		t.Fatalf("got %+v", user)
	}
	var hash string
	if err := s.pool.QueryRow(context.Background(), "SELECT password_hash FROM users").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(hash, "password") {
		t.Fatalf("stored %q", hash)
	}
	// Spaces around the password count, unlike those around the email.
	s.login(t, "SARI@example.com", "  password 123 ")
	if status, _ := request(t, http.MethodPost, s.URL+"/login", `{"email":"sari@example.com","password":"password 123"}`, ""); status != http.StatusUnauthorized {
		t.Fatalf("trimmed password: status %d", status)
	}
}

func TestSamePasswordGetsDifferentHashes(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "a@example.com", "password123")
	s.register(t, "b@example.com", "password123")
	var distinct int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(DISTINCT password_hash) FROM users").Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 2 {
		t.Fatalf("got %d distinct hashes", distinct)
	}
}

func TestDuplicateEmailsConflictEvenAtOnce(t *testing.T) {
	s := newTestServer(t, nil)
	const attempts = 6
	statuses := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"email":"SARI@example.com","name":"Sari","password":"password123"}`
			statuses[i], _ = request(t, http.MethodPost, s.URL+"/register", body, "")
		}()
	}
	wg.Wait()
	created, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != attempts-1 {
		t.Fatalf("statuses %v", statuses)
	}
	if status, _ := request(t, http.MethodPost, s.URL+"/register", `{"email":"sari@EXAMPLE.com","name":"Sari","password":"password123"}`, ""); status != http.StatusConflict {
		t.Fatalf("other case: status %d", status)
	}
}

func TestLoginFailuresLookTheSame(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	cases := map[string]string{
		"wrong password": `{"email":"sari@example.com","password":"password124"}`,
		"unknown email":  `{"email":"nobody@example.com","password":"password123"}`,
		"invalid email":  `{"email":"nobody","password":"password123"}`,
		"NUL in email":   `{"email":"sa\u0000ri@example.com","password":"password123"}`,
	}
	var first string
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			status, got := request(t, http.MethodPost, s.URL+"/login", body, "")
			if status != http.StatusUnauthorized {
				t.Fatalf("status %d, body %s", status, got)
			}
			if first == "" {
				first = string(got)
			}
			if string(got) != first {
				t.Fatalf("got %s, want %s", got, first)
			}
		})
	}
	if status, _ := request(t, http.MethodPost, s.URL+"/login", `{"email":"sari@example.com"}`, ""); status != http.StatusBadRequest {
		t.Fatalf("no password: status %d", status)
	}
}

func TestUnknownEmailTakesAsLongAsWrongPassword(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	measure := func(body string) time.Duration {
		best := time.Hour
		for range 5 {
			start := time.Now()
			request(t, http.MethodPost, s.URL+"/login", body, "")
			best = min(best, time.Since(start))
		}
		return best
	}
	wrong := measure(`{"email":"sari@example.com","password":"password124"}`)
	unknown := measure(`{"email":"nobody@example.com","password":"password124"}`)
	if unknown < wrong/2 {
		t.Fatalf("unknown email took %v, wrong password %v", unknown, wrong)
	}
}

func TestMeReturnsCurrentUser(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	login := s.login(t, "sari@example.com", "password123")
	if login.TokenType != "Bearer" || login.ExpiresIn != 3600 || login.User.Email != "sari@example.com" {
		t.Fatalf("got %+v", login)
	}
	status, body := request(t, http.MethodGet, s.URL+"/me", "", login.Token)
	if status != http.StatusOK || !strings.Contains(string(body), `"email":"sari@example.com"`) {
		t.Fatalf("status %d, body %s", status, body)
	}
	if strings.Contains(string(body), "argon2") {
		t.Fatalf("response exposes the hash: %s", body)
	}
	// /me reads the database, so it shows a role change before the token expires.
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "UPDATE users SET role = 'admin'"); err != nil {
		t.Fatal(err)
	}
	_, body = request(t, http.MethodGet, s.URL+"/me", "", login.Token)
	if !strings.Contains(string(body), `"role":"admin"`) {
		t.Fatalf("got %s", body)
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM users"); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(t, http.MethodGet, s.URL+"/me", "", login.Token); status != http.StatusUnauthorized {
		t.Fatalf("deleted user: status %d", status)
	}
}

// sign makes a token with the given key and method, for tokens the service must refuse.
func sign(t *testing.T, method jwt.SigningMethod, key any, claims tokenClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestMeRefusesBadTokens(t *testing.T) {
	s := newTestServer(t, nil)
	user := s.register(t, "sari@example.com", "password123")
	good := s.login(t, "sari@example.com", "password123").Token

	valid := func() tokenClaims {
		return tokenClaims{Role: "admin", RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{tokenAudience},
			Subject:   strconv.FormatInt(user.ID, 10),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}}
	}
	// A valid token proves the helper makes tokens the service would accept.
	if status, body := request(t, http.MethodGet, s.URL+"/me", "", sign(t, jwt.SigningMethodEdDSA, s.tokens.private, valid())); status != http.StatusOK {
		t.Fatalf("control token: status %d, body %s", status, body)
	}

	expired := valid()
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	noExpiry := valid()
	noExpiry.ExpiresAt = nil
	wrongIssuer := valid()
	wrongIssuer.Issuer = "someone-else"
	wrongAudience := valid()
	wrongAudience.Audience = jwt.ClaimStrings{"other"}
	badSubject := valid()
	badSubject.Subject = "not-a-number"
	missingUser := valid()
	missingUser.Subject = "999999"

	parts := strings.Split(good, ".")
	forged, err := json.Marshal(map[string]any{"sub": "1", "role": "admin", "iss": tokenIssuer, "aud": tokenAudience, "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"garbage":        "nope",
		"other key":      sign(t, jwt.SigningMethodEdDSA, newTestTokens(t, time.Hour).private, valid()),
		"alg none":       sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, valid()),
		"HS256 with key": sign(t, jwt.SigningMethodHS256, []byte(s.tokens.public), valid()),
		"expired":        sign(t, jwt.SigningMethodEdDSA, s.tokens.private, expired),
		"no expiry":      sign(t, jwt.SigningMethodEdDSA, s.tokens.private, noExpiry),
		"wrong issuer":   sign(t, jwt.SigningMethodEdDSA, s.tokens.private, wrongIssuer),
		"wrong audience": sign(t, jwt.SigningMethodEdDSA, s.tokens.private, wrongAudience),
		"bad subject":    sign(t, jwt.SigningMethodEdDSA, s.tokens.private, badSubject),
		"missing user":   sign(t, jwt.SigningMethodEdDSA, s.tokens.private, missingUser),
		"changed claims": parts[0] + "." + base64.RawURLEncoding.EncodeToString(forged) + "." + parts[2],
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			status, body := request(t, http.MethodGet, s.URL+"/me", "", token)
			if status != http.StatusUnauthorized {
				t.Fatalf("status %d, body %s", status, body)
			}
		})
	}
	t.Run("no header", func(t *testing.T) {
		if status, _ := request(t, http.MethodGet, s.URL+"/me", "", ""); status != http.StatusUnauthorized {
			t.Fatalf("status %d", status)
		}
	})
}

// Another service only gets the JWKS, so this checks a token the way that service would.
func TestTokenVerifiesWithPublishedKey(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	token := s.login(t, "sari@example.com", "password123").Token

	status, body := request(t, http.MethodGet, s.URL+"/.well-known/jwks.json", "", "")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 1 || set.Keys[0]["kty"] != "OKP" || set.Keys[0]["d"] != "" {
		t.Fatalf("got %s", body)
	}
	x, err := base64.RawURLEncoding.DecodeString(set.Keys[0]["x"])
	if err != nil {
		t.Fatal(err)
	}
	var claims tokenClaims
	parsed, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return ed25519.PublicKey(x), nil },
		jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithAudience(tokenAudience), jwt.WithIssuer(tokenIssuer))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header["kid"] != set.Keys[0]["kid"] || claims.Role != "customer" || claims.Subject != "1" {
		t.Fatalf("header %v, claims %+v", parsed.Header, claims)
	}
}

func TestBusyPasswordSlotsAnswer503(t *testing.T) {
	passwords, err := newPasswords(1)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, passwords)
	if err := passwords.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, body := request(t, http.MethodPost, s.URL+"/login", `{"email":"sari@example.com","password":"password123"}`, "")
	passwords.release()
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status %d, body %s", status, body)
	}
	s.register(t, "sari@example.com", "password123")
}

func TestPrivateKeyMustBeEd25519PEM(t *testing.T) {
	for name, value := range map[string]string{
		"not PEM":   "nope",
		"not PKCS8": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("nope")})),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newTokens(value, time.Hour); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestSameKeyKeepsItsID(t *testing.T) {
	key := newKeyPEM(t)
	a, err := newTokens(key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newTokens(key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a.keyID != b.keyID || a.keyID == "" {
		t.Fatalf("got %q and %q", a.keyID, b.keyID)
	}
}

const testInternalToken = "internal-token-for-the-tests-only"

func TestInternalUserNeedsTheInternalToken(t *testing.T) {
	server := newTestServer(t, nil)
	status, body := request(t, http.MethodPost, server.URL+"/register", `{"email":"sari@example.com","name":"Sari","password":"rahasia-sekali"}`, "")
	if status != http.StatusCreated {
		t.Fatalf("register answered %d: %s", status, body)
	}
	var created User
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("%s/internal/users/%d", server.URL, created.ID)

	userToken, err := server.tokens.Issue(created)
	if err != nil {
		t.Fatal(err)
	}
	for _, bearer := range []string{"", "wrong", userToken} {
		if status, _ := request(t, http.MethodGet, url, "", bearer); status != http.StatusUnauthorized {
			t.Errorf("bearer %q: want 401, got %d", bearer, status)
		}
	}

	status, body = request(t, http.MethodGet, url, "", testInternalToken)
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", status, body)
	}
	var user map[string]any
	if err := json.Unmarshal(body, &user); err != nil {
		t.Fatal(err)
	}
	if user["email"] != "sari@example.com" || user["name"] != "Sari" {
		t.Errorf("got %v", user)
	}
	if strings.Contains(string(body), "password") {
		t.Errorf("the answer mentions a password: %s", body)
	}

	for _, id := range []string{"999999", "0", "abc"} {
		if status, _ := request(t, http.MethodGet, server.URL+"/internal/users/"+id, "", testInternalToken); status != http.StatusNotFound {
			t.Errorf("id %s: want 404, got %d", id, status)
		}
	}
}
