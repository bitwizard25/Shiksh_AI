package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// AuthConfig tunes token lifetimes and links.
type AuthConfig struct {
	RefreshTTL        time.Duration // lifetime of each refresh token (720h)
	RefreshReuseGrace time.Duration // reuse inside this window does not revoke the family (20s)
	ResetTTL          time.Duration // lifetime of password reset links (30m)
	AppBaseURL        string        // reset links point at <AppBaseURL>/reset?token=...
}

// AuthDeps are the ports the Auth interactor needs.
type AuthDeps struct {
	Users  UserRepository
	Tokens TokenRepository
	Tx     TxManager
	Hasher PasswordHasher
	Access AccessTokens
	Opaque OpaqueTokens
	Mailer Mailer
	Now    func() time.Time // defaults to time.Now
}

// Auth is the authentication interactor: sign-up, sign-in, token rotation and password reset.
type Auth struct {
	d   AuthDeps
	cfg AuthConfig
}

func NewAuth(d AuthDeps, cfg AuthConfig) *Auth {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Auth{d: d, cfg: cfg}
}

// TokenPair is returned by Register, Login and Refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
}

// RegisterInput is the sign-up form.
type RegisterInput struct {
	Email           string
	Password        string
	DisplayName     string
	PreferredLang   string // defaults to entity.DefaultLanguage
	Grade           *int
	TermsAccepted   bool
	GuardianConsent bool
}

// Register validates the form, creates the account and its first refresh token atomically, and signs the user in.
func (a *Auth) Register(ctx context.Context, in RegisterInput) (entity.User, TokenPair, error) {
	email, err := entity.ParseEmail(in.Email)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if err := entity.ValidatePassword("password", in.Password); err != nil {
		return entity.User{}, TokenPair{}, err
	}
	name, err := entity.ParseDisplayName(in.DisplayName)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if in.PreferredLang == "" {
		in.PreferredLang = entity.DefaultLanguage
	}
	if err := entity.ValidateLanguage(in.PreferredLang); err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if err := entity.ValidateGrade(in.Grade); err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if !in.TermsAccepted {
		return entity.User{}, TokenPair{}, &entity.ValidationError{Field: "terms_accepted", Message: "must be accepted"}
	}

	hash, err := a.d.Hasher.Hash(ctx, in.Password)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	now := a.d.Now()
	var (
		user entity.User
		pair TokenPair
	)
	err = a.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		user, err = a.d.Users.Create(ctx, NewUser{
			Email: email, PasswordHash: hash, DisplayName: name, PreferredLang: in.PreferredLang,
			Grade: in.Grade, AcceptedAt: now, GuardianConsent: in.GuardianConsent,
		})
		if err != nil {
			return err
		}
		pair, err = a.issuePair(ctx, user.ID, now)
		return err
	})
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	return user, pair, nil
}

// Login checks credentials. Unknown or malformed emails cost the same time as a wrong password.
func (a *Auth) Login(ctx context.Context, email, password string) (entity.User, TokenPair, error) {
	addr, err := entity.ParseEmail(email)
	if err != nil {
		a.d.Hasher.VerifyDummy(ctx, password)
		return entity.User{}, TokenPair{}, entity.ErrInvalidCredentials
	}
	user, err := a.d.Users.GetByEmail(ctx, addr)
	if errors.Is(err, entity.ErrNotFound) {
		a.d.Hasher.VerifyDummy(ctx, password)
		return entity.User{}, TokenPair{}, entity.ErrInvalidCredentials
	}
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	ok, err := a.d.Hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if !ok {
		return entity.User{}, TokenPair{}, entity.ErrInvalidCredentials
	}
	pair, err := a.issuePair(ctx, user.ID, a.d.Now())
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	return user, pair, nil
}

// Authenticate validates an access token and returns the user id.
func (a *Auth) Authenticate(accessToken string) (uuid.UUID, error) {
	return a.d.Access.Verify(accessToken)
}

// issuePair creates an access token and the first refresh token of a new rotation family.
func (a *Auth) issuePair(ctx context.Context, userID uuid.UUID, now time.Time) (TokenPair, error) {
	access, ttl, err := a.d.Access.Issue(userID)
	if err != nil {
		return TokenPair{}, err
	}
	plain, hash, err := a.d.Opaque.New()
	if err != nil {
		return TokenPair{}, err
	}
	if err := a.d.Tokens.CreateRefresh(ctx, userID, uuid.New(), hash, now.Add(a.cfg.RefreshTTL)); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, ExpiresIn: ttl}, nil
}

// Refresh rotates a refresh token: it consumes the presented token and issues a successor in the
// same family, atomically. Presenting a token that was rotated more than RefreshReuseGrace ago
// revokes the whole family (theft detection). Reuse inside the grace window is only rejected.
func (a *Auth) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	now := a.d.Now()
	oldHash := a.d.Opaque.Hash(refreshToken)
	plain, newHash, err := a.d.Opaque.New()
	if err != nil {
		return TokenPair{}, err
	}
	var userID uuid.UUID
	err = a.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		tok, err := a.d.Tokens.ConsumeRefresh(ctx, oldHash, now)
		if err != nil {
			return err
		}
		userID = tok.UserID
		return a.d.Tokens.CreateRefresh(ctx, tok.UserID, tok.FamilyID, newHash, now.Add(a.cfg.RefreshTTL))
	})
	if errors.Is(err, entity.ErrTokenInvalid) {
		if rerr := a.revokeIfReplayed(ctx, oldHash, now); rerr != nil {
			return TokenPair{}, rerr
		}
		return TokenPair{}, entity.ErrTokenInvalid
	}
	if err != nil {
		return TokenPair{}, err
	}
	access, ttl, err := a.d.Access.Issue(userID)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, ExpiresIn: ttl}, nil
}

// revokeIfReplayed revokes the family of an already-rotated token presented after the grace window.
func (a *Auth) revokeIfReplayed(ctx context.Context, hash []byte, now time.Time) error {
	tok, err := a.d.Tokens.FindRefresh(ctx, hash)
	if errors.Is(err, entity.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if tok.IsReplay(now, a.cfg.RefreshReuseGrace) {
		return a.d.Tokens.RevokeFamily(ctx, tok.FamilyID, now)
	}
	return nil
}

// Logout revokes the refresh token's whole family. Unknown tokens are ignored.
func (a *Auth) Logout(ctx context.Context, refreshToken string) error {
	return a.d.Tokens.RevokeFamilyOf(ctx, a.d.Opaque.Hash(refreshToken), a.d.Now())
}

// ForgotPassword emails a single-use reset link. It never reveals whether the account exists.
func (a *Auth) ForgotPassword(ctx context.Context, email string) error {
	addr, err := entity.ParseEmail(email)
	if err != nil {
		return nil // not an address anyone could have registered with
	}
	user, err := a.d.Users.GetByEmail(ctx, addr)
	if errors.Is(err, entity.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, hash, err := a.d.Opaque.New()
	if err != nil {
		return err
	}
	if err := a.d.Tokens.CreatePasswordReset(ctx, user.ID, hash, a.d.Now().Add(a.cfg.ResetTTL)); err != nil {
		return err
	}
	link := strings.TrimRight(a.cfg.AppBaseURL, "/") + "/reset?token=" + url.QueryEscape(plain)
	return a.d.Mailer.Send(ctx, Message{
		To:      user.Email.String(),
		Subject: "Reset your Shiksha AI password",
		Body: fmt.Sprintf("Hi %s,\n\nUse this link within %d minutes to choose a new password:\n\n%s\n\nIf you did not ask for this, you can ignore this email.\n",
			user.DisplayName, int(a.cfg.ResetTTL.Minutes()), link),
	})
}

// ResetPassword sets a new password using a reset token. In one transaction it consumes the token,
// updates the password, invalidates the user's other reset links and signs them out everywhere.
func (a *Auth) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := entity.ValidatePassword("new_password", newPassword); err != nil {
		return err
	}
	hash, err := a.d.Hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	now := a.d.Now()
	return a.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		userID, err := a.d.Tokens.ConsumePasswordReset(ctx, a.d.Opaque.Hash(token), now)
		if err != nil {
			return err
		}
		if err := a.d.Users.UpdatePassword(ctx, userID, hash, now); err != nil {
			return err
		}
		if err := a.d.Tokens.InvalidatePasswordResets(ctx, userID, now); err != nil {
			return err
		}
		return a.d.Tokens.RevokeAllForUser(ctx, userID, now)
	})
}
