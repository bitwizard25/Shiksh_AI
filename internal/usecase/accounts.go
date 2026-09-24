package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// ProfileInput changes only its non-nil fields.
type ProfileInput struct {
	DisplayName   *string
	PreferredLang *string
	Grade         *int
}

// Accounts is the interactor for a signed-in learner's own account.
type Accounts struct {
	users  UserRepository
	hasher PasswordHasher
	now    func() time.Time
}

// NewAccounts builds the interactor. now defaults to time.Now.
func NewAccounts(users UserRepository, hasher PasswordHasher, now func() time.Time) *Accounts {
	if now == nil {
		now = time.Now
	}
	return &Accounts{users: users, hasher: hasher, now: now}
}

// Me returns the learner's profile.
func (a *Accounts) Me(ctx context.Context, userID uuid.UUID) (entity.User, error) {
	return a.users.GetByID(ctx, userID)
}

// UpdateProfile validates and applies the non-nil fields.
func (a *Accounts) UpdateProfile(ctx context.Context, userID uuid.UUID, in ProfileInput) (entity.User, error) {
	var patch ProfilePatch
	if in.DisplayName != nil {
		name, err := entity.ParseDisplayName(*in.DisplayName)
		if err != nil {
			return entity.User{}, err
		}
		patch.DisplayName = &name
	}
	if in.PreferredLang != nil {
		if err := entity.ValidateLanguage(*in.PreferredLang); err != nil {
			return entity.User{}, err
		}
		patch.PreferredLang = in.PreferredLang
	}
	if in.Grade != nil {
		if err := entity.ValidateGrade(in.Grade); err != nil {
			return entity.User{}, err
		}
		patch.Grade = in.Grade
	}
	return a.users.UpdateProfile(ctx, userID, patch, a.now())
}

// DeleteAccount permanently deletes the account after re-checking the password.
func (a *Accounts) DeleteAccount(ctx context.Context, userID uuid.UUID, password string) error {
	user, err := a.users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := a.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return entity.ErrInvalidCredentials
	}
	return a.users.Delete(ctx, userID)
}
