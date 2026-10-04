package ws

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

const (
	outboxSize   = 512
	writeTimeout = 5 * time.Second
	flushTimeout = 2 * time.Second
	heardWindow  = 8 // how many recent turns' written-segment counts are kept
)

var errOutboxClosed = errors.New("ws: outbox closed")

// Conn is the part of *websocket.Conn the outbox uses.
type Conn interface {
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
	Close(code websocket.StatusCode, reason string) error
}

type frame struct {
	typ    websocket.MessageType
	data   []byte
	turnID uint32
	scoped bool // may be dropped as stale
	counts bool // writing it completes one tutor segment
}

// Outbox is the single writer of a connection. Frames queue in a bounded channel and one goroutine
// writes them. Turn-scoped frames whose turn is no longer current are dropped at write time, which
// removes audio that was queued before the learner interrupted. It implements conversation.Output.
type Outbox struct {
	conn    Conn
	ctx     context.Context
	q       chan frame
	cur     atomic.Uint32
	onError func(error)
	dropped func()

	closing chan struct{}
	stopped chan struct{} // closed when the writer exits
	once    sync.Once

	mu    sync.Mutex
	heard map[uint32]int
}

var _ conversation.Output = (*Outbox)(nil)

// NewOutbox starts the writer. ctx is the connection's context: write timeouts derive from it, and
// the writer stops when it ends. onError runs once if a write fails; dropped counts stale frames (may be nil).
func NewOutbox(ctx context.Context, conn Conn, onError func(error), dropped func()) *Outbox {
	if dropped == nil {
		dropped = func() {}
	}
	o := &Outbox{
		conn: conn, ctx: ctx, q: make(chan frame, outboxSize), onError: onError, dropped: dropped,
		closing: make(chan struct{}), stopped: make(chan struct{}), heard: map[uint32]int{},
	}
	go o.run()
	return o
}

func (o *Outbox) SetTurn(id uint32) { o.cur.Store(id) }

func (o *Outbox) build(e conversation.Event, control bool) (frame, error) {
	typ, data, err := Encode(e)
	if err != nil {
		return frame{}, err
	}
	id, scoped := e.Turn()
	f := frame{typ: typ, data: data, turnID: id, scoped: scoped && !control}
	switch e := e.(type) {
	case conversation.AudioFrame:
		f.counts = e.Kind == conversation.KindSpeech && e.LastOfSeg
	case conversation.TutorSegment:
		f.counts = e.NoAudio
	}
	return f, nil
}

// Send queues a turn-scoped event, waiting for room or for ctx to end.
func (o *Outbox) Send(ctx context.Context, e conversation.Event) error {
	f, err := o.build(e, false)
	if err != nil {
		return err
	}
	select {
	case <-o.closing:
		return errOutboxClosed
	default:
	}
	select {
	case o.q <- f:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-o.closing:
		return errOutboxClosed
	}
}

// TrySend queues a control event without waiting. It reports false when the queue is full or closed.
func (o *Outbox) TrySend(e conversation.Event) bool {
	f, err := o.build(e, true)
	if err != nil {
		return false
	}
	select {
	case <-o.closing:
		return false
	default:
	}
	select {
	case o.q <- f:
		return true
	default:
		return false
	}
}

// HeardSegments returns how many tutor segments of the turn were completely written.
func (o *Outbox) HeardSegments(turnID uint32) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.heard[turnID]
}

func (o *Outbox) countWritten(turnID uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.heard[turnID]++
	if len(o.heard) > heardWindow {
		oldest := turnID
		for id := range o.heard {
			oldest = min(oldest, id)
		}
		delete(o.heard, oldest)
	}
}

func (o *Outbox) run() {
	defer close(o.stopped)
	for {
		select {
		case f := <-o.q:
			if !o.write(f) {
				return
			}
		case <-o.closing:
			for { // flush what is already queued, then stop
				select {
				case f := <-o.q:
					if !o.write(f) {
						return
					}
				default:
					return
				}
			}
		case <-o.ctx.Done():
			return
		}
	}
}

// write sends one frame, or drops it if it is stale. It reports false when the writer must stop.
func (o *Outbox) write(f frame) bool {
	if f.scoped && f.turnID != o.cur.Load() {
		o.dropped()
		return true
	}
	ctx, cancel := context.WithTimeout(o.ctx, writeTimeout)
	err := o.conn.Write(ctx, f.typ, f.data)
	cancel()
	if err != nil {
		o.onError(err)
		return false
	}
	if f.counts {
		o.countWritten(f.turnID)
	}
	return true
}

// Close flushes the queue (bounded), then closes the connection with the given WebSocket status code.
func (o *Outbox) Close(code int, reason string) {
	o.once.Do(func() {
		close(o.closing)
		select {
		case <-o.stopped:
		case <-time.After(flushTimeout):
		}
		_ = o.conn.Close(websocket.StatusCode(code), truncate(reason, 120))
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
