package conversation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const (
	persistQueue    = 32
	persistAttempts = 3
	persistTimeout  = 5 * time.Second
)

type opKind int

const (
	opInsert opKind = iota
	opUpdateTutor
)

// persistOp is one database write. Both kinds carry the session's epoch through the Persister.
type persistOp struct {
	kind    opKind
	insert  usecase.TurnInsert
	turnNo  int
	content string
	status  string
}

// Persister writes a session's turns to the database on its own goroutine, so the actor never
// waits on Postgres. Writes keep their order; touches are coalesced into at most one pending write.
type Persister struct {
	store     usecase.TurnStore
	sessionID uuid.UUID
	epoch     int
	now       func() time.Time
	onFenced  func()
	onFailure func(error)

	ops   chan persistOp
	wake  chan struct{}
	touch bool // guarded by mu

	mu      sync.Mutex
	closed  bool
	stopped chan struct{}
}

// NewPersister starts the writer goroutine. onFenced runs once a newer connection has replaced this
// one; onFailure runs when a write still fails after its retries. Both are called from the writer goroutine.
func NewPersister(store usecase.TurnStore, sessionID uuid.UUID, epoch int, now func() time.Time, onFenced func(), onFailure func(error)) *Persister {
	p := &Persister{
		store: store, sessionID: sessionID, epoch: epoch, now: now, onFenced: onFenced, onFailure: onFailure,
		ops: make(chan persistOp, persistQueue), wake: make(chan struct{}, 1), stopped: make(chan struct{}),
	}
	go p.run()
	return p
}

// TryEnqueue queues a write without blocking. It reports false if the queue is full or closed.
func (p *Persister) TryEnqueue(op persistOp) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	select {
	case p.ops <- op:
		return true
	default:
		return false
	}
}

// Touch asks for last_active_at to be refreshed. Repeated calls before the write runs collapse into one.
func (p *Persister) Touch() {
	p.mu.Lock()
	if !p.closed {
		p.touch = true
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Drain stops accepting writes, finishes the queued ones and waits for the writer, or until ctx ends.
func (p *Persister) Drain(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.ops)
	}
	p.mu.Unlock()
	select {
	case <-p.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Persister) run() {
	defer close(p.stopped)
	fenced := false
	for {
		select {
		case op, ok := <-p.ops:
			if !ok {
				if !fenced {
					p.flushTouch()
				}
				return
			}
			if fenced {
				continue // the connection is replaced: nothing it writes may land
			}
			if err := p.write(op); errors.Is(err, entity.ErrFenced) {
				fenced = true
				p.onFenced()
			} else if err != nil {
				p.onFailure(err)
			}
		case <-p.wake:
			if !fenced && p.flushTouch() {
				fenced = true
				p.onFenced()
			}
		}
	}
}

// flushTouch writes a pending touch. It reports whether the write found the connection fenced.
func (p *Persister) flushTouch() bool {
	p.mu.Lock()
	pending := p.touch
	p.touch = false
	p.mu.Unlock()
	if !pending {
		return false
	}
	err := p.retry(func(ctx context.Context) error { return p.store.Touch(ctx, p.sessionID, p.epoch, p.now()) })
	if errors.Is(err, entity.ErrFenced) {
		return true
	}
	if err != nil {
		p.onFailure(err)
	}
	return false
}

func (p *Persister) write(op persistOp) error {
	return p.retry(func(ctx context.Context) error {
		switch op.kind {
		case opInsert:
			return p.store.InsertTurn(ctx, p.sessionID, p.epoch, op.insert, p.now())
		default:
			return p.store.UpdateTutor(ctx, p.sessionID, p.epoch, op.turnNo, op.content, op.status)
		}
	})
}

// retry runs fn up to persistAttempts times with a growing pause. A fenced error is final.
func (p *Persister) retry(fn func(context.Context) error) error {
	var err error
	for attempt := range persistAttempts {
		ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
		err = fn(ctx)
		cancel()
		if err == nil || errors.Is(err, entity.ErrFenced) || errors.Is(err, entity.ErrNotFound) {
			return err
		}
		if attempt < persistAttempts-1 {
			time.Sleep(time.Duration(100<<attempt) * time.Millisecond)
		}
	}
	return err
}
