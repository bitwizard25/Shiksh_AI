// Package usecase holds the application business rules (interactors) and the ports they need.
// It imports only internal/entity from this module; outer layers implement the ports.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// NewUser is the input to UserRepository.Create.
type NewUser struct {
	Email           entity.Email
	PasswordHash    string
	DisplayName     string
	PreferredLang   string
	Grade           *int
	AcceptedAt      time.Time // terms (and guardian consent, when given) were accepted at this time
	GuardianConsent bool
}

// ProfilePatch changes only its non-nil fields.
type ProfilePatch struct {
	DisplayName   *string
	PreferredLang *string
	Grade         *int
}

// UserRepository persists learner accounts.
type UserRepository interface {
	// Create returns entity.ErrEmailTaken if the email is already registered.
	Create(ctx context.Context, u NewUser) (entity.User, error)
	// GetByEmail and GetByID return entity.ErrNotFound if there is no such user.
	GetByEmail(ctx context.Context, email entity.Email) (entity.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (entity.User, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, p ProfilePatch, now time.Time) (entity.User, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string, now time.Time) error
	// Delete removes the user and everything they own. entity.ErrNotFound if absent.
	Delete(ctx context.Context, id uuid.UUID) error
}

// TokenRepository persists refresh and password-reset tokens by their hashes.
type TokenRepository interface {
	CreateRefresh(ctx context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error
	// ConsumeRefresh marks an unused, unrevoked, unexpired token as used at now and returns it.
	// Any other token gets entity.ErrTokenInvalid. At most one concurrent caller succeeds.
	ConsumeRefresh(ctx context.Context, hash []byte, now time.Time) (entity.RefreshToken, error)
	// FindRefresh returns the token in any state, or entity.ErrNotFound.
	FindRefresh(ctx context.Context, hash []byte) (entity.RefreshToken, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID, now time.Time) error
	// RevokeFamilyOf revokes the family of the token with this hash; unknown hashes are a no-op.
	RevokeFamilyOf(ctx context.Context, hash []byte, now time.Time) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID, now time.Time) error
	CreatePasswordReset(ctx context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error
	// ConsumePasswordReset marks a valid reset token used and returns its user, or entity.ErrTokenInvalid.
	ConsumePasswordReset(ctx context.Context, hash []byte, now time.Time) (uuid.UUID, error)
	InvalidatePasswordResets(ctx context.Context, userID uuid.UUID, now time.Time) error
}

// TxManager runs fn atomically. Repositories called with the ctx passed to fn join the transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// PasswordHasher hashes and verifies passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, plain string) (string, error)
	Verify(ctx context.Context, plain, encoded string) (bool, error)
	// VerifyDummy costs as much as Verify; call it when no account exists so timing reveals nothing.
	VerifyDummy(ctx context.Context, plain string)
}

// AccessTokens issues and verifies short-lived access tokens.
type AccessTokens interface {
	Issue(userID uuid.UUID) (token string, ttl time.Duration, err error)
	// Verify returns entity.ErrTokenInvalid for any invalid or expired token.
	Verify(token string) (uuid.UUID, error)
}

// OpaqueTokens creates random secrets (refresh and reset tokens) and hashes them for storage.
type OpaqueTokens interface {
	New() (plain string, hash []byte, err error)
	Hash(plain string) []byte
}

// Message is a plain-text email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Mailer sends email.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}
