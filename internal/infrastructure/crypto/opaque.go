package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// Opaque creates random URL-safe secrets (refresh and reset tokens) and their SHA-256 storage hashes.
type Opaque struct{}

var _ usecase.OpaqueTokens = Opaque{}

// New returns a random token (32 bytes, base64url) and the hash to store in its place.
func (Opaque) New() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plain := base64.RawURLEncoding.EncodeToString(b)
	return plain, Opaque{}.Hash(plain), nil
}

// Hash returns the SHA-256 of a token, which is the form stored in the database.
func (Opaque) Hash(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}
