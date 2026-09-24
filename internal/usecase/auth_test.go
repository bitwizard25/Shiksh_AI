package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestRegisterCreatesUserAndTokens(t *testing.T) {
	e := newEnv(t)
	in := validRegistration()
	in.Email = "  Asha@Example.com "
	in.GuardianConsent = true
	user, pair, err := e.auth.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.Email != "asha@example.com" || user.PreferredLang != entity.DefaultLanguage || user.PasswordHash != "hashed:correct horse" {
		t.Fatalf("user = %+v", user)
	}
	if !user.TermsAcceptedAt.Equal(e.clock.Now()) || user.GuardianConsentAt == nil {
		t.Fatalf("consent timestamps = %v, %v", user.TermsAcceptedAt, user.GuardianConsentAt)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.ExpiresIn != 15*time.Minute {
		t.Fatalf("pair = %+v", pair)
	}
	if id, err := e.auth.Authenticate(pair.AccessToken); err != nil || id != user.ID {
		t.Fatalf("Authenticate = %v, %v; want %v", id, err, user.ID)
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	e := newEnv(t)
	mustRegister(t, e)
	in := validRegistration()
	in.Email = "ASHA@example.com"
	if _, _, err := e.auth.Register(context.Background(), in); !errors.Is(err, entity.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := newEnv(t)
	grade13 := 13
	cases := []struct {
		name   string
		mutate func(*usecase.RegisterInput)
		field  string
	}{
		{"bad email", func(in *usecase.RegisterInput) { in.Email = "not-an-email" }, "email"},
		{"short password", func(in *usecase.RegisterInput) { in.Password = "short" }, "password"},
		{"blank name", func(in *usecase.RegisterInput) { in.DisplayName = "   " }, "display_name"},
		{"unsupported lang", func(in *usecase.RegisterInput) { in.PreferredLang = "xx" }, "preferred_lang"},
		{"grade out of range", func(in *usecase.RegisterInput) { in.Grade = &grade13 }, "grade"},
		{"terms not accepted", func(in *usecase.RegisterInput) { in.TermsAccepted = false }, "terms_accepted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validRegistration()
			tc.mutate(&in)
			_, _, err := e.auth.Register(context.Background(), in)
			var ve *entity.ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("err = %v, want validation error on %q", err, tc.field)
			}
		})
	}
}

func TestRegisterAcceptsDevanagariNameAndPassword(t *testing.T) {
	e := newEnv(t)
	in := validRegistration()
	in.DisplayName = "आशा"
	in.Password = "पासवर्ड१२" // 9 runes, 27 bytes
	user, _, err := e.auth.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.DisplayName != "आशा" {
		t.Fatalf("DisplayName = %q", user.DisplayName)
	}
	if _, _, err := e.auth.Login(context.Background(), in.Email, in.Password); err != nil {
		t.Fatalf("Login with Devanagari password: %v", err)
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user, _ := mustRegister(t, e)

	got, pair, err := e.auth.Login(ctx, " ASHA@Example.com ", "correct horse")
	if err != nil || got.ID != user.ID || pair.RefreshToken == "" {
		t.Fatalf("Login with differently typed email = %+v, %+v, %v", got, pair, err)
	}
	if _, _, err := e.auth.Login(ctx, "asha@example.com", "wrong horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}

	before := e.hasher.dummyCalls.Load()
	if _, _, err := e.auth.Login(ctx, "nobody@example.com", "correct horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v", err)
	}
	if _, _, err := e.auth.Login(ctx, "not an email", "correct horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("malformed email err = %v", err)
	}
	if got := e.hasher.dummyCalls.Load() - before; got != 2 {
		t.Fatalf("VerifyDummy called %d times for unknown/malformed emails, want 2 (timing equalization)", got)
	}
}

func TestAuthenticateRejectsGarbage(t *testing.T) {
	e := newEnv(t)
	if _, err := e.auth.Authenticate("garbage"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}
