package repository_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestUsersCreateAndGet(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	grade := 7
	in := newUserInput("asha@example.com")
	in.Grade, in.GuardianConsent = &grade, true

	created, err := users.Create(ctx, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil || created.Email != "asha@example.com" || created.Grade == nil || *created.Grade != 7 {
		t.Fatalf("created = %+v", created)
	}
	if !created.TermsAcceptedAt.Equal(t0) || !created.CreatedAt.Equal(t0) || created.GuardianConsentAt == nil || !created.GuardianConsentAt.Equal(t0) {
		t.Fatalf("timestamps not taken from AcceptedAt: %+v", created)
	}
	byEmail, err := users.GetByEmail(ctx, "asha@example.com")
	if err != nil || byEmail.ID != created.ID {
		t.Fatalf("GetByEmail = %+v, %v", byEmail, err)
	}
	byID, err := users.GetByID(ctx, created.ID)
	if err != nil || byID.Email != "asha@example.com" {
		t.Fatalf("GetByID = %+v, %v", byID, err)
	}

	noConsent, err := users.Create(ctx, newUserInput("ravi@example.com"))
	if err != nil || noConsent.GuardianConsentAt != nil {
		t.Fatalf("consent should be nil when not given: %+v, %v", noConsent, err)
	}
}

func TestUsersEmailUniqueIgnoringCase(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	if _, err := users.Create(ctx, newUserInput("dup@example.com")); err != nil {
		t.Fatal(err)
	}
	// The database index is the last line of defence even if a caller skips normalization.
	if _, err := users.Create(ctx, newUserInput("DUP@example.com")); !errors.Is(err, entity.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestUsersMissingReturnsNotFound(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	if _, err := users.GetByID(ctx, uuid.New()); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("GetByID err = %v", err)
	}
	if _, err := users.GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("GetByEmail err = %v", err)
	}
	if err := users.UpdatePassword(ctx, uuid.New(), "h", t0); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("UpdatePassword err = %v", err)
	}
	if err := users.Delete(ctx, uuid.New()); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("Delete err = %v", err)
	}
}

func TestUsersUpdateProfileChangesOnlyGivenFields(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	u := mustUser(t, users)
	later := t0.Add(time.Hour)

	name, grade := "Asha K", 9
	updated, err := users.UpdateProfile(ctx, u.ID, usecase.ProfilePatch{DisplayName: &name, Grade: &grade}, later)
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.DisplayName != "Asha K" || *updated.Grade != 9 || updated.PreferredLang != "hi" || !updated.UpdatedAt.Equal(later) {
		t.Fatalf("updated = %+v", updated)
	}
	same, err := users.UpdateProfile(ctx, u.ID, usecase.ProfilePatch{}, later)
	if err != nil || same.DisplayName != "Asha K" || *same.Grade != 9 {
		t.Fatalf("empty patch changed fields: %+v, %v", same, err)
	}
	if _, err := users.UpdateProfile(ctx, uuid.New(), usecase.ProfilePatch{}, later); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("unknown user err = %v", err)
	}
}

func TestUsersUpdatePassword(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	u := mustUser(t, users)
	if err := users.UpdatePassword(ctx, u.ID, "new-hash", t0.Add(time.Minute)); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	got, _ := users.GetByID(ctx, u.ID)
	if got.PasswordHash != "new-hash" {
		t.Fatalf("PasswordHash = %q", got.PasswordHash)
	}
}

func TestUsersDeleteCascadesToTokens(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	if err := tokens.CreateRefresh(ctx, u.ID, uuid.New(), hashOf("A"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tokens.FindRefresh(ctx, hashOf("A")); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("token survived user deletion: %v", err)
	}
}
