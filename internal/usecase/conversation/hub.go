package conversation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

const (
	replaceWait   = 3 * time.Second
	memorySummary = 3
)

// Connection is one accepted WebSocket, as the hub needs to see it.
type Connection struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Out       Output
	// Attach starts delivering client messages to the session. The hub calls it once the session is ready.
	Attach func(*Session)
}

type live struct {
	s    *Session
	done chan struct{}
}

// Hub tracks the sessions whose WebSocket this process hosts. Across processes, only the epoch
// fence in the database keeps a replaced connection from writing.
type Hub struct {
	deps Deps

	mu       sync.Mutex
	live     map[uuid.UUID]*live
	draining bool
	wg       sync.WaitGroup
}

// NewHub builds a hub. Deps must have Store, Users, Sessions, Messages and the providers set.
func NewHub(d Deps) *Hub {
	d.fill()
	return &Hub{deps: d, live: map[uuid.UUID]*live{}}
}

// Draining reports whether the hub refuses new connections.
func (h *Hub) Draining() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.draining
}

// Serve runs one connection to its end and returns how to close it. A new connection for a
// session that is already live here replaces the old one, and waits for the old one to finish
// writing before it bumps the epoch, so turn numbers never collide.
func (h *Hub) Serve(ctx context.Context, c Connection) CloseInfo {
	mine := &live{done: make(chan struct{})}
	if info, ok := h.register(ctx, c.SessionID, mine); !ok {
		return info
	}
	defer func() {
		h.mu.Lock()
		delete(h.live, c.SessionID)
		h.mu.Unlock()
		close(mine.done)
		h.wg.Done()
	}()

	sess, info, ok := h.load(ctx, c)
	if !ok {
		return info
	}
	h.mu.Lock()
	mine.s = sess
	h.mu.Unlock()

	h.deps.Metrics.Connections(1)
	defer h.deps.Metrics.Connections(-1)
	c.Attach(sess)
	return sess.Run(ctx)
}

// register makes mine the live entry for the session, replacing an existing one. When ok is
// false the connection must be closed with info, and nothing is registered.
func (h *Hub) register(ctx context.Context, id uuid.UUID, mine *live) (CloseInfo, bool) {
	for {
		h.mu.Lock()
		if h.draining {
			h.mu.Unlock()
			return CloseInfo{Code: CloseGoingAway, Reason: "server shutdown"}, false
		}
		old := h.live[id]
		if old == nil {
			h.live[id] = mine
			h.wg.Add(1)
			h.mu.Unlock()
			return CloseInfo{}, true
		}
		s := old.s
		h.mu.Unlock()
		if s != nil {
			s.Stop(StopReplaced)
		}
		select {
		case <-old.done:
		case <-time.After(replaceWait):
			return CloseInfo{Code: ClosePolicy, Reason: "previous connection did not close"}, false
		case <-ctx.Done():
			return CloseInfo{Code: CloseGoingAway, Reason: "server shutdown"}, false
		}
	}
}

// load bumps the epoch and gathers what the actor needs. It runs after the previous connection finished.
func (h *Hub) load(ctx context.Context, c Connection) (*Session, CloseInfo, bool) {
	fail := func(err error) (*Session, CloseInfo, bool) {
		if errors.Is(err, entity.ErrNotFound) {
			return nil, CloseInfo{Code: CloseSessionEnded, Reason: "session ended or not found"}, false
		}
		h.deps.Log.Error("load session failed", "session_id", c.SessionID, "err", err)
		c.Out.TrySend(ErrorEvent{Code: ErrStorageUnavailable, Message: "could not open the session, try again", Retryable: true})
		return nil, CloseInfo{Code: CloseInternal, Reason: "storage unavailable"}, false
	}
	d := &h.deps
	epoch, err := d.Store.Connect(ctx, c.SessionID, d.Now())
	if err != nil {
		return fail(err)
	}
	meta, err := d.Sessions.Get(ctx, c.SessionID)
	if err != nil {
		return fail(err)
	}
	if meta.UserID != c.UserID {
		return fail(entity.ErrNotFound)
	}
	user, err := d.Users.GetByID(ctx, meta.UserID)
	if err != nil {
		return fail(err)
	}
	maxTurn, err := d.Store.MaxTurnNo(ctx, c.SessionID)
	if err != nil {
		return fail(err)
	}
	history, err := d.Messages.Recent(ctx, c.SessionID, d.Cfg.HistoryMaxMessages)
	if err != nil {
		return fail(err)
	}
	memory, err := d.Sessions.RecentSummaries(ctx, meta.UserID, meta.Subject, memorySummary)
	if err != nil {
		return fail(err)
	}
	sess, err := NewSession(SessionParams{
		Deps: h.deps, Session: meta, User: user, Epoch: epoch, Out: c.Out,
		History: history, Memory: memory, MaxTurnNo: maxTurn,
	})
	if err != nil {
		return fail(err)
	}
	return sess, CloseInfo{}, true
}

// End asks a live session to end as finished by the learner. It reports whether one is live here.
func (h *Hub) End(id uuid.UUID) bool {
	h.mu.Lock()
	l := h.live[id]
	h.mu.Unlock()
	if l == nil || l.s == nil {
		return false
	}
	l.s.Stop(StopEndRequest)
	return true
}

// Live reports whether a session is connected through this hub.
func (h *Hub) Live(id uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[id] != nil
}

// Drain refuses new connections, tells every live session the server is shutting down, and waits
// for them to save what they have, or until ctx ends.
func (h *Hub) Drain(ctx context.Context) error {
	h.mu.Lock()
	h.draining = true
	var sessions []*Session
	for _, l := range h.live {
		if l.s != nil {
			sessions = append(sessions, l.s)
		}
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.Stop(StopShutdown)
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
