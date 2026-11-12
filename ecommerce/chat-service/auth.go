package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	tokenIssuer   = "user-service"
	tokenAudience = "shop"
	// How often an unknown key id can trigger a request, so bad tokens can't flood user-service.
	keyRefreshGap = 30 * time.Second
)

var errKeysUnavailable = errors.New("the public keys are not available")

// Verifier checks login tokens with user-service's public keys, so this service can't create them.
type Verifier struct {
	userURL string
	client  *http.Client

	mu          sync.Mutex
	keys        map[string]ed25519.PublicKey
	lastRefresh time.Time
}

func newVerifier(userURL string) *Verifier {
	return &Verifier{userURL: userURL, client: &http.Client{Timeout: 5 * time.Second}}
}

func (v *Verifier) fetchKeys(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.userURL+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user-service answered %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	keys := make(map[string]ed25519.PublicKey, len(set.Keys))
	for _, key := range set.Keys {
		x, err := base64.RawURLEncoding.DecodeString(key.X)
		if key.Kty == "OKP" && key.Crv == "Ed25519" && err == nil && len(x) == ed25519.PublicKeySize {
			keys[key.Kid] = x
		}
	}
	return keys, nil
}

func (v *Verifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key, found := v.keys[kid]; found {
		return key, nil
	}
	if !v.lastRefresh.IsZero() && time.Since(v.lastRefresh) < keyRefreshGap {
		if v.keys == nil {
			return nil, errKeysUnavailable
		}
		return nil, errors.New("unknown key id")
	}
	v.lastRefresh = time.Now()
	keys, err := v.fetchKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errKeysUnavailable, err)
	}
	v.keys = keys
	if key, found := keys[kid]; found {
		return key, nil
	}
	return nil, errors.New("unknown key id")
}

// UserID checks a token and returns who it belongs to.
func (v *Verifier) UserID(ctx context.Context, tokenString string) (int64, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(tokenString, &claims,
		func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			return v.key(ctx, kid)
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("token subject isn't a user id")
	}
	return id, nil
}
