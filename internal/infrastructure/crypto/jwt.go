package crypto

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const (
	tokenIssuer   = "shiksha-ai"
	tokenAudience = "api"
)

// JWTIssuer signs and verifies short-lived HS256 access tokens.
type JWTIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

var _ usecase.AccessTokens = (*JWTIssuer)(nil)

// NewJWTIssuer creates an issuer. The secret must be at least 32 bytes (config enforces this).
func NewJWTIssuer(secret string, ttl time.Duration) *JWTIssuer {
	return &JWTIssuer{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// Issue returns a signed access token for userID and its lifetime.
func (ti *JWTIssuer) Issue(userID uuid.UUID) (string, time.Duration, error) {
	now := ti.now()
	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		Issuer:    tokenIssuer,
		Audience:  jwt.ClaimStrings{tokenAudience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ti.ttl)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.secret)
	if err != nil {
		return "", 0, fmt.Errorf("sign access token: %w", err)
	}
	return signed, ti.ttl, nil
}

// Verify checks signature, algorithm, issuer, audience and expiry and returns the user id.
// Every failure is reported as entity.ErrTokenInvalid.
func (ti *JWTIssuer) Verify(token string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return ti.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(5*time.Second),
		jwt.WithTimeFunc(ti.now),
	)
	if err != nil {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	return id, nil
}
