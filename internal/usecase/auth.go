package usecase

import (
	"context"
	"errors"
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
