package repository_test

import (
	"errors"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func turnOf(no int, learner, tutor string) usecase.TurnInsert {
	return usecase.TurnInsert{
		TurnNo:  no,
		Learner: entity.Message{InputMode: entity.InputVoice, Content: learner, Latency: []byte(`{"asr_ms":120}`)},
		Tutor:   entity.Message{Content: tutor, Latency: []byte(`{"first_audio_ms":900}`)},
	}
}

func TestConnectBumpsEpochAndActivates(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions, turns := repository.NewSessions(pool), repository.NewTurns(pool)
	s := newSession(t, sessions, mustUser(t, repository.NewUsers(pool)).ID, "math", t0)

	e1, err := turns.Connect(t.Context(), s.ID, t0.Add(time.Minute))
	if err != nil || e1 != 1 {
		t.Fatalf("first Connect = %d, %v", e1, err)
	}
	e2, _ := turns.Connect(t.Context(), s.ID, t0.Add(2*time.Minute))
	if e2 != 2 {
		t.Fatalf("second epoch = %d", e2)
	}
	got, _ := sessions.Get(t.Context(), s.ID)
	if got.Status != entity.SessionActive || got.ConnEpoch != 2 || !got.StartedAt.Equal(t0.Add(time.Minute)) || !got.LastActiveAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("session = %+v", got)
	}
	if _, err := sessions.End(t.Context(), s.ID, entity.EndLearner, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := turns.Connect(t.Context(), s.ID, t0); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("Connect on ended session err = %v", err)
	}
}

func TestInsertTurnIsFencedByEpoch(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions, turns, messages := repository.NewSessions(pool), repository.NewTurns(pool), repository.NewMessages(pool)
	s := newSession(t, sessions, mustUser(t, repository.NewUsers(pool)).ID, "math", t0)
	old, _ := turns.Connect(t.Context(), s.ID, t0)
	cur, _ := turns.Connect(t.Context(), s.ID, t0)

	if err := turns.InsertTurn(t.Context(), s.ID, old, turnOf(1, "stale q", "stale a"), t0); !errors.Is(err, entity.ErrFenced) {
		t.Fatalf("stale epoch err = %v, want ErrFenced", err)
	}
	if all, _ := messages.List(t.Context(), s.ID); len(all) != 0 {
		t.Fatalf("a fenced write stored %d messages", len(all))
	}
	if err := turns.InsertTurn(t.Context(), s.ID, cur, turnOf(1, "q1", "a1"), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	all, _ := messages.List(t.Context(), s.ID)
	if len(all) != 2 || all[0].Content != "q1" || all[0].InputMode != entity.InputVoice || all[1].Content != "a1" || string(all[1].Latency) != `{"first_audio_ms": 900}` {
		t.Fatalf("messages = %+v", all)
	}
	got, _ := sessions.Get(t.Context(), s.ID)
	if got.TurnCount != 1 || !got.LastActiveAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("session = %+v", got)
	}
	if err := turns.InsertTurn(t.Context(), s.ID, cur, turnOf(1, "dup", "dup"), t0); err == nil || errors.Is(err, entity.ErrFenced) {
		t.Fatalf("duplicate turn_no err = %v, want a unique violation", err)
	}
	if n, err := turns.MaxTurnNo(t.Context(), s.ID); err != nil || n != 1 {
		t.Fatalf("MaxTurnNo = %d, %v", n, err)
	}
}

func TestMaxTurnNoOfEmptySessionIsZero(t *testing.T) {
	pool := dbtest.NewPool(t)
	s := newSession(t, repository.NewSessions(pool), mustUser(t, repository.NewUsers(pool)).ID, "math", t0)
	if n, err := repository.NewTurns(pool).MaxTurnNo(t.Context(), s.ID); err != nil || n != 0 {
		t.Fatalf("MaxTurnNo = %d, %v", n, err)
	}
}

func TestUpdateTutorAndTouchAreFenced(t *testing.T) {
	pool := dbtest.NewPool(t)
	sessions, turns, messages := repository.NewSessions(pool), repository.NewTurns(pool), repository.NewMessages(pool)
	s := newSession(t, sessions, mustUser(t, repository.NewUsers(pool)).ID, "math", t0)
	old, _ := turns.Connect(t.Context(), s.ID, t0)
	if err := turns.InsertTurn(t.Context(), s.ID, old, turnOf(1, "q", "full answer"), t0); err != nil {
		t.Fatal(err)
	}
	if err := turns.UpdateTutor(t.Context(), s.ID, old, 1, "full", entity.MessageInterrupted); err != nil {
		t.Fatal(err)
	}
	all, _ := messages.List(t.Context(), s.ID)
	if all[1].Content != "full" || all[1].Status != entity.MessageInterrupted {
		t.Fatalf("tutor message = %+v", all[1])
	}
	if err := turns.UpdateTutor(t.Context(), s.ID, old, 9, "x", entity.MessageComplete); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("missing turn err = %v", err)
	}
	if err := turns.Touch(t.Context(), s.ID, old, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	cur, _ := turns.Connect(t.Context(), s.ID, t0.Add(2*time.Hour))
	if err := turns.UpdateTutor(t.Context(), s.ID, old, 1, "late", entity.MessageComplete); !errors.Is(err, entity.ErrFenced) {
		t.Fatalf("stale UpdateTutor err = %v", err)
	}
	if err := turns.Touch(t.Context(), s.ID, old, t0.Add(3*time.Hour)); !errors.Is(err, entity.ErrFenced) {
		t.Fatalf("stale Touch err = %v", err)
	}
	if err := turns.Touch(t.Context(), s.ID, cur, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
}
