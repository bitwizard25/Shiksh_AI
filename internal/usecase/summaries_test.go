package usecase_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func summarizable(t *testing.T, e *sessionEnv) entity.TutoringSession {
	t.Helper()
	user, _ := mustRegister(t, e.env)
	started, err := e.uc.Create(context.Background(), user.ID, usecase.SessionInput{Subject: "math"})
	if err != nil {
		t.Fatal(err)
	}
	id := started.Session.ID
	e.sessions.set(id, func(s *entity.TutoringSession) { s.TurnCount = 2 })
	e.messages.add(id,
		entity.Message{Role: entity.RoleLearner, Content: "what is a fraction", Status: entity.MessageComplete},
		entity.Message{Role: entity.RoleTutor, Content: "tutor failed line", Status: entity.MessageFailed},
		entity.Message{Role: entity.RoleTutor, Content: "a part of a whole", Status: entity.MessageComplete})
	s, _ := e.sessions.Get(context.Background(), id)
	return s
}

func TestSummarizeBuildsTranscriptFromHeardMessages(t *testing.T) {
	e := newSessionEnv(t)
	s := summarizable(t, e)
	if err := e.summaries.Summarize(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if want := "Learner: what is a fraction\nTutor: a part of a whole\n"; e.gen.user != want {
		t.Fatalf("transcript = %q, want %q", e.gen.user, want)
	}
	if !strings.Contains(e.gen.system, "80 words") {
		t.Fatalf("system prompt = %q", e.gen.system)
	}
}

func TestSummarizeSkipsShortOrAlreadySummarized(t *testing.T) {
	e := newSessionEnv(t)
	s := summarizable(t, e)
	ctx := context.Background()
	e.sessions.set(s.ID, func(s *entity.TutoringSession) { s.TurnCount = 1 })
	_ = e.summaries.Summarize(ctx, s.ID)
	e.sessions.set(s.ID, func(s *entity.TutoringSession) { s.TurnCount = 2; s.Summary = "already" })
	_ = e.summaries.Summarize(ctx, s.ID)
	if e.gen.callCount() != 0 {
		t.Fatalf("LLM called %d times", e.gen.callCount())
	}
}

func TestSummarizeFailureLeavesSessionUnsummarized(t *testing.T) {
	e := newSessionEnv(t)
	s := summarizable(t, e)
	e.gen.err = errGen
	if err := e.summaries.Summarize(context.Background(), s.ID); err == nil {
		t.Fatal("want error")
	}
	e.gen.err, e.gen.reply = nil, "   "
	if err := e.summaries.Summarize(context.Background(), s.ID); err == nil {
		t.Fatal("blank summary accepted")
	}
	if got, _ := e.sessions.Get(context.Background(), s.ID); got.Summary != "" {
		t.Fatalf("summary = %q", got.Summary)
	}
}

func TestSummaryIsCapped(t *testing.T) {
	e := newSessionEnv(t)
	s := summarizable(t, e)
	e.gen.reply = strings.Repeat("हिन्दी ", 500)
	if err := e.summaries.Summarize(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.sessions.Get(context.Background(), s.ID)
	if n := len([]rune(got.Summary)); n > 800 {
		t.Fatalf("summary has %d runes", n)
	}
}

func TestSummariesDisabledWithoutGenerator(t *testing.T) {
	e := newSessionEnv(t)
	off := usecase.NewSummaries(e.sessions, e.messages, nil, nil)
	off.Submit(summarizable(t, e).ID)
	if err := off.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSweeperEndsStaleSessionsAndPurges(t *testing.T) {
	e := newSessionEnv(t)
	s := summarizable(t, e)
	ctx := context.Background()
	if err := e.tickets.Create(ctx, s.UserID, s.ID, []byte("old"), e.clock.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.tokens.CreateRefresh(ctx, s.UserID, s.ID, []byte("r"), e.clock.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	sw := usecase.NewSweeper(usecase.SweeperDeps{Sessions: e.sessions, Tickets: e.tickets, Tokens: e.tokens, Summaries: e.summaries},
		5*time.Minute, e.clock.Now)

	res, err := sw.Sweep(ctx)
	if err != nil || res.SessionsEnded != 0 || res.TicketsPurged != 1 || res.TokensPurged != 1 {
		t.Fatalf("fresh sweep = %+v, %v", res, err)
	}
	e.clock.Advance(6 * time.Minute)
	res, err = sw.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsEnded != 1 {
		t.Fatalf("sweep = %+v", res)
	}
	if err := e.summaries.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.sessions.Get(ctx, s.ID)
	if got.EndReason != entity.EndSwept || got.Summary == "" {
		t.Fatalf("session = %+v", got)
	}
}
