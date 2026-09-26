package gateway

import (
	"context"
	"errors"
	"iter"
	"sync"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// Outcomes of a guarded call (the outcome label of shiksha_provider_requests_total).
const (
	outcomeOK          = "ok"
	outcomeCanceled    = "canceled"     // the caller gave up (barge-in, early stop): not the provider's fault
	outcomeClientError = "client_error" // non-retryable answer (4xx, blocked, unsupported): the provider is up
	outcomeError       = "error"        // timeout, network error, 429, 5xx: counts toward opening the breaker
	outcomeRejected    = "rejected"     // failed fast: the breaker is open
)

// Breaker states (the value of shiksha_breaker_state).
const (
	stateClosed   = 0
	stateHalfOpen = 1
	stateOpen     = 2
)

const (
	opASR         = "asr"
	opTTS         = "tts"
	opLLMStream   = "llm_stream"
	opLLMComplete = "llm_complete"
)

// GuardConfig configures one provider's guard. Zero values take the defaults.
type GuardConfig struct {
	Provider         string        // metric label, e.g. "bhashini"
	MaxConcurrency   int           // default 64
	FailureThreshold int           // consecutive failures that open the breaker; default 5
	OpenFor          time.Duration // how long the breaker stays open before a probe; default 15s
}

// Guard protects one provider. At most MaxConcurrency calls run at once. After FailureThreshold
// consecutive failures, calls fail fast with conversation.ErrProviderUnavailable for OpenFor (so a
// turn can play its error clip at once instead of waiting out timeouts); then a single probe call
// decides whether the breaker closes again.
type Guard struct {
	cfg     GuardConfig
	metrics *Metrics
	sem     chan struct{}
	now     func() time.Time

	mu        sync.Mutex
	state     int
	failures  int
	openUntil time.Time
	probing   bool
}

// NewGuard builds a guard; a nil m uses unregistered metrics.
func NewGuard(cfg GuardConfig, m *Metrics) *Guard {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 64
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.OpenFor <= 0 {
		cfg.OpenFor = 15 * time.Second
	}
	if m == nil {
		m = NewMetrics(nil)
	}
	g := &Guard{cfg: cfg, metrics: m, sem: make(chan struct{}, cfg.MaxConcurrency), now: time.Now}
	m.breaker.WithLabelValues(cfg.Provider).Set(stateClosed)
	return g
}

// ASR wraps next with the guard.
func (g *Guard) ASR(next conversation.ASR) conversation.ASR { return guardedASR{g, next} }

// TTS wraps next with the guard.
func (g *Guard) TTS(next conversation.TTS) conversation.TTS { return guardedTTS{g, next} }

// LLM wraps next with the guard.
func (g *Guard) LLM(next conversation.LLM) conversation.LLM { return guardedLLM{g, next} }

type guardedASR struct {
	g    *Guard
	next conversation.ASR
}

func (a guardedASR) Transcribe(ctx context.Context, req conversation.ASRRequest) (conversation.ASRResult, error) {
	var res conversation.ASRResult
	err := a.g.do(ctx, opASR, func(ctx context.Context) error {
		var err error
		res, err = a.next.Transcribe(ctx, req)
		return err
	})
	return res, err
}

type guardedTTS struct {
	g    *Guard
	next conversation.TTS
}

func (t guardedTTS) Synthesize(ctx context.Context, req conversation.TTSRequest) (conversation.TTSResult, error) {
	var res conversation.TTSResult
	err := t.g.do(ctx, opTTS, func(ctx context.Context) error {
		var err error
		res, err = t.next.Synthesize(ctx, req)
		return err
	})
	return res, err
}

type guardedLLM struct {
	g    *Guard
	next conversation.LLM
}

func (l guardedLLM) Complete(ctx context.Context, req conversation.LLMRequest) (string, error) {
	var out string
	err := l.g.do(ctx, opLLMComplete, func(ctx context.Context) error {
		var err error
		out, err = l.next.Complete(ctx, req)
		return err
	})
	return out, err
}

func (l guardedLLM) Stream(ctx context.Context, req conversation.LLMRequest) iter.Seq2[conversation.Delta, error] {
	return func(yield func(conversation.Delta, error) bool) {
		g := l.g
		probe, err := g.admit(opLLMStream)
		if err != nil {
			g.count(opLLMStream, outcomeRejected)
			yield(conversation.Delta{}, err)
			return
		}
		if err := g.acquire(ctx); err != nil {
			g.record(probe, outcomeCanceled)
			g.count(opLLMStream, outcomeCanceled)
			yield(conversation.Delta{}, err)
			return
		}
		var (
			streamErr error
			stopped   bool
		)
		func() {
			defer g.release()
			start, first := g.now(), true
			for d, err := range l.next.Stream(ctx, req) {
				if err != nil {
					streamErr = err
					return
				}
				if first {
					first = false
					g.observe(opLLMStream, g.now().Sub(start))
				}
				if !yield(d, nil) {
					stopped = true
					return
				}
			}
		}()
		outcome := classify(ctx, streamErr)
		if stopped {
			outcome = outcomeCanceled
		}
		g.record(probe, outcome)
		g.count(opLLMStream, outcome)
		if streamErr != nil {
			yield(conversation.Delta{}, streamErr)
		}
	}
}

// do runs one unary call under the guard.
func (g *Guard) do(ctx context.Context, op string, fn func(context.Context) error) error {
	probe, err := g.admit(op)
	if err != nil {
		g.count(op, outcomeRejected)
		return err
	}
	if err := g.acquire(ctx); err != nil {
		g.record(probe, outcomeCanceled)
		g.count(op, outcomeCanceled)
		return err
	}
	start := g.now()
	err = fn(ctx)
	g.release()
	outcome := classify(ctx, err)
	if outcome != outcomeCanceled {
		g.observe(op, g.now().Sub(start))
	}
	g.record(probe, outcome)
	g.count(op, outcome)
	return err
}

// admit decides whether a call may start; probe is true for the single half-open trial call.
func (g *Guard) admit(op string) (probe bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.state {
	case stateOpen:
		if g.now().Before(g.openUntil) {
			return false, g.unavailable(op)
		}
		g.setState(stateHalfOpen)
		fallthrough
	case stateHalfOpen:
		if g.probing {
			return false, g.unavailable(op)
		}
		g.probing = true
		return true, nil
	}
	return false, nil
}

// record feeds a finished call's outcome to the breaker.
func (g *Guard) record(probe bool, outcome string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if probe {
		g.probing = false
	}
	switch outcome {
	case outcomeOK, outcomeClientError:
		g.failures = 0
		if g.state != stateClosed {
			g.setState(stateClosed)
		}
	case outcomeError:
		g.failures++
		if probe || (g.state == stateClosed && g.failures >= g.cfg.FailureThreshold) {
			g.openUntil = g.now().Add(g.cfg.OpenFor)
			g.setState(stateOpen)
		}
	}
	// outcomeCanceled carries no evidence; after a canceled probe the next call probes.
}

func (g *Guard) setState(s int) {
	g.state = s
	g.metrics.breaker.WithLabelValues(g.cfg.Provider).Set(float64(s))
}

func (g *Guard) unavailable(op string) error {
	return &conversation.ProviderError{Provider: g.cfg.Provider, Op: op, Err: conversation.ErrProviderUnavailable}
}

func (g *Guard) acquire(ctx context.Context) error {
	select {
	case g.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Guard) release() { <-g.sem }

func (g *Guard) count(op, outcome string) {
	g.metrics.requests.WithLabelValues(g.cfg.Provider, op, outcome).Inc()
}

func (g *Guard) observe(op string, d time.Duration) {
	g.metrics.latency.WithLabelValues(g.cfg.Provider, op).Observe(d.Seconds())
}

func classify(ctx context.Context, err error) string {
	var pe *conversation.ProviderError
	switch {
	case err == nil:
		return outcomeOK
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return outcomeCanceled
	case errors.As(err, &pe) && !pe.Retryable:
		return outcomeClientError
	default:
		return outcomeError
	}
}
