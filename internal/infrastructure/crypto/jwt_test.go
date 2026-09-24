package crypto

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

var testSecret = strings.Repeat("s", 32)

func TestIssueAndVerify(t *testing.T) {
	ti := NewJWTIssuer(testSecret, 15*time.Minute)
	id := uuid.New()
	tok, ttl, err := ti.Issue(id)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if ttl != 15*time.Minute {
		t.Errorf("ttl = %v, want 15m", ttl)
	}
	got, err := ti.Verify(tok)
	if err != nil || got != id {
		t.Fatalf("Verify = %v, %v; want %v", got, err, id)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	ti := NewJWTIssuer(testSecret, time.Minute)
	start := time.Now()
	ti.now = func() time.Time { return start }
	tok, _, err := ti.Issue(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	ti.now = func() time.Time { return start.Add(2 * time.Minute) }
	if _, err := ti.Verify(tok); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyRejectsForeignSignature(t *testing.T) {
	tok, _, _ := NewJWTIssuer(strings.Repeat("x", 32), time.Minute).Issue(uuid.New())
	if _, err := NewJWTIssuer(testSecret, time.Minute).Verify(tok); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyRejectsWrongAlgorithmAudienceAndMissingExpiry(t *testing.T) {
	ti := NewJWTIssuer(testSecret, time.Minute)
	base := jwt.RegisteredClaims{
		Subject:   uuid.NewString(),
		Issuer:    tokenIssuer,
		Audience:  jwt.ClaimStrings{tokenAudience},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	sign := func(m jwt.SigningMethod, c jwt.RegisteredClaims) string {
		s, err := jwt.NewWithClaims(m, c).SignedString([]byte(testSecret))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	wrongAud := base
	wrongAud.Audience = jwt.ClaimStrings{"ws"}
	noExp := base
	noExp.ExpiresAt = nil
	badSub := base
	badSub.Subject = "not-a-uuid"

	for name, tok := range map[string]string{
		"HS512":          sign(jwt.SigningMethodHS512, base),
		"wrong audience": sign(jwt.SigningMethodHS256, wrongAud),
		"no expiry":      sign(jwt.SigningMethodHS256, noExp),
		"bad subject":    sign(jwt.SigningMethodHS256, badSub),
		"garbage":        "not.a.jwt",
	} {
		if _, err := ti.Verify(tok); !errors.Is(err, entity.ErrTokenInvalid) {
			t.Errorf("%s: err = %v, want ErrTokenInvalid", name, err)
		}
	}
}
