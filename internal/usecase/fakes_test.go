package usecase_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// memUsers is an in-memory UserRepository with the same contract as the Postgres one.
type memUsers struct {
	mu   sync.Mutex
	byID map[uuid.UUID]entity.User
}

func (m *memUsers) Create(_ context.Context, u usecase.NewUser) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.byID {
		if existing.Email == u.Email {
			return entity.User{}, entity.ErrEmailTaken
		}
	}
	user := entity.User{
		ID: uuid.New(), Email: u.Email, PasswordHash: u.PasswordHash, DisplayName: u.DisplayName,
		PreferredLang: u.PreferredLang, Grade: u.Grade,
		TermsAcceptedAt: u.AcceptedAt, CreatedAt: u.AcceptedAt, UpdatedAt: u.AcceptedAt,
	}
	if u.GuardianConsent {
		at := u.AcceptedAt
		user.GuardianConsentAt = &at
	}
	m.byID[user.ID] = user
	return user, nil
}

func (m *memUsers) GetByEmail(_ context.Context, email entity.Email) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.byID {
		if u.Email == email {
			return u, nil
		}
	}
	return entity.User{}, entity.ErrNotFound
}

func (m *memUsers) GetByID(_ context.Context, id uuid.UUID) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return entity.User{}, entity.ErrNotFound
	}
	return u, nil
}

func (m *memUsers) UpdateProfile(_ context.Context, id uuid.UUID, p usecase.ProfilePatch, now time.Time) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return entity.User{}, entity.ErrNotFound
	}
	if p.DisplayName != nil {
		u.DisplayName = *p.DisplayName
	}
	if p.PreferredLang != nil {
		u.PreferredLang = *p.PreferredLang
	}
	if p.Grade != nil {
		g := *p.Grade
		u.Grade = &g
	}
	u.UpdatedAt = now
	m.byID[id] = u
	return u, nil
}

func (m *memUsers) UpdatePassword(_ context.Context, id uuid.UUID, hash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return entity.ErrNotFound
	}
	u.PasswordHash, u.UpdatedAt = hash, now
	m.byID[id] = u
	return nil
}

func (m *memUsers) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return entity.ErrNotFound
	}
	delete(m.byID, id)
	return nil
}

// memTokens is an in-memory TokenRepository with the same contract as the Postgres one.
type memTokens struct {
	mu      sync.Mutex
	refresh map[string]*entity.RefreshToken
	resets  map[string]*memReset
}

type memReset struct {
	userID    uuid.UUID
	expiresAt time.Time
	usedAt    *time.Time
}

func (m *memTokens) CreateRefresh(_ context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh[string(hash)] = &entity.RefreshToken{UserID: userID, FamilyID: familyID, ExpiresAt: expiresAt}
	return nil
}

func (m *memTokens) ConsumeRefresh(_ context.Context, hash []byte, now time.Time) (entity.RefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.refresh[string(hash)]
	if !ok || tok.UsedAt != nil || tok.RevokedAt != nil || !now.Before(tok.ExpiresAt) {
		return entity.RefreshToken{}, entity.ErrTokenInvalid
	}
	used := now
	tok.UsedAt = &used
	return *tok, nil
}

func (m *memTokens) FindRefresh(_ context.Context, hash []byte) (entity.RefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.refresh[string(hash)]
	if !ok {
		return entity.RefreshToken{}, entity.ErrNotFound
	}
	return *tok, nil
}

func (m *memTokens) RevokeFamily(_ context.Context, familyID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokeWhere(func(t *entity.RefreshToken) bool { return t.FamilyID == familyID }, now)
	return nil
}

func (m *memTokens) RevokeFamilyOf(_ context.Context, hash []byte, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.refresh[string(hash)]
	if !ok {
		return nil
	}
	family := tok.FamilyID
	m.revokeWhere(func(t *entity.RefreshToken) bool { return t.FamilyID == family }, now)
	return nil
}

func (m *memTokens) RevokeAllForUser(_ context.Context, userID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokeWhere(func(t *entity.RefreshToken) bool { return t.UserID == userID }, now)
	return nil
}

func (m *memTokens) revokeWhere(match func(*entity.RefreshToken) bool, now time.Time) {
	for _, t := range m.refresh {
		if t.RevokedAt == nil && match(t) {
			at := now
			t.RevokedAt = &at
		}
	}
}

func (m *memTokens) CreatePasswordReset(_ context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resets[string(hash)] = &memReset{userID: userID, expiresAt: expiresAt}
	return nil
}

func (m *memTokens) ConsumePasswordReset(_ context.Context, hash []byte, now time.Time) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resets[string(hash)]
	if !ok || r.usedAt != nil || !now.Before(r.expiresAt) {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	used := now
	r.usedAt = &used
	return r.userID, nil
}

func (m *memTokens) InvalidatePasswordResets(_ context.Context, userID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.resets {
		if r.userID == userID && r.usedAt == nil {
			used := now
			r.usedAt = &used
		}
	}
	return nil
}

// noTx runs fn directly: the in-memory fakes need no transaction. Atomicity is tested against
// Postgres in the repository tests.
type noTx struct{}

func (noTx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

// fakeHasher "hashes" by prefixing and counts dummy verifications.
type fakeHasher struct {
	dummyCalls atomic.Int32
	hashCalls  atomic.Int32
}

func (h *fakeHasher) Hash(_ context.Context, plain string) (string, error) {
	h.hashCalls.Add(1)
	return "hashed:" + plain, nil
}

func (h *fakeHasher) Verify(_ context.Context, plain, encoded string) (bool, error) {
	return encoded == "hashed:"+plain, nil
}

func (h *fakeHasher) VerifyDummy(context.Context, string) { h.dummyCalls.Add(1) }

// fakeAccess issues readable access tokens: "access:<user id>".
type fakeAccess struct{}

func (fakeAccess) Issue(id uuid.UUID) (string, time.Duration, error) {
	return "access:" + id.String(), 15 * time.Minute, nil
}

func (fakeAccess) Verify(token string) (uuid.UUID, error) {
	s, ok := strings.CutPrefix(token, "access:")
	if !ok {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	return id, nil
}

// fakeOpaque issues sequential tokens "tok-1", "tok-2", ... with hash "h:<token>".
type fakeOpaque struct{ n atomic.Int64 }

func (o *fakeOpaque) New() (string, []byte, error) {
	plain := fmt.Sprintf("tok-%d", o.n.Add(1))
	return plain, o.Hash(plain), nil
}

func (o *fakeOpaque) Hash(plain string) []byte { return []byte("h:" + plain) }

type captureMailer struct {
	mu   sync.Mutex
	sent []usecase.Message
}

func (c *captureMailer) Send(_ context.Context, m usecase.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func (c *captureMailer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *captureMailer) last(t *testing.T) usecase.Message {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		t.Fatal("no email was sent")
	}
	return c.sent[len(c.sent)-1]
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

const (
	refreshTTL = 30 * 24 * time.Hour
	reuseGrace = 20 * time.Second
	resetTTL   = 30 * time.Minute
)

// env is a fully wired use-case layer on fakes.
type env struct {
	auth     *usecase.Auth
	accounts *usecase.Accounts
	users    *memUsers
	tokens   *memTokens
	hasher   *fakeHasher
	mailer   *captureMailer
	clock    *fakeClock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		users:  &memUsers{byID: map[uuid.UUID]entity.User{}},
		tokens: &memTokens{refresh: map[string]*entity.RefreshToken{}, resets: map[string]*memReset{}},
		hasher: &fakeHasher{},
		mailer: &captureMailer{},
		clock:  &fakeClock{now: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)},
	}
	e.auth = usecase.NewAuth(usecase.AuthDeps{
		Users: e.users, Tokens: e.tokens, Tx: noTx{}, Hasher: e.hasher, Access: fakeAccess{},
		Opaque: &fakeOpaque{}, Mailer: e.mailer, Now: e.clock.Now,
	}, usecase.AuthConfig{RefreshTTL: refreshTTL, RefreshReuseGrace: reuseGrace, ResetTTL: resetTTL, AppBaseURL: "https://app.example/"})
	e.accounts = usecase.NewAccounts(e.users, e.hasher, e.clock.Now)
	return e
}

func validRegistration() usecase.RegisterInput {
	return usecase.RegisterInput{Email: "asha@example.com", Password: "correct horse", DisplayName: "Asha", TermsAccepted: true}
}

func mustRegister(t *testing.T, e *env) (entity.User, usecase.TokenPair) {
	t.Helper()
	user, pair, err := e.auth.Register(context.Background(), validRegistration())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return user, pair
}
