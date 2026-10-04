package repository_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func newSession(t *testing.T, sessions *repository.Sessions, userID uuid.UUID, subject string, at time.Time) entity.TutoringSession {
	t.Helper()
	s, err := sessions.Create(t.Context(), usecase.NewSession{UserID: userID, Subject: subject, Language: "hi", Now: at})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s
}

func addTurn(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, turn int, learner, tutor, tutorStatus string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `
		INSERT INTO messages (session_id, turn_no, role, input_mode, content) VALUES ($1, $2, 'learner', 'voice', $3)`, id, turn, learner)
	if err == nil {
		_, err = pool.Exec(t.Context(), `
			INSERT INTO messages (session_id, turn_no, role, content, status) VALUES ($1, $2, 'tutor', $3, $4)`, id, turn, tutor, tutorStatus)
	}
	if err == nil {
		_, err = pool.Exec(t.Context(), `UPDATE tutoring_sessions SET turn_count = turn_count + 1 WHERE id = $1`, id)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionCreateAndGet(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions := repository.NewSessions(pool)
	u := mustUser(t, repository.NewUsers(pool))
	grade := 7
	s, err := sessions.Create(t.Context(), usecase.NewSession{UserID: u.ID, Subject: "math", Language: "ta", Grade: &grade, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != entity.SessionCreated || s.Subject != "math" || s.Language != "ta" || *s.Grade != 7 || !s.CreatedAt.Equal(t0) || s.Summary != "" {
		t.Fatalf("session = %+v", s)
	}
	got, err := sessions.Get(t.Context(), s.ID)
	if err != nil || got.ID != s.ID {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := sessions.Get(t.Context(), uuid.New()); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("Get unknown err = %v", err)
	}
}

func TestSessionListKeysetPagination(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions := repository.NewSessions(pool)
	users := repository.NewUsers(pool)
	u, other := mustUser(t, users), mustUser(t, users)
	var want []uuid.UUID
	for i := range 5 {
		want = append(want, newSession(t, sessions, u.ID, "math", t0.Add(time.Duration(i)*time.Minute)).ID)
	}
	slices.Reverse(want) // newest first
	newSession(t, sessions, other.ID, "math", t0)

	page1, err := sessions.List(t.Context(), u.ID, 2, nil)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1 = %d, %v", len(page1), err)
	}
	cur := &usecase.SessionCursor{CreatedAt: page1[1].CreatedAt, ID: page1[1].ID}
	page2, err := sessions.List(t.Context(), u.ID, 10, cur)
	if err != nil {
		t.Fatal(err)
	}
	var got []uuid.UUID
	for _, s := range append(page1, page2...) {
		got = append(got, s.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestSessionEnd(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions := repository.NewSessions(pool)
	s := newSession(t, sessions, mustUser(t, repository.NewUsers(pool)).ID, "math", t0)
	ended, err := sessions.End(t.Context(), s.ID, entity.EndLearner, t0.Add(time.Minute))
	if err != nil || !ended {
		t.Fatalf("End = %v, %v", ended, err)
	}
	if again, _ := sessions.End(t.Context(), s.ID, entity.EndIdle, t0.Add(time.Hour)); again {
		t.Fatal("second End reported it ended the session")
	}
	got, _ := sessions.Get(t.Context(), s.ID)
	if !got.Ended() || got.EndReason != entity.EndLearner || !got.EndedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("session = %+v", got)
	}
}

func TestClaimStaleEndsOnlyIdleSessionsOnce(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions := repository.NewSessions(pool)
	u := mustUser(t, repository.NewUsers(pool))
	stale := newSession(t, sessions, u.ID, "math", t0) // created, never connected
	fresh := newSession(t, sessions, u.ID, "math", t0.Add(9*time.Minute))
	active := newSession(t, sessions, u.ID, "math", t0) // old, but touched recently
	if _, err := pool.Exec(t.Context(), `UPDATE tutoring_sessions SET status='active', last_active_at=$2 WHERE id=$1`, active.ID, t0.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	done := newSession(t, sessions, u.ID, "math", t0)
	if _, err := sessions.End(t.Context(), done.ID, entity.EndLearner, t0); err != nil {
		t.Fatal(err)
	}

	now := t0.Add(10 * time.Minute)
	ids, err := sessions.ClaimStale(t.Context(), now.Add(-5*time.Minute), 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != stale.ID {
		t.Fatalf("claimed %v, want only %v", ids, stale.ID)
	}
	got, _ := sessions.Get(t.Context(), stale.ID)
	if !got.Ended() || got.EndReason != entity.EndSwept {
		t.Fatalf("stale session = %+v", got)
	}
	for _, id := range []uuid.UUID{fresh.ID, active.ID} {
		if s, _ := sessions.Get(t.Context(), id); s.Ended() {
			t.Fatalf("session %v was ended", id)
		}
	}
	if again, _ := sessions.ClaimStale(t.Context(), now.Add(-5*time.Minute), 10, now); len(again) != 0 {
		t.Fatalf("second claim = %v", again)
	}
}

func TestClaimStaleConcurrentCallersNeverShareASession(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions := repository.NewSessions(pool)
	u := mustUser(t, repository.NewUsers(pool))
	const total = 20
	for range total {
		newSession(t, sessions, u.ID, "math", t0)
	}
	now := t0.Add(time.Hour)
	var (
		mu   sync.Mutex
		seen = map[uuid.UUID]int{}
		wg   sync.WaitGroup
	)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := sessions.ClaimStale(context.Background(), now.Add(-time.Minute), 7, now)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			for _, id := range ids {
				seen[id]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("session %v claimed %d times", id, n)
		}
	}
	if len(seen) != 20 {
		t.Fatalf("claimed %d sessions, want 20", len(seen))
	}
}

func TestSummaries(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions := repository.NewSessions(pool)
	u := mustUser(t, repository.NewUsers(pool))
	for i, sub := range []string{"math", "math", "science", "math"} {
		s := newSession(t, sessions, u.ID, sub, t0.Add(time.Duration(i)*time.Hour))
		if i < 3 { // the last math session has no summary yet
			if err := sessions.SetSummary(t.Context(), s.ID, sub+" summary "+string(rune('A'+i))); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := sessions.End(t.Context(), s.ID, entity.EndLearner, t0.Add(time.Duration(i)*time.Hour+time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := sessions.RecentSummaries(t.Context(), u.ID, "math", 3)
	if err != nil || !slices.Equal(got, []string{"math summary B", "math summary A"}) {
		t.Fatalf("summaries = %v, %v", got, err)
	}
	if err := sessions.SetSummary(t.Context(), uuid.New(), "x"); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("SetSummary unknown err = %v", err)
	}
}

func TestMessagesRecentAndList(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions, messages := repository.NewSessions(pool), repository.NewMessages(pool)
	s := newSession(t, sessions, mustUser(t, repository.NewUsers(pool)).ID, "math", t0)
	addTurn(t, pool, s.ID, 1, "q1", "a1", "complete")
	addTurn(t, pool, s.ID, 2, "q2", "a2", "interrupted")
	addTurn(t, pool, s.ID, 3, "q3", "a3", "failed")

	all, err := messages.List(t.Context(), s.ID)
	if err != nil || len(all) != 6 {
		t.Fatalf("List = %d, %v", len(all), err)
	}
	contents := func(ms []entity.Message) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Content)
		}
		return out
	}
	if want := []string{"q1", "a1", "q2", "a2", "q3", "a3"}; !slices.Equal(contents(all), want) {
		t.Fatalf("order = %v", contents(all))
	}
	if all[0].InputMode != entity.InputVoice || all[1].Status != entity.MessageComplete || all[3].Status != entity.MessageInterrupted {
		t.Fatalf("fields = %+v", all)
	}
	recent, err := messages.Recent(t.Context(), s.ID, 3)
	if err != nil || !slices.Equal(contents(recent), []string{"a2", "q3", "a3"}) {
		t.Fatalf("Recent = %v, %v", contents(recent), err)
	}
	if other, _ := messages.List(t.Context(), uuid.New()); len(other) != 0 {
		t.Fatalf("unknown session has messages: %v", other)
	}
}

func TestTicketRedeem(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions, tickets := repository.NewSessions(pool), repository.NewTickets(pool)
	u := mustUser(t, repository.NewUsers(pool))
	s := newSession(t, sessions, u.ID, "math", t0)
	if err := tickets.Create(t.Context(), u.ID, s.ID, hashOf("T"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tickets.Redeem(t.Context(), hashOf("T"), t0.Add(2*time.Minute)); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("expired ticket err = %v", err)
	}
	uid, sid, err := tickets.Redeem(t.Context(), hashOf("T"), t0)
	if err != nil || uid != u.ID || sid != s.ID {
		t.Fatalf("Redeem = %v %v %v", uid, sid, err)
	}
	if _, _, err := tickets.Redeem(t.Context(), hashOf("T"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("second redeem err = %v", err)
	}
	if _, _, err := tickets.Redeem(t.Context(), hashOf("unknown"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("unknown ticket err = %v", err)
	}
}

func TestTicketForEndedSessionIsRejected(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions, tickets := repository.NewSessions(pool), repository.NewTickets(pool)
	u := mustUser(t, repository.NewUsers(pool))
	s := newSession(t, sessions, u.ID, "math", t0)
	if err := tickets.Create(t.Context(), u.ID, s.ID, hashOf("T"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.End(t.Context(), s.ID, entity.EndLearner, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tickets.Redeem(t.Context(), hashOf("T"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestTicketConcurrentRedeemHasOneWinner(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions, tickets := repository.NewSessions(pool), repository.NewTickets(pool)
	u := mustUser(t, repository.NewUsers(pool))
	s := newSession(t, sessions, u.ID, "math", t0)
	if err := tickets.Create(t.Context(), u.ID, s.ID, hashOf("T"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := tickets.Redeem(context.Background(), hashOf("T"), t0); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d winners, want 1", wins)
	}
}

func TestPurgeExpired(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, sessions, tickets, tokens := repository.NewUsers(pool), repository.NewSessions(pool), repository.NewTickets(pool), repository.NewTokens(pool)
	u := mustUser(t, users)
	s := newSession(t, sessions, u.ID, "math", t0)
	ctx := t.Context()
	now := t0.Add(time.Hour)
	mustNil := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustNil(tickets.Create(ctx, u.ID, s.ID, hashOf("old"), now.Add(-time.Second)))
	mustNil(tickets.Create(ctx, u.ID, s.ID, hashOf("new"), now.Add(time.Minute)))
	mustNil(tokens.CreateRefresh(ctx, u.ID, uuid.New(), hashOf("r-old"), now.Add(-time.Second)))
	mustNil(tokens.CreateRefresh(ctx, u.ID, uuid.New(), hashOf("r-new"), now.Add(time.Minute)))
	mustNil(tokens.CreatePasswordReset(ctx, u.ID, hashOf("p-old"), now.Add(-time.Second)))
	mustNil(tokens.CreatePasswordReset(ctx, u.ID, hashOf("p-new"), now.Add(time.Minute)))

	if n, err := tickets.PurgeExpired(ctx, now); err != nil || n != 1 {
		t.Fatalf("tickets purged = %d, %v", n, err)
	}
	if n, err := tokens.PurgeExpired(ctx, now); err != nil || n != 2 {
		t.Fatalf("tokens purged = %d, %v", n, err)
	}
	if _, err := tokens.FindRefresh(ctx, hashOf("r-new")); err != nil {
		t.Fatalf("live refresh token was purged: %v", err)
	}
	if _, err := tokens.FindRefresh(ctx, hashOf("r-old")); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("expired refresh token survived: %v", err)
	}
}
