package repository_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestConsumeRefreshIsSingleUse(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	family := uuid.New()
	if err := tokens.CreateRefresh(ctx, u.ID, family, hashOf("A"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	tok, err := tokens.ConsumeRefresh(ctx, hashOf("A"), t0)
	if err != nil {
		t.Fatalf("ConsumeRefresh: %v", err)
	}
	if tok.UserID != u.ID || tok.FamilyID != family || tok.UsedAt == nil || !tok.UsedAt.Equal(t0) {
		t.Fatalf("token = %+v", tok)
	}
	if _, err := tokens.ConsumeRefresh(ctx, hashOf("A"), t0.Add(time.Second)); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("second consume err = %v", err)
	}
	found, err := tokens.FindRefresh(ctx, hashOf("A"))
	if err != nil || found.UsedAt == nil || !found.UsedAt.Equal(t0) {
		t.Fatalf("FindRefresh = %+v, %v", found, err)
	}
}

func TestConsumeRefreshRejectsExpiredRevokedAndUnknown(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)

	if err := tokens.CreateRefresh(ctx, u.ID, uuid.New(), hashOf("EXP"), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ConsumeRefresh(ctx, hashOf("EXP"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("expired err = %v", err)
	}

	revokedFamily := uuid.New()
	if err := tokens.CreateRefresh(ctx, u.ID, revokedFamily, hashOf("REV"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tokens.RevokeFamily(ctx, revokedFamily, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ConsumeRefresh(ctx, hashOf("REV"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("revoked err = %v", err)
	}

	if _, err := tokens.ConsumeRefresh(ctx, hashOf("unknown"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("unknown consume err = %v", err)
	}
	if _, err := tokens.FindRefresh(ctx, hashOf("unknown")); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("unknown find err = %v", err)
	}
}

func TestConsumeRefreshConcurrentHasOneWinner(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	u := mustUser(t, users)
	if err := tokens.CreateRefresh(t.Context(), u.ID, uuid.New(), hashOf("A"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = tokens.ConsumeRefresh(context.Background(), hashOf("A"), t0)
		}(i)
	}
	wg.Wait()

	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, entity.ErrTokenInvalid):
			t.Fatalf("consumer %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent consumers succeeded, want exactly 1", wins)
	}
}

func TestRevokeFamilyOfAndAllForUser(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	family, other := uuid.New(), uuid.New()
	for token, fam := range map[string]uuid.UUID{"A": family, "B": family, "C": other} {
		if err := tokens.CreateRefresh(ctx, u.ID, fam, hashOf(token), t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tokens.RevokeFamilyOf(ctx, hashOf("A"), t0); err != nil {
		t.Fatalf("RevokeFamilyOf: %v", err)
	}
	if b, _ := tokens.FindRefresh(ctx, hashOf("B")); b.RevokedAt == nil {
		t.Fatal("sibling B not revoked with its family")
	}
	if c, _ := tokens.FindRefresh(ctx, hashOf("C")); c.RevokedAt != nil {
		t.Fatal("token from another family was revoked")
	}
	if err := tokens.RevokeFamilyOf(ctx, hashOf("unknown"), t0); err != nil {
		t.Fatalf("RevokeFamilyOf(unknown) = %v, want nil", err)
	}
	if err := tokens.RevokeAllForUser(ctx, u.ID, t0); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if c, _ := tokens.FindRefresh(ctx, hashOf("C")); c.RevokedAt == nil {
		t.Fatal("RevokeAllForUser missed a family")
	}
}

func TestPasswordResetTokens(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	for token, exp := range map[string]time.Time{"R1": t0.Add(30 * time.Minute), "R2": t0.Add(30 * time.Minute), "R3": t0} {
		if err := tokens.CreatePasswordReset(ctx, u.ID, hashOf(token), exp); err != nil {
			t.Fatal(err)
		}
	}

	got, err := tokens.ConsumePasswordReset(ctx, hashOf("R1"), t0)
	if err != nil || got != u.ID {
		t.Fatalf("ConsumePasswordReset = %v, %v", got, err)
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R1"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("reuse err = %v", err)
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R3"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("expired err = %v", err)
	}
	if err := tokens.InvalidatePasswordResets(ctx, u.ID, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R2"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("invalidated token err = %v", err)
	}
}

func TestRepositoriesJoinTheUseCaseTransaction(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	tm := database.NewTxManager(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	if err := tokens.CreatePasswordReset(ctx, u.ID, hashOf("R"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	failure := errors.New("later step failed")
	err := tm.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R"), t0); err != nil {
			return err
		}
		if err := users.UpdatePassword(ctx, u.ID, "new-hash", t0); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("WithinTx err = %v", err)
	}
	got, _ := users.GetByID(ctx, u.ID)
	if got.PasswordHash != "old-hash" {
		t.Fatal("password change survived the rollback")
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R"), t0); err != nil {
		t.Fatalf("reset token consumption survived the rollback: %v", err)
	}
}
