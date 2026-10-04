package ws

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// fakeConn records writes; block, when set, makes writes wait for it to be closed.
type fakeConn struct {
	mu     sync.Mutex
	writes []write
	block  chan struct{}
	failOn int // fail the n-th write (1-based); 0 = never
	closed *closeRec
}

type write struct {
	typ  websocket.MessageType
	data []byte
}

type closeRec struct {
	code   websocket.StatusCode
	reason string
}

func (c *fakeConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failOn > 0 && len(c.writes)+1 == c.failOn {
		return errors.New("write failed")
	}
	c.writes = append(c.writes, write{typ, p})
	return nil
}

func (c *fakeConn) Close(code websocket.StatusCode, reason string) error {
	c.mu.Lock()
	c.closed = &closeRec{code, reason}
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) written() []write {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]write(nil), c.writes...)
}

func audio(turn uint32, seq uint16, last bool) conversation.AudioFrame {
	return conversation.AudioFrame{Kind: conversation.KindSpeech, TurnID: turn, ChunkSeq: seq, LastOfSeg: last, SampleRate: 22050, PCM: make([]byte, 4)}
}

func TestOutboxDropsFramesOfStaleTurns(t *testing.T) {
	c := &fakeConn{block: make(chan struct{})}
	dropped := 0
	o := NewOutbox(t.Context(), c, func(error) {}, func() { dropped++ })
	o.SetTurn(1)
	for i := range 3 {
		if err := o.Send(t.Context(), audio(1, uint16(i), true)); err != nil {
			t.Fatal(err)
		}
	}
	// The learner interrupts while the writer is stuck: everything queued for turn 1 is now stale.
	o.SetTurn(0)
	if !o.TrySend(conversation.TutorDone{TurnID: 1, Status: "interrupted", Control: true}) {
		t.Fatal("control event rejected")
	}
	close(c.block)
	o.Close(1000, "bye")

	got := c.written()
	// One frame was already in flight inside Write when the turn changed; everything after it is dropped.
	var audioWrites int
	for _, w := range got {
		if w.typ == websocket.MessageBinary {
			audioWrites++
		}
	}
	if audioWrites > 1 {
		t.Fatalf("%d stale audio frames were written", audioWrites)
	}
	if dropped < 2 {
		t.Fatalf("dropped = %d", dropped)
	}
	if last := got[len(got)-1]; last.typ != websocket.MessageText || string(last.data) == "" {
		t.Fatalf("the control tutor.done was not written last: %+v", got)
	}
	if c.closed == nil || c.closed.code != 1000 {
		t.Fatalf("closed = %+v", c.closed)
	}
}

func TestOutboxKeepsOrder(t *testing.T) {
	c := &fakeConn{}
	o := NewOutbox(t.Context(), c, func(error) {}, nil)
	o.SetTurn(2)
	_ = o.Send(t.Context(), conversation.TutorSegment{TurnID: 2, ChunkSeq: 0, Text: "x", SampleRate: 22050})
	_ = o.Send(t.Context(), audio(2, 0, false))
	_ = o.Send(t.Context(), audio(2, 0, true))
	o.Close(1000, "")
	got := c.written()
	if len(got) != 3 || got[0].typ != websocket.MessageText || got[1].typ != websocket.MessageBinary || got[2].typ != websocket.MessageBinary {
		t.Fatalf("writes = %+v", got)
	}
}

func TestOutboxCountsCompletedSegments(t *testing.T) {
	c := &fakeConn{}
	o := NewOutbox(t.Context(), c, func(error) {}, nil)
	o.SetTurn(1)
	_ = o.Send(t.Context(), audio(1, 0, false))
	_ = o.Send(t.Context(), audio(1, 0, true))                                                           // segment 0 complete
	_ = o.Send(t.Context(), conversation.TutorSegment{TurnID: 1, ChunkSeq: 1, Text: "x", NoAudio: true}) // caption only
	_ = o.Send(t.Context(), audio(1, 2, false))                                                          // segment 2 incomplete
	o.Close(1000, "")
	if n := o.HeardSegments(1); n != 2 {
		t.Fatalf("heard = %d, want 2", n)
	}
	if n := o.HeardSegments(9); n != 0 {
		t.Fatalf("unknown turn heard = %d", n)
	}
}

func TestOutboxTrySendReportsFullQueue(t *testing.T) {
	c := &fakeConn{block: make(chan struct{})}
	o := NewOutbox(t.Context(), c, func(error) {}, nil)
	accepted := 0
	for range outboxSize + 10 {
		if o.TrySend(conversation.Pong{}) {
			accepted++
		}
	}
	// The writer holds one frame inside Write, so the queue plus that one frame fit.
	if accepted < outboxSize || accepted > outboxSize+1 {
		t.Fatalf("accepted %d, want about %d", accepted, outboxSize)
	}
	close(c.block)
	o.Close(1000, "")
}

func TestOutboxSendRespectsContext(t *testing.T) {
	c := &fakeConn{block: make(chan struct{})}
	o := NewOutbox(t.Context(), c, func(error) {}, nil)
	o.SetTurn(1)
	for range outboxSize + 1 {
		_ = o.Send(t.Context(), audio(1, 0, false))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := o.Send(ctx, audio(1, 0, false)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	close(c.block)
	o.Close(1000, "")
	if err := o.Send(t.Context(), audio(1, 0, false)); err == nil {
		t.Fatal("Send after Close succeeded")
	}
	if o.TrySend(conversation.Pong{}) {
		t.Fatal("TrySend after Close succeeded")
	}
}

func TestOutboxReportsWriteFailure(t *testing.T) {
	c := &fakeConn{failOn: 2}
	failed := make(chan error, 1)
	o := NewOutbox(t.Context(), c, func(err error) { failed <- err }, nil)
	o.TrySend(conversation.Pong{})
	o.TrySend(conversation.Pong{})
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("write failure was not reported")
	}
	o.Close(1011, "x")
}

func TestOutboxCloseIsIdempotentAndTruncatesReason(t *testing.T) {
	c := &fakeConn{}
	o := NewOutbox(t.Context(), c, func(error) {}, nil)
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	o.Close(4009, string(long))
	o.Close(1000, "again")
	if c.closed == nil || c.closed.code != 4009 || len(c.closed.reason) != 120 {
		t.Fatalf("closed = %+v", c.closed)
	}
}
