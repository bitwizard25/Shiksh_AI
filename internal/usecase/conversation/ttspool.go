package conversation

import (
	"context"
	"sync"
)

type segment struct {
	seq  int
	text string
}

type ttsResult struct {
	pcm  []byte
	rate int
	err  error
}

type pollState int

const (
	pollPending pollState = iota // not synthesized yet
	pollReady                    // the result is returned
	pollDone                     // no segment with this number will ever exist
)

// ttsPool synthesizes segments in parallel and hands the results back in order, so a slow
// sentence never lets a later one overtake it.
type ttsPool struct {
	jobs   chan segment
	notify chan struct{}
	wg     sync.WaitGroup

	mu      sync.Mutex
	results map[int]ttsResult
	total   int
	closed  bool
}

// newTTSPool starts workers goroutines running synth. They stop when ctx ends or input is closed and drained.
func newTTSPool(ctx context.Context, workers int, synth func(context.Context, segment) ttsResult) *ttsPool {
	p := &ttsPool{jobs: make(chan segment, 128), notify: make(chan struct{}, 1), results: map[int]ttsResult{}}
	for range max(workers, 1) {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case seg, ok := <-p.jobs:
					if !ok {
						return
					}
					res := synth(ctx, seg)
					p.mu.Lock()
					p.results[seg.seq] = res
					p.mu.Unlock()
					p.wake()
				}
			}
		}()
	}
	return p
}

func (p *ttsPool) wake() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Submit queues a segment. Segments must be submitted with consecutive seq numbers from 0.
func (p *ttsPool) Submit(ctx context.Context, seg segment) error {
	p.mu.Lock()
	p.total++
	p.mu.Unlock()
	select {
	case p.jobs <- seg:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// CloseInput says no more segments will come.
func (p *ttsPool) CloseInput() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.jobs)
	}
	p.mu.Unlock()
	p.wake()
}

// Poll returns the result for seq if it is ready.
func (p *ttsPool) Poll(seq int) (ttsResult, pollState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.results[seq]; ok {
		delete(p.results, seq)
		return r, pollReady
	}
	if p.closed && seq >= p.total {
		return ttsResult{}, pollDone
	}
	return ttsResult{}, pollPending
}

// Notify fires (at most one pending signal) whenever a result arrives or the input closes.
func (p *ttsPool) Notify() <-chan struct{} { return p.notify }

// Wait blocks until every worker has stopped.
func (p *ttsPool) Wait() { p.wg.Wait() }

// final reports whether seq is known to be the last segment of the turn.
func (p *ttsPool) final(seq int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed && seq+1 >= p.total
}
