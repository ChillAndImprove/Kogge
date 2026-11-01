// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package crypto

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the Argon2id cost parameters.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultArgon2 follows current recommendations for interactive logins.
var DefaultArgon2 = Argon2Params{Memory: 64 * 1024, Time: 3, Threads: 2}

// PasswordHasher hashes and verifies passwords with Argon2id behind a concurrency limit, so login floods cannot
// exhaust CPU or memory.
type PasswordHasher struct {
	params Argon2Params
	sem    chan struct{}
}

// NewPasswordHasher creates a hasher; concurrency 0 means half the CPUs (at least 2).
func NewPasswordHasher(params Argon2Params, concurrency int) *PasswordHasher {
	if concurrency <= 0 {
		concurrency = max(2, runtime.NumCPU()/2)
	}
	return &PasswordHasher{params: params, sem: make(chan struct{}, concurrency)}
}

func (h *PasswordHasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *PasswordHasher) release() { <-h.sem }

// Hash returns the PHC string of the password.
func (h *PasswordHasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := h.params
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks a password against a PHC string produced by Hash. needsRehash reports whether the stored hash
// uses weaker parameters than the current ones.
func (h *PasswordHasher) Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, errors.New("password hash: unknown format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, errors.New("password hash: unsupported version")
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, false, errors.New("password hash: bad parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false, errors.New("password hash: bad salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false, errors.New("password hash: bad key")
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	needsRehash = p.Memory < h.params.Memory || p.Time < h.params.Time || p.Threads < h.params.Threads
	return ok, needsRehash, nil
}

// ValidatePassword checks the password policy for local accounts.
func ValidatePassword(password, username string) error {
	if len([]rune(password)) < 12 {
		return errors.New("The password must have at least 12 characters.")
	}
	if len(password) > 1024 {
		return errors.New("The password is too long.")
	}
	if username != "" && strings.Contains(strings.ToLower(password), strings.ToLower(username)) {
		return errors.New("The password must not contain the user name.")
	}
	return nil
}
