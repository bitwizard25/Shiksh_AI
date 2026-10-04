package usecase_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// memSessions is an in-memory SessionRepository with the same contract as the Postgres one.
type memSessions struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*entity.TutoringSession
}

func newMemSessions() *memSessions {
	return &memSessions{byID: map[uuid.UUID]*entity.TutoringSession{}}
}

func (m *memSessions) Create(_ context.Context, n usecase.NewSession) (entity.TutoringSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &entity.TutoringSession{ID: uuid.New(), UserID: n.UserID, Subject: n.Subject, Language: n.Language,
		Grade: n.Grade, Status: entity.SessionCreated, CreatedAt: n.Now}
	m.byID[s.ID] = s
	return *s, nil
}

func (m *memSessions) Get(_ context.Context, id uuid.UUID) (entity.TutoringSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return entity.TutoringSession{}, entity.ErrNotFound
	}
	return *s, nil
}

func (m *memSessions) List(_ context.Context, userID uuid.UUID, limit int, after *usecase.SessionCursor) ([]entity.TutoringSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []entity.TutoringSession
	for _, s := range m.byID {
		if s.UserID == userID {
			all = append(all, *s)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	var out []entity.TutoringSession
	for _, s := range all {
		if after != nil && !s.CreatedAt.Before(after.CreatedAt) {
			continue
		}
		out = append(out, s)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memSessions) End(_ context.Context, id uuid.UUID, reason string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if !ok || s.Ended() {
		return false, nil
	}
	s.Status, s.EndReason, s.EndedAt = entity.SessionEnded, reason, &now
	return true, nil
}

func (m *memSessions) ClaimStale(_ context.Context, cutoff time.Time, limit int, now time.Time) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []uuid.UUID
	for _, s := range m.byID {
		last := s.CreatedAt
		if s.LastActiveAt != nil {
			last = *s.LastActiveAt
		}
		if !s.Ended() && last.Before(cutoff) && len(ids) < limit {
			s.Status, s.EndReason, s.EndedAt = entity.SessionEnded, entity.EndSwept, &now
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

func (m *memSessions) SetSummary(_ context.Context, id uuid.UUID, summary string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return entity.ErrNotFound
	}
	s.Summary = summary
	return nil
}

func (m *memSessions) RecentSummaries(_ context.Context, userID uuid.UUID, subject string, n int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matching []*entity.TutoringSession
	for _, s := range m.byID {
		if s.UserID == userID && s.Subject == subject && s.Summary != "" {
			matching = append(matching, s)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].CreatedAt.After(matching[j].CreatedAt) })
	var out []string
	for _, s := range matching {
		if len(out) < n {
			out = append(out, s.Summary)
		}
	}
	return out, nil
}

// set changes a stored session directly, for test setup.
func (m *memSessions) set(id uuid.UUID, fn func(*entity.TutoringSession)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.byID[id])
}

type memMessages struct {
	mu        sync.Mutex
	bySession map[uuid.UUID][]entity.Message
}

func (m *memMessages) add(id uuid.UUID, msgs ...entity.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bySession == nil {
		m.bySession = map[uuid.UUID][]entity.Message{}
	}
	m.bySession[id] = append(m.bySession[id], msgs...)
}

func (m *memMessages) List(_ context.Context, id uuid.UUID) ([]entity.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]entity.Message(nil), m.bySession[id]...), nil
}

func (m *memMessages) Recent(ctx context.Context, id uuid.UUID, n int) ([]entity.Message, error) {
	all, _ := m.List(ctx, id)
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

type memTickets struct {
	mu      sync.Mutex
	tickets map[string]*memTicket
}

type memTicket struct {
	userID, sessionID uuid.UUID
	expiresAt         time.Time
	used              bool
}

func (m *memTickets) Create(_ context.Context, userID, sessionID uuid.UUID, hash []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickets[string(hash)] = &memTicket{userID: userID, sessionID: sessionID, expiresAt: expiresAt}
	return nil
}

func (m *memTickets) Redeem(_ context.Context, hash []byte, now time.Time) (uuid.UUID, uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[string(hash)]
	if !ok || t.used || !t.expiresAt.After(now) {
		return uuid.Nil, uuid.Nil, entity.ErrTokenInvalid
	}
	t.used = true
	return t.userID, t.sessionID, nil
}

func (m *memTickets) PurgeExpired(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, t := range m.tickets {
		if t.expiresAt.Before(now) {
			delete(m.tickets, k)
			n++
		}
	}
	return n, nil
}

// fakeGen records its calls and returns reply (or err).
type fakeGen struct {
	mu     sync.Mutex
	reply  string
	err    error
	system string
	user   string
	calls  int
}

func (g *fakeGen) Complete(_ context.Context, system, user string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.system, g.user = system, user
	return g.reply, g.err
}

func (g *fakeGen) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

var errGen = errors.New("llm down")

const ticketTTL = 60 * time.Second

// sessionEnv is the session use cases wired on fakes, on top of the account env.
type sessionEnv struct {
	*env
	sessions  *memSessions
	messages  *memMessages
	tickets   *memTickets
	gen       *fakeGen
	summaries *usecase.Summaries
	uc        *usecase.Sessions
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	e := newEnv(t)
	s := &sessionEnv{
		env:      e,
		sessions: newMemSessions(),
		messages: &memMessages{},
		tickets:  &memTickets{tickets: map[string]*memTicket{}},
		gen:      &fakeGen{reply: "  Covered fractions. Needs practice.  "},
	}
	s.summaries = usecase.NewSummaries(s.sessions, s.messages, s.gen, nil)
	s.uc = usecase.NewSessions(usecase.SessionsDeps{
		Users: e.users, Sessions: s.sessions, Messages: s.messages, Tickets: s.tickets,
		Tx: noTx{}, Opaque: &fakeOpaque{}, Summaries: s.summaries,
	}, usecase.SessionsConfig{TicketTTL: ticketTTL}, e.clock.Now)
	return s
}
