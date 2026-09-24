package repository_test

import (
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var t0 = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func newUserInput(email string) usecase.NewUser {
	return usecase.NewUser{Email: entity.Email(email), PasswordHash: "old-hash", DisplayName: "Asha", PreferredLang: "hi", AcceptedAt: t0}
}

func mustUser(t *testing.T, users *repository.Users) entity.User {
	t.Helper()
	u, err := users.Create(t.Context(), newUserInput(uuid.NewString()+"@example.com"))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}
