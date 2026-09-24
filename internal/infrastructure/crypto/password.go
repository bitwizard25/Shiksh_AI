// Package crypto implements the password, access-token and opaque-token ports.
package crypto

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// argon2id parameters: the OWASP minimum recommendation (19 MiB, 2 iterations, 1 lane).
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

var errMalformedHash = errors.New("crypto: malformed password hash")

// Argon2Hasher hashes and verifies passwords with argon2id. Every hash allocates about 19 MiB,
// so a semaphore caps concurrent hashes and bounds memory during a burst of logins.
type Argon2Hasher struct {
	sem   chan struct{}
	dummy string
}

var _ usecase.PasswordHasher = (*Argon2Hasher)(nil)

// NewArgon2Hasher allows at most maxConcurrent hashes at a time (use 2×NumCPU in production).
func NewArgon2Hasher(maxConcurrent int) *Argon2Hasher {
	salt := make([]byte, argonSaltLen) // a fixed salt is fine: the dummy hash never matches a real password
	key := argon2.IDKey([]byte("dummy password"), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return &Argon2Hasher{
		sem:   make(chan struct{}, max(1, maxConcurrent)),
		dummy: encodeHash(salt, key, argonMemoryKiB, argonTime, argonThreads),
	}
}

// Hash returns a PHC-formatted hash: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>.
func (h *Argon2Hasher) Hash(ctx context.Context, password string) (string, error) {
	password = norm.NFC.String(password)
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return encodeHash(salt, key, argonMemoryKiB, argonTime, argonThreads), nil
}

// Verify reports whether password matches encoded. Parameters are read from the hash itself,
// so hashes made with older parameters keep verifying after the constants change. password is
// NFC-normalized before comparing, matching Hash, so a password typed with differently
// decomposed combining marks still verifies.
func (h *Argon2Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	password = norm.NFC.String(password)
	salt, key, m, t, p, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// VerifyDummy spends the same time as Verify. Call it when the account does not exist so that
// response timing does not reveal which emails are registered.
func (h *Argon2Hasher) VerifyDummy(ctx context.Context, password string) {
	_, _ = h.Verify(ctx, password, h.dummy)
}

func (h *Argon2Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Argon2Hasher) release() { <-h.sem }

func encodeHash(salt, key []byte, m, t uint32, p uint8) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, m, t, p,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func decodeHash(encoded string) (salt, key []byte, m, t uint32, p uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	return salt, key, m, t, p, nil
}
