package conversation

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

func TestTTSPoolReturnsResultsInOrderDespiteRandomDelays(t *testing.T) {
	ctx := t.Context()
	synth := func(_ context.Context, s segment) ttsResult {
		time.Sleep(time.Duration(rand.IntN(20)) * time.Millisecond)
		return ttsResult{pcm: []byte(fmt.Sprint(s.seq)), rate: 22050}
	}
	p := newTTSPool(ctx, 3, synth)
	const n = 30
	go func() {
		for i := range n {
			_ = p.Submit(ctx, segment{seq: i, text: "x"})
		}
		p.CloseInput()
	}()
	for seq := 0; seq < n; {
		res, st := p.Poll(seq)
		switch st {
		case pollReady:
			if string(res.pcm) != fmt.Sprint(seq) {
				t.Fatalf("seq %d got %q", seq, res.pcm)
			}
			seq++
		case pollDone:
			t.Fatalf("pool finished early at %d", seq)
		default:
			select {
			case <-p.Notify():
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for results")
			}
		}
	}
	if _, st := p.Poll(n); st != pollDone {
		t.Fatalf("after the last segment state = %v, want done", st)
	}
}

func TestTTSPoolWorkersStopWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	p := newTTSPool(ctx, 2, func(ctx context.Context, _ segment) ttsResult { <-ctx.Done(); return ttsResult{err: ctx.Err()} })
	_ = p.Submit(ctx, segment{seq: 0})
	cancel()
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not stop")
	}
}

func TestBucket(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := newBucket(3, 30, now) // burst 3, one token per 2 s
	for i := range 3 {
		if !b.allow(now) {
			t.Fatalf("burst call %d rejected", i)
		}
	}
	if b.allow(now) {
		t.Fatal("fourth immediate call allowed")
	}
	if b.allow(now.Add(time.Second)) {
		t.Fatal("allowed after 1s; a token takes 2s")
	}
	if !b.allow(now.Add(2 * time.Second)) {
		t.Fatal("not allowed after 2s")
	}
	if !b.allow(now.Add(time.Hour)) || !b.allow(now.Add(time.Hour)) || !b.allow(now.Add(time.Hour)) || b.allow(now.Add(time.Hour)) {
		t.Fatal("refill must cap at the burst size")
	}
}
