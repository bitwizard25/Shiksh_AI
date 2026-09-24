package usecase_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

var resetLink = regexp.MustCompile(`/reset\?token=([A-Za-z0-9_-]+)`)

func resetTokenFrom(t *testing.T, body string) string {
	t.Helper()
	m := resetLink.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no reset link in email body:\n%s", body)
	}
	return m[1]
}

func TestRefreshRotates(t *testing.T) {
	e := newEnv(t)
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(context.Background(), pair.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if next.RefreshToken == pair.RefreshToken || next.ExpiresIn != 15*time.Minute {
		t.Fatalf("next = %+v", next)
	}
	if _, err := e.auth.Authenticate(next.AccessToken); err != nil {
		t.Fatalf("new access token invalid: %v", err)
	}
}

func TestRefreshReuseInsideGraceKeepsFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(5 * time.Second)
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("reuse err = %v, want ErrTokenInvalid", err)
	}
	if _, err := e.auth.Refresh(ctx, next.RefreshToken); err != nil {
		t.Fatalf("successor should survive an in-grace reuse: %v", err)
	}
}

func TestRefreshReplayAfterGraceRevokesFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(reuseGrace + time.Second)
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("replay err = %v, want ErrTokenInvalid", err)
	}
	if _, err := e.auth.Refresh(ctx, next.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("successor after replay err = %v, want ErrTokenInvalid (family revoked)", err)
	}
}

func TestRefreshRejectsExpiredAndUnknown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	if _, err := e.auth.Refresh(ctx, "never-issued"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("unknown err = %v", err)
	}
	e.clock.Advance(refreshTTL)
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("expired err = %v", err)
	}
}

func TestLogoutRevokesWholeFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.auth.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := e.auth.Refresh(ctx, next.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("refresh after logout err = %v", err)
	}
	if err := e.auth.Logout(ctx, "garbage"); err != nil {
		t.Fatalf("Logout(unknown) = %v, want nil", err)
	}
}

func TestForgotAndResetPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)

	if err := e.auth.ForgotPassword(ctx, "ASHA@example.com"); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	msg := e.mailer.last(t)
	if msg.To != "asha@example.com" || !strings.Contains(msg.Body, "https://app.example/reset?token=") {
		t.Fatalf("email = %+v", msg)
	}
	token := resetTokenFrom(t, msg.Body)

	var ve *entity.ValidationError
	if err := e.auth.ResetPassword(ctx, token, "short"); !errors.As(err, &ve) || ve.Field != "new_password" {
		t.Fatalf("short new password err = %v", err)
	}
	if err := e.auth.ResetPassword(ctx, token, "new password 1"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, _, err := e.auth.Login(ctx, "asha@example.com", "correct horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, _, err := e.auth.Login(ctx, "asha@example.com", "new password 1"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("old sessions survived the reset: %v", err)
	}
	if err := e.auth.ResetPassword(ctx, token, "another password"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("reset token reused: %v", err)
	}
	if err := e.auth.ResetPassword(ctx, "bogus", "long enough pw"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("bogus token err = %v", err)
	}
}

func TestResetInvalidatesOtherOutstandingLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mustRegister(t, e)
	for range 2 {
		if err := e.auth.ForgotPassword(ctx, "asha@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	e.mailer.mu.Lock()
	first, second := resetTokenFrom(t, e.mailer.sent[0].Body), resetTokenFrom(t, e.mailer.sent[1].Body)
	e.mailer.mu.Unlock()

	if err := e.auth.ResetPassword(ctx, second, "new password 1"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if err := e.auth.ResetPassword(ctx, first, "new password 2"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("older link still works: %v", err)
	}
}

func TestResetLinkExpires(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mustRegister(t, e)
	if err := e.auth.ForgotPassword(ctx, "asha@example.com"); err != nil {
		t.Fatal(err)
	}
	token := resetTokenFrom(t, e.mailer.last(t).Body)
	e.clock.Advance(resetTTL)
	if err := e.auth.ResetPassword(ctx, token, "new password 1"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("expired link err = %v", err)
	}
}

func TestForgotPasswordUnknownOrMalformedEmailSendsNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, email := range []string{"nobody@example.com", "not an email"} {
		if err := e.auth.ForgotPassword(ctx, email); err != nil {
			t.Fatalf("ForgotPassword(%q) = %v, want nil", email, err)
		}
	}
	if e.mailer.count() != 0 {
		t.Fatal("email sent for an account that does not exist")
	}
}
