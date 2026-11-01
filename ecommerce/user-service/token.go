package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	tokenLifetime = time.Hour
	tokenIssuer   = "user-service"
	tokenAudience = "shop"
)

type tokenClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// Tokens signs JWTs with a private key that only this service has. Other services check
// them with the public key from the JWKS endpoint, so they can't create their own.
type Tokens struct {
	private  ed25519.PrivateKey
	public   ed25519.PublicKey
	keyID    string
	lifetime time.Duration
}

func newTokens(privateKeyPEM string, lifetime time.Duration) (*Tokens, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("it isn't PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("it isn't a PKCS#8 private key")
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("it isn't an Ed25519 key")
	}
	public := private.Public().(ed25519.PublicKey)
	encoded := base64.RawURLEncoding.EncodeToString(public)
	// The JWK thumbprint from RFC 7638, so the same key always has the same id.
	thumbprint := sha256.Sum256([]byte(`{"crv":"Ed25519","kty":"OKP","x":"` + encoded + `"}`))
	return &Tokens{
		private:  private,
		public:   public,
		keyID:    base64.RawURLEncoding.EncodeToString(thumbprint[:]),
		lifetime: lifetime,
	}, nil
}

func (t *Tokens) Issue(user User) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, tokenClaims{
		Role: user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{tokenAudience},
			Subject:   strconv.FormatInt(user.ID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.lifetime)),
		},
	})
	token.Header["kid"] = t.keyID
	return token.SignedString(t.private)
}

// UserID checks a token and returns who it belongs to.
func (t *Tokens) UserID(tokenString string) (int64, error) {
	var claims tokenClaims
	_, err := jwt.ParseWithClaims(tokenString, &claims,
		func(*jwt.Token) (any, error) { return t.public, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("token subject isn't a user id: %w", err)
	}
	return id, nil
}

// JWKS is the public key in the format that JWT libraries read.
func (t *Tokens) JWKS() map[string]any {
	return map[string]any{"keys": []map[string]string{{
		"kty": "OKP",
		"crv": "Ed25519",
		"use": "sig",
		"alg": "EdDSA",
		"kid": t.keyID,
		"x":   base64.RawURLEncoding.EncodeToString(t.public),
	}}}
}
