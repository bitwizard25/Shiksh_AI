package conversation

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

type hubRig struct {
	hub   *Hub
	db    *memDB
	store *memStore
	llm   *fakeLLM
	ended []uuid.UUID
}

func newHubRig(t *testing.T) *hubRig {
	t.Helper()
	r := &hubRig{store: &memStore{}}
	r.db = &memDB{
		store: r.store,
		sess:  entity.TutoringSession{ID: uuid.New(), UserID: uuid.New(), Subject: "math", Language: "en", Status: entity.SessionCreated},
		user:  entity.User{DisplayName: "Asha"},
	}
	r.llm = &fakeLLM{script: func(int, LLMRequest) llmScript { return llmScript{deltas: []string{"Here is an answer."}} }}
	r.hub = NewHub(Deps{
		ASR: &fakeASR{text: "hello"}, LLM: r.llm, TTS: &fakeTTS{}, Store: r.store,
		Users: r.db.users(), Sessions: r.db.sessions(), Messages: r.db.messages(),
		Cfg: testConfig(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnEnded: func(id uuid.UUID) { r.ended = append(r.ended, id) },
	})
	return r
}

// conn is one fake connection served by the hub.
type conn struct {
	out  *fakeOut
	sess chan *Session
	res  chan CloseInfo
}

func (r *hubRig) serve(ctx context.Context, userID uuid.UUID) *conn {
	c := &conn{out: newFakeOut(), sess: make(chan *Session, 1), res: make(chan CloseInfo, 1)}
	go func() {
		c.res <- r.hub.Serve(ctx, Connection{
			UserID: userID, SessionID: r.db.sess.ID, Out: c.out,
			Attach: func(s *Session) { c.sess <- s },
		})
	}()
	return c
}

func TestHubServesASessionToItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newHubRig(t)
		c := r.serve(t.Context(), r.db.sess.UserID)
		s := <-c.sess
		s.Message(ClientMsg{Type: MsgTextInput, Text: "hi"})
		time.Sleep(3 * time.Second)
		s.Message(ClientMsg{Type: MsgSessionEnd})
		if info := <-c.res; info.Code != CloseNormal {
			t.Fatalf("close = %+v", info)
		}
		if inserts, _ := r.store.snapshot(); len(inserts) != 1 || r.store.epoch != 1 {
			t.Fatalf("inserts = %d, epoch = %d", len(inserts), r.store.epoch)
		}
		if len(r.ended) != 1 {
			t.Fatal("summary hook not called")
		}
	})
}

func TestHubReplacesLiveConnectionBeforeLoadingTheNewOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newHubRig(t)
		first := r.serve(t.Context(), r.db.sess.UserID)
		s1 := <-first.sess
		s1.Message(ClientMsg{Type: MsgTextInput, Text: "hi"})
		time.Sleep(3 * time.Second)

		second := r.serve(t.Context(), r.db.sess.UserID)
		s2 := <-second.sess
		if info := <-first.res; info.Code != CloseReplaced {
			t.Fatalf("first close = %+v", info)
		}
		synctest.Wait()
		ready := second.out.find(func(e Event) bool { _, ok := e.(SessionReady); return ok })[0].(SessionReady)
		// The first connection had stored turn 1 before the second loaded, so numbering continues at 2.
		if !ready.Resumed || ready.NextTurnID != 2 || len(ready.History) != 2 {
			t.Fatalf("ready = %+v", ready)
		}
		if r.store.epoch != 2 {
			t.Fatalf("epoch = %d, want 2", r.store.epoch)
		}
		s2.Message(ClientMsg{Type: MsgTextInput, Text: "next"})
		time.Sleep(3 * time.Second)
		s2.Message(ClientMsg{Type: MsgSessionEnd})
		<-second.res
		inserts, _ := r.store.snapshot()
		if len(inserts) != 2 || inserts[0].TurnNo != 1 || inserts[1].TurnNo != 2 {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestHubEndReachesOnlyLiveSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newHubRig(t)
		if r.hub.End(r.db.sess.ID) {
			t.Fatal("End reported a live session before any connected")
		}
		c := r.serve(t.Context(), r.db.sess.UserID)
		<-c.sess
		synctest.Wait()
		if !r.hub.End(r.db.sess.ID) {
			t.Fatal("End did not find the live session")
		}
		if info := <-c.res; info.Code != CloseNormal {
			t.Fatalf("close = %+v", info)
		}
		if r.hub.End(r.db.sess.ID) {
			t.Fatal("End found a session that already finished")
		}
	})
}

func TestHubDrainStopsLiveSessionsAndRefusesNewOnes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newHubRig(t)
		c := r.serve(t.Context(), r.db.sess.UserID)
		<-c.sess
		synctest.Wait()
		if err := r.hub.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if info := <-c.res; info.Code != CloseGoingAway || !contains(c.out.names(), "error:server_shutdown") {
			t.Fatalf("close = %+v, trace %v", info, c.out.names())
		}
		if !r.hub.Draining() {
			t.Fatal("not draining")
		}
		late := r.serve(t.Context(), r.db.sess.UserID)
		if info := <-late.res; info.Code != CloseGoingAway {
			t.Fatalf("late connection close = %+v", info)
		}
	})
}

func TestHubRejectsAnotherUsersSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newHubRig(t)
		c := r.serve(t.Context(), uuid.New())
		if info := <-c.res; info.Code != CloseSessionEnded {
			t.Fatalf("close = %+v", info)
		}
		if r.hub.End(r.db.sess.ID) {
			t.Fatal("a rejected connection stayed registered")
		}
	})
}
