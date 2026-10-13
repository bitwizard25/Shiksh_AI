package conversation

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

func TestIdleTimeoutClosesButKeepsTheSessionResumable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.IdleTimeout = time.Minute })
		h.start(t.Context())
		time.Sleep(50 * time.Second)
		h.s.Message(ClientMsg{Type: MsgPing}) // activity resets the timer
		time.Sleep(50 * time.Second)
		select {
		case info := <-h.res:
			t.Fatalf("closed early: %+v", info)
		default:
		}
		info := h.close(time.Minute)
		if info.Code != CloseIdleTimeout {
			t.Fatalf("close = %+v", info)
		}
		if got := h.out.names(); got[len(got)-1] != "ended:idle" {
			t.Fatalf("trace = %v", got)
		}
		if len(h.db.ends()) != 0 {
			t.Fatalf("an idle session was ended in the database: %v", h.db.ends())
		}
	})
}

func TestMaxDurationEndsTheSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.MaxSessionDuration = time.Minute; c.IdleTimeout = time.Hour })
		h.start(t.Context())
		info := h.close(2 * time.Minute)
		if info.Code != CloseNormal || !slices.Equal(h.db.ends(), []string{entity.EndMaxDuration}) || len(h.ended) != 1 {
			t.Fatalf("close = %+v, ends = %v", info, h.db.ends())
		}
		if got := h.out.names(); got[len(got)-1] != "ended:max_duration" {
			t.Fatalf("trace = %v", got)
		}
	})
}

func TestEndedByLearnerMidTurnSavesWhatWasWritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = slowReply
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(1800 * time.Millisecond) // segments 0 and 1 are out
		info := h.endSession()
		if info.Code != CloseNormal {
			t.Fatalf("close = %+v", info)
		}
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 || inserts[0].Tutor.Content != "First sentence is here. Second sentence follows." || inserts[0].Tutor.Status != entity.MessageInterrupted {
			t.Fatalf("inserts = %+v", inserts)
		}
		if !slices.Equal(h.db.ends(), []string{entity.EndLearner}) {
			t.Fatalf("ends = %v", h.db.ends())
		}
	})
}

func TestRESTEndStopsALiveSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.s.Stop(StopEndRequest)
		info := h.close(10 * time.Second)
		if info.Code != CloseNormal || !slices.Equal(h.db.ends(), []string{entity.EndLearner}) {
			t.Fatalf("close = %+v, ends = %v", info, h.db.ends())
		}
		if got := h.out.names(); got[len(got)-1] != "ended:learner" {
			t.Fatalf("trace = %v", got)
		}
	})
}

func TestConnectionClosedDetachesWithoutEnding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = slowReply
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(1100 * time.Millisecond)
		h.s.Closed(errBoom)
		h.close(10 * time.Second)
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 || inserts[0].Tutor.Content != "First sentence is here." {
			t.Fatalf("inserts = %+v", inserts)
		}
		if len(h.db.ends()) != 0 {
			t.Fatalf("a dropped connection ended the session: %v", h.db.ends())
		}
	})
}

func TestShutdownTellsClientToReconnectAndSavesTheTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = slowReply
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(1100 * time.Millisecond)
		h.s.Stop(StopShutdown)
		info := h.close(10 * time.Second)
		if info.Code != CloseGoingAway {
			t.Fatalf("close = %+v", info)
		}
		errs := h.out.find(func(e Event) bool { x, ok := e.(ErrorEvent); return ok && x.Code == ErrServerShutdown && x.Retryable })
		if len(errs) != 1 {
			t.Fatalf("trace = %v", h.out.names())
		}
		if inserts, _ := h.store.snapshot(); len(inserts) != 1 || inserts[0].Tutor.Status != entity.MessageInterrupted {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestCancelledContextIsAShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ctx, cancel := context.WithCancel(t.Context())
		h.start(ctx)
		synctest.Wait()
		cancel()
		if info := h.close(10 * time.Second); info.Code != CloseGoingAway {
			t.Fatalf("close = %+v", info)
		}
	})
}

func TestReplacedSessionClosesWith4009(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.s.Stop(StopReplaced)
		info := h.close(10 * time.Second)
		if info.Code != CloseReplaced || !contains(h.out.names(), "error:session_replaced") {
			t.Fatalf("close = %+v, trace %v", info, h.out.names())
		}
	})
}

func TestFencedWriteClosesTheStaleConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		_, _ = h.store.Connect(t.Context(), uuid.Nil, time.Time{}) // another instance connected: the epoch moved on
		h.text("hello")
		info := h.close(30 * time.Second)
		if info.Code != CloseReplaced || !contains(h.out.names(), "error:session_replaced") {
			t.Fatalf("close = %+v, trace %v", info, h.out.names())
		}
		if inserts, _ := h.store.snapshot(); len(inserts) != 0 {
			t.Fatalf("a fenced connection wrote: %+v", inserts)
		}
		if len(h.db.ends()) != 0 {
			t.Fatal("a fenced connection ended the session")
		}
	})
}

func TestStorageFailureEndsConnectionButNotSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.store.failInert = errBoom
		h.start(t.Context())
		h.text("hello")
		info := h.close(30 * time.Second)
		if info.Code != CloseInternal || !contains(h.out.names(), "error:storage_unavailable") {
			t.Fatalf("close = %+v, trace %v", info, h.out.names())
		}
		if len(h.db.ends()) != 0 {
			t.Fatal("a storage failure ended the session")
		}
	})
}

func TestSlowConsumerIsClosedWithPolicyCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		synctest.Wait()
		h.out.mu.Lock()
		h.out.full = true
		h.out.mu.Unlock()
		h.s.Message(ClientMsg{Type: MsgPing})
		if info := h.close(10 * time.Second); info.Code != ClosePolicy {
			t.Fatalf("close = %+v", info)
		}
	})
}

func TestActorTouchesTheSessionPeriodically(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.TouchEvery = 10 * time.Second; c.IdleTimeout = time.Hour })
		h.start(t.Context())
		time.Sleep(35 * time.Second)
		h.endSession()
		h.store.mu.Lock()
		touches := h.store.touches
		h.store.mu.Unlock()
		if touches < 3 {
			t.Fatalf("touched %d times in 35 s, want at least 3", touches)
		}
	})
}

func TestTurnDeadlineFailsASlowTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.TurnDeadline = 5 * time.Second; c.LLMFirstToken = time.Hour })
		h.llm.script = func(int, LLMRequest) llmScript { return llmScript{hang: true} }
		h.start(t.Context())
		h.text("hello")
		time.Sleep(10 * time.Second)
		if got := joinTrace(h.out.names()); got != "ready turn.started transcript error:llm_failed audio3 done:failed" {
			t.Fatalf("trace = %s", got)
		}
		h.endSession()
	})
}

func TestStaleFramesAreDroppedAfterBargeIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = slowReply
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(1100 * time.Millisecond)
		h.msg(MsgSpeechStart)
		time.Sleep(5 * time.Second)
		h.out.mu.Lock()
		dropped := h.out.dropped
		h.out.mu.Unlock()
		// Segments 1 and 2 were still being produced when the learner cut in; whatever they tried to send is dropped.
		_ = dropped
		if segs := h.out.segments(); len(segs) != 1 {
			t.Fatalf("segments after barge-in = %+v", segs)
		}
		h.endSession()
	})
}

func TestPersisterCoalescesTouches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memStore{epoch: 1}
		p := NewPersister(store, uuid.New(), 1, time.Now, func() {}, func(error) {})
		for range 50 {
			p.Touch()
		}
		synctest.Wait()
		if err := p.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.touches == 0 || store.touches > 2 {
			t.Fatalf("50 touches became %d writes", store.touches)
		}
	})
}

func TestPersisterStopsWritingOnceFenced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memStore{epoch: 2}
		fenced := 0
		p := NewPersister(store, uuid.New(), 1, time.Now, func() { fenced++ }, func(error) {})
		for i := range 3 {
			if !p.TryEnqueue(persistOp{kind: opInsert, insert: usecaseTurn(i + 1)}) {
				t.Fatal("enqueue failed")
			}
		}
		if err := p.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if inserts, _ := store.snapshot(); len(inserts) != 0 || fenced != 1 {
			t.Fatalf("inserts = %d, fenced callbacks = %d", len(inserts), fenced)
		}
		if p.TryEnqueue(persistOp{}) {
			t.Fatal("a drained persister accepted work")
		}
	})
}

func TestPersisterRetriesThenReportsFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memStore{epoch: 1, failInert: errors.New("db down")}
		var failures []error
		p := NewPersister(store, uuid.New(), 1, time.Now, func() {}, func(err error) { failures = append(failures, err) })
		p.TryEnqueue(persistOp{kind: opInsert, insert: usecaseTurn(1)})
		if err := p.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 {
			t.Fatalf("failures = %v", failures)
		}
	})
}
