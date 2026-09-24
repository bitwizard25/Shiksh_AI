package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestUpdateProfile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user, _ := mustRegister(t, e)

	name, code, grade := " Asha K ", "mr", 8
	got, err := e.accounts.UpdateProfile(ctx, user.ID, usecase.ProfileInput{DisplayName: &name, PreferredLang: &code, Grade: &grade})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if got.DisplayName != "Asha K" || got.PreferredLang != "mr" || got.Grade == nil || *got.Grade != 8 {
		t.Fatalf("got = %+v", got)
	}

	bad := "xx"
	var ve *entity.ValidationError
	if _, err := e.accounts.UpdateProfile(ctx, user.ID, usecase.ProfileInput{PreferredLang: &bad}); !errors.As(err, &ve) || ve.Field != "preferred_lang" {
		t.Fatalf("bad lang err = %v", err)
	}

	same, err := e.accounts.UpdateProfile(ctx, user.ID, usecase.ProfileInput{})
	if err != nil || same.DisplayName != "Asha K" || same.PreferredLang != "mr" || *same.Grade != 8 {
		t.Fatalf("empty update = %+v, %v; want unchanged", same, err)
	}
	if _, err := e.accounts.UpdateProfile(ctx, uuid.New(), usecase.ProfileInput{}); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("unknown user err = %v, want ErrNotFound", err)
	}
}

func TestDeleteAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user, _ := mustRegister(t, e)

	if err := e.accounts.DeleteAccount(ctx, user.ID, "wrong horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}
	if err := e.accounts.DeleteAccount(ctx, user.ID, "correct horse"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, err := e.accounts.Me(ctx, user.ID); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("Me after delete err = %v, want ErrNotFound", err)
	}
}

func TestLanguagesCatalog(t *testing.T) {
	langs := usecase.Languages()
	if len(langs) != 9 || langs[0].Code != "hi" {
		t.Fatalf("Languages() = %+v", langs)
	}
}
