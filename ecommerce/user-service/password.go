package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The parameters OWASP lists for Argon2id with 19 MiB of memory.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltBytes = 16
	argonKeyBytes  = 32

	// Each hash holds argonMemoryKiB, so this keeps a burst of logins under the Pod's memory limit.
	maxConcurrentHashes = 3
)

var errBusy = errors.New("too many password checks at once")

// Passwords hashes and checks passwords, a few at a time.
type Passwords struct {
	slots chan struct{}
	// dummy is checked for unknown emails, so they take as long as a wrong password.
	dummy string
}

func newPasswords(concurrent int) (*Passwords, error) {
	p := &Passwords{slots: make(chan struct{}, concurrent)}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	p.dummy = encodeHash("unused", salt)
	return p, nil
}

func (p *Passwords) acquire(ctx context.Context) error {
	select {
	case p.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return errBusy
	}
}

func (p *Passwords) release() { <-p.slots }

func (p *Passwords) Hash(ctx context.Context, password string) (string, error) {
	if err := p.acquire(ctx); err != nil {
		return "", err
	}
	defer p.release()
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return encodeHash(password, salt), nil
}

func (p *Passwords) Verify(ctx context.Context, encoded, password string) (bool, error) {
	if err := p.acquire(ctx); err != nil {
		return false, err
	}
	defer p.release()
	return verifyHash(encoded, password)
}

// WasteTime does the work of a failed Verify, for a login with no matching account.
func (p *Passwords) WasteTime(ctx context.Context, password string) error {
	_, err := p.Verify(ctx, p.dummy, password)
	return err
}

func encodeHash(password string, salt []byte) string {
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// verifyHash reads the parameters from the stored hash, so they can be raised later.
func verifyHash(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("stored password hash has an unknown format")
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("stored password hash has bad parameters: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("stored password hash has a bad salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("stored password hash has a bad key: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
