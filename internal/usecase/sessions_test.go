package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestCreateSessionUsesProfileDefaults(t *testing.T) {
	e := newSessionEnv(t)
	grade := 6
	user, _, err := e.auth.Register(context.Background(), usecase.RegisterInput{
		Email: "a@example.com", Password: "correct horse", DisplayName: "Asha", TermsAccepted: true,
		PreferredLang: "ta", Grade: &grade,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.uc.Create(context.Background(), user.ID, usecase.SessionInput{Subject: "math"})
	if err != nil {
		t.Fatal(err)
	}
	s := got.Session
	if s.Language != "ta" || *s.Grade != 6 || s.Subject != "math" || s.UserID != user.ID || s.Status != entity.SessionCreated {
		t.Fatalf("session = %+v", s)
	}
	if got.Ticket.Token == "" || !got.Ticket.ExpiresAt.Equal(e.clock.Now().Add(ticketTTL)) {
		t.Fatalf("ticket = %+v", got.Ticket)
	}
	uid, sid, err := e.uc.RedeemTicket(context.Background(), got.Ticket.Token)
	if err != nil || uid != user.ID || sid != s.ID {
		t.Fatalf("RedeemTicket = %v %v %v", uid, sid, err)
	}
	if _, _, err := e.uc.RedeemTicket(context.Background(), got.Ticket.Token); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("ticket reuse err = %v", err)
	}
}

func TestCreateSessionOverridesAndValidation(t *testing.T) {
	e := newSessionEnv(t)
	user, _ := mustRegister(t, e.env)
	ctx := context.Background()
	bad13, ok9 := 13, 9

	got, err := e.uc.Create(ctx, user.ID, usecase.SessionInput{Subject: "physics", Language: "en", Grade: &ok9})
	if err != nil || got.Session.Language != "en" || *got.Session.Grade != 9 {
		t.Fatalf("override = %+v, %v", got.Session, err)
	}
	for name, in := range map[string]usecase.SessionInput{
		"unknown subject":  {Subject: "astrology"},
		"empty subject":    {},
		"unknown language": {Subject: "math", Language: "xx"},
		"bad grade":        {Subject: "math", Grade: &bad13},
	} {
		_, err := e.uc.Create(ctx, user.ID, in)
		var ve *entity.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
}

func TestNewTicketAndEndedSessions(t *testing.T) {
	e := newSessionEnv(t)
	user, _ := mustRegister(t, e.env)
	ctx := context.Background()
	started, _ := e.uc.Create(ctx, user.ID, usecase.SessionInput{Subject: "math"})
	id := started.Session.ID

	tk, err := e.uc.NewTicket(ctx, user.ID, id)
	if err != nil || tk.Token == "" || tk.Token == started.Ticket.Token {
		t.Fatalf("NewTicket = %+v, %v", tk, err)
	}
	if _, err := e.uc.NewTicket(ctx, uuid.New(), id); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("other user's ticket err = %v", err)
	}
	if err := e.uc.End(ctx, user.ID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.uc.NewTicket(ctx, user.ID, id); !errors.Is(err, entity.ErrSessionEnded) {
		t.Fatalf("ended session ticket err = %v", err)
	}
	if err := e.uc.End(ctx, user.ID, id); err != nil {
		t.Fatalf("second End err = %v, want nil", err)
	}
}

func TestOtherUsersSessionLooksMissing(t *testing.T) {
	e := newSessionEnv(t)
	user, _ := mustRegister(t, e.env)
	started, _ := e.uc.Create(context.Background(), user.ID, usecase.SessionInput{Subject: "math"})
	stranger := uuid.New()
	if _, err := e.uc.Get(context.Background(), stranger, started.Session.ID); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("Get err = %v", err)
	}
	if err := e.uc.End(context.Background(), stranger, started.Session.ID); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("End err = %v", err)
	}
	if s, _ := e.sessions.Get(context.Background(), started.Session.ID); s.Ended() {
		t.Fatal("a stranger ended the session")
	}
}

func TestEndSummarizesOnlySessionsWithEnoughTurns(t *testing.T) {
	e := newSessionEnv(t)
	user, _ := mustRegister(t, e.env)
	ctx := context.Background()
	short, _ := e.uc.Create(ctx, user.ID, usecase.SessionInput{Subject: "math"})
	long, _ := e.uc.Create(ctx, user.ID, usecase.SessionInput{Subject: "math"})
	e.sessions.set(short.Session.ID, func(s *entity.TutoringSession) { s.TurnCount = 1 })
	e.sessions.set(long.Session.ID, func(s *entity.TutoringSession) { s.TurnCount = 2 })
	e.messages.add(long.Session.ID,
		entity.Message{Role: entity.RoleLearner, Content: "what is a fraction", Status: entity.MessageComplete},
		entity.Message{Role: entity.RoleTutor, Content: "a part of a whole", Status: entity.MessageComplete})

	if err := e.uc.End(ctx, user.ID, short.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.uc.End(ctx, user.ID, long.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.summaries.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if e.gen.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want 1", e.gen.callCount())
	}
	got, _ := e.sessions.Get(ctx, long.Session.ID)
	if got.Summary != "Covered fractions. Needs practice." {
		t.Fatalf("summary = %q", got.Summary)
	}
	if s, _ := e.sessions.Get(ctx, short.Session.ID); s.Summary != "" {
		t.Fatalf("short session summary = %q", s.Summary)
	}
}

func TestListPagination(t *testing.T) {
	e := newSessionEnv(t)
	user, _ := mustRegister(t, e.env)
	ctx := context.Background()
	for range 5 {
		if _, err := e.uc.Create(ctx, user.ID, usecase.SessionInput{Subject: "math"}); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
	}
	p1, err := e.uc.List(ctx, user.ID, 2, nil)
	if err != nil || len(p1.Items) != 2 || p1.Next == nil {
		t.Fatalf("page1 = %d items, next=%v, %v", len(p1.Items), p1.Next, err)
	}
	p2, _ := e.uc.List(ctx, user.ID, 2, p1.Next)
	p3, _ := e.uc.List(ctx, user.ID, 2, p2.Next)
	if len(p2.Items) != 2 || p2.Next == nil || len(p3.Items) != 1 || p3.Next != nil {
		t.Fatalf("pages = %d/%v, %d/%v", len(p2.Items), p2.Next, len(p3.Items), p3.Next)
	}
	if !p1.Items[0].CreatedAt.After(p1.Items[1].CreatedAt) {
		t.Fatal("not newest first")
	}
	huge, _ := e.uc.List(ctx, user.ID, 100000, nil)
	if len(huge.Items) != 5 {
		t.Fatalf("huge limit returned %d", len(huge.Items))
	}
}
