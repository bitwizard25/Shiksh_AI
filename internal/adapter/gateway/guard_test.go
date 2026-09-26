package gateway

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

var (
	errServer = &conversation.ProviderError{Provider: "p", Op: "asr", Status: 503, Retryable: true, Err: errors.New("overloaded")}
	errClient = &conversation.ProviderError{Provider: "p", Op: "asr", Status: 400, Err: errors.New("bad request")}
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

// stubASR returns whatever fn returns and counts calls.
type stubASR struct {
	calls atomic.Int32
	fn    func(ctx context.Context) error
}

func (s *stubASR) Transcribe(ctx context.Context, _ conversation.ASRRequest) (conversation.ASRResult, error) {
	s.calls.Add(1)
	if err := s.fn(ctx); err != nil {
		return conversation.ASRResult{}, err
	}
	return conversation.ASRResult{Text: "ok"}, nil
}

func newTestGuard(t *testing.T, cfg GuardConfig) (*Guard, *prometheus.Registry, *fakeClock) {
	t.Helper()
	reg := prometheus.NewRegistry()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	if cfg.Provider == "" {
		cfg.Provider = "p"
	}
	g := NewGuard(cfg, NewMetrics(reg))
	g.now = clock.Now
	return g, reg, clock
}

// metricValue reads a counter or gauge value, or a histogram's sample count, by name and labels.
func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels ...string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		want[labels[i]] = labels[i+1]
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metrics:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if got[k] != v {
					continue metrics
				}
			}
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				return float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

type stubTTS struct{}

func (stubTTS) Synthesize(context.Context, conversation.TTSRequest) (conversation.TTSResult, error) {
	return conversation.TTSResult{PCM: []byte{1, 0, 2, 0}, SampleRate: 22050}, nil
}

func TestGuardPassesResultsThrough(t *testing.T) {
	g, reg, _ := newTestGuard(t, GuardConfig{})
	ctx := context.Background()
	asr, err := g.ASR(&stubASR{fn: func(context.Context) error { return nil }}).Transcribe(ctx, conversation.ASRRequest{Lang: "hi"})
	if err != nil || asr.Text != "ok" {
		t.Fatalf("ASR = %+v, %v", asr, err)
	}
	tts, err := g.TTS(stubTTS{}).Synthesize(ctx, conversation.TTSRequest{Lang: "hi", Text: "नमस्ते"})
	if err != nil || tts.SampleRate != 22050 || len(tts.PCM) != 4 {
		t.Fatalf("TTS = rate %d, %d bytes, %v", tts.SampleRate, len(tts.PCM), err)
	}
	llm := g.LLM(stubLLM{deltas: []string{"न", "म"}})
	if s, err := llm.Complete(ctx, conversation.LLMRequest{}); err != nil || s != "नम" {
		t.Fatalf("Complete = %q, %v", s, err)
	}
	var streamed string
	for d, err := range llm.Stream(ctx, conversation.LLMRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		streamed += d.Text
	}
	if streamed != "नम" {
		t.Fatalf("stream = %q", streamed)
	}
	for _, op := range []string{"asr", "tts", "llm_complete", "llm_stream"} {
		if v := metricValue(t, reg, "shiksha_provider_requests_total", "provider", "p", "op", op, "outcome", "ok"); v != 1 {
			t.Errorf("%s ok count = %v, want 1", op, v)
		}
		if v := metricValue(t, reg, "shiksha_provider_latency_seconds", "provider", "p", "op", op); v != 1 {
			t.Errorf("%s latency samples = %v, want 1", op, v)
		}
	}
}

func TestBreakerOpensProbesAndCloses(t *testing.T) {
	g, reg, clock := newTestGuard(t, GuardConfig{})
	stub := &stubASR{fn: func(context.Context) error { return errServer }}
	asr := g.ASR(stub)
	ctx := context.Background()

	for range 5 {
		if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); !errors.Is(err, errServer) {
			t.Fatalf("err = %v", err)
		}
	}
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 2 {
		t.Fatalf("breaker state = %v, want 2 (open)", v)
	}
	_, err := asr.Transcribe(ctx, conversation.ASRRequest{})
	var pe *conversation.ProviderError
	if !errors.Is(err, conversation.ErrProviderUnavailable) || !errors.As(err, &pe) || pe.Retryable || stub.calls.Load() != 5 {
		t.Fatalf("open breaker: err = %v, calls = %d", err, stub.calls.Load())
	}
	if v := metricValue(t, reg, "shiksha_provider_requests_total", "op", "asr", "outcome", "rejected"); v != 1 {
		t.Fatalf("rejected = %v", v)
	}

	clock.Advance(15 * time.Second) // probe fails: open again
	if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); !errors.Is(err, errServer) {
		t.Fatalf("probe err = %v", err)
	}
	if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); !errors.Is(err, conversation.ErrProviderUnavailable) {
		t.Fatalf("after failed probe: err = %v, want unavailable", err)
	}

	clock.Advance(15 * time.Second) // probe succeeds: closed
	stub.fn = func(context.Context) error { return nil }
	if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); err != nil {
		t.Fatalf("probe err = %v", err)
	}
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 0 {
		t.Fatalf("breaker state = %v, want 0 (closed)", v)
	}
}

func TestHalfOpenAllowsExactlyOneProbe(t *testing.T) {
	g, reg, clock := newTestGuard(t, GuardConfig{FailureThreshold: 1})
	entered, release := make(chan struct{}), make(chan struct{})
	stub := &stubASR{fn: func(context.Context) error { return errServer }}
	asr := g.ASR(stub)
	ctx := context.Background()
	_, _ = asr.Transcribe(ctx, conversation.ASRRequest{}) // one failure opens it
	clock.Advance(15 * time.Second)

	stub.fn = func(context.Context) error { close(entered); <-release; return nil }
	probeDone := make(chan error, 1)
	go func() { _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); probeDone <- err }()
	<-entered
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 1 {
		t.Fatalf("breaker state = %v, want 1 (half-open)", v)
	}
	if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); !errors.Is(err, conversation.ErrProviderUnavailable) {
		t.Fatalf("second call during probe: err = %v, want unavailable", err)
	}
	close(release)
	if err := <-probeDone; err != nil {
		t.Fatalf("probe = %v", err)
	}
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 0 {
		t.Fatalf("breaker state = %v, want 0", v)
	}
}

func TestCancellationDoesNotTripBreaker(t *testing.T) {
	g, reg, _ := newTestGuard(t, GuardConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	asr := g.ASR(&stubASR{fn: func(ctx context.Context) error { return ctx.Err() }})
	for range 20 { // repeated barge-ins
		if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	}
	clientErr := g.ASR(&stubASR{fn: func(context.Context) error { return errClient }})
	for range 20 {
		_, _ = clientErr.Transcribe(context.Background(), conversation.ASRRequest{})
	}
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 0 {
		t.Fatalf("breaker state = %v, want closed", v)
	}
	if c, ce := metricValue(t, reg, "shiksha_provider_requests_total", "outcome", "canceled"), metricValue(t, reg, "shiksha_provider_requests_total", "outcome", "client_error"); c != 20 || ce != 20 {
		t.Fatalf("canceled = %v, client_error = %v", c, ce)
	}
}

func TestSuccessResetsConsecutiveFailures(t *testing.T) {
	g, reg, _ := newTestGuard(t, GuardConfig{})
	fail := true
	asr := g.ASR(&stubASR{fn: func(context.Context) error {
		if fail {
			return errServer
		}
		return nil
	}})
	run := func(n int, f bool) {
		fail = f
		for range n {
			_, _ = asr.Transcribe(context.Background(), conversation.ASRRequest{})
		}
	}
	run(4, true)
	run(1, false)
	run(4, true)
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 0 {
		t.Fatalf("breaker state = %v, want closed (failures were not consecutive)", v)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	g, reg, _ := newTestGuard(t, GuardConfig{MaxConcurrency: 2})
	var active, peak atomic.Int32
	release := make(chan struct{})
	asr := g.ASR(&stubASR{fn: func(context.Context) error {
		n := active.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		<-release
		active.Add(-1)
		return nil
	}})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = asr.Transcribe(context.Background(), conversation.ASRRequest{}) }()
	}
	eventually(t, "two calls running", func() bool { return active.Load() == 2 })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller: err = %v, want its own deadline", err)
	}
	close(release)
	wg.Wait()
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak.Load())
	}
	if v := metricValue(t, reg, "shiksha_provider_requests_total", "outcome", "canceled"); v != 1 {
		t.Fatalf("gave-up-waiting calls counted as %v canceled, want 1", v)
	}
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 0 {
		t.Fatalf("breaker state = %v", v)
	}
}

// stubLLM streams the given deltas, then err if non-nil.
type stubLLM struct {
	deltas []string
	err    error
}

func (s stubLLM) Stream(ctx context.Context, _ conversation.LLMRequest) iter.Seq2[conversation.Delta, error] {
	return func(yield func(conversation.Delta, error) bool) {
		for _, d := range s.deltas {
			if !yield(conversation.Delta{Text: d}, nil) {
				return
			}
		}
		if s.err != nil {
			yield(conversation.Delta{}, s.err)
		}
	}
}

func (s stubLLM) Complete(context.Context, conversation.LLMRequest) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return strings.Join(s.deltas, ""), nil
}

func TestStreamGuard(t *testing.T) {
	g, reg, _ := newTestGuard(t, GuardConfig{MaxConcurrency: 1})
	llm := g.LLM(stubLLM{deltas: []string{"a", "b", "c"}})

	for range llm.Stream(context.Background(), conversation.LLMRequest{}) {
		break // consumer stops early (barge-in)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got string
	for d, err := range llm.Stream(ctx, conversation.LLMRequest{}) { // needs the slot back
		if err != nil {
			t.Fatalf("second stream: %v (semaphore not released?)", err)
		}
		got += d.Text
	}
	if got != "abc" {
		t.Fatalf("second stream text = %q", got)
	}

	failing := g.LLM(stubLLM{deltas: []string{"a"}, err: errServer})
	var errs int
	for _, err := range failing.Stream(context.Background(), conversation.LLMRequest{}) {
		if err != nil {
			errs++
			if !errors.Is(err, errServer) {
				t.Fatalf("stream err = %v", err)
			}
		}
	}
	if errs != 1 {
		t.Fatalf("errors yielded = %d, want 1", errs)
	}
	for outcome, want := range map[string]float64{"canceled": 1, "ok": 1, "error": 1} {
		if v := metricValue(t, reg, "shiksha_provider_requests_total", "op", "llm_stream", "outcome", outcome); v != want {
			t.Errorf("llm_stream %s = %v, want %v", outcome, v, want)
		}
	}
}

func TestMetricsCanBeCreatedTwiceOnOneRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	a, b := NewMetrics(reg), NewMetrics(reg)
	NewGuard(GuardConfig{Provider: "x"}, a).count("asr", outcomeOK)
	NewGuard(GuardConfig{Provider: "x"}, b).count("asr", outcomeOK)
	if v := metricValue(t, reg, "shiksha_provider_requests_total", "provider", "x", "outcome", "ok"); v != 2 {
		t.Fatalf("count = %v, want 2 (shared collectors)", v)
	}
}

func TestNewHTTPClient(t *testing.T) {
	c := NewHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.MaxIdleConnsPerHost != 32 || tr.IdleConnTimeout != 90*time.Second || tr.TLSHandshakeTimeout != 5*time.Second || !tr.ForceAttemptHTTP2 {
		t.Fatalf("transport = %+v", c.Transport)
	}
}

func TestPanicDoesNotLeakSlotOrWedgeProbe(t *testing.T) {
	g, reg, clock := newTestGuard(t, GuardConfig{MaxConcurrency: 1, FailureThreshold: 1})
	stub := &stubASR{fn: func(context.Context) error { panic("provider bug") }}
	asr := g.ASR(stub)
	call := func() (panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_, _ = asr.Transcribe(context.Background(), conversation.ASRRequest{})
		return false
	}
	if !call() {
		t.Fatal("the panic was swallowed; it must propagate")
	}
	if v := metricValue(t, reg, "shiksha_breaker_state", "provider", "p"); v != 2 {
		t.Fatalf("breaker state = %v, want 2 (a panic counts as a failure)", v)
	}
	clock.Advance(15 * time.Second)
	if !call() { // the half-open probe panics too
		t.Fatal("the probe's panic was swallowed")
	}
	clock.Advance(15 * time.Second)
	stub.fn = func(context.Context) error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := asr.Transcribe(ctx, conversation.ASRRequest{}); err != nil {
		t.Fatalf("after two panics: err = %v (a slot or the probe leaked)", err)
	}
}

func TestStreamConsumerPanicReleasesSlot(t *testing.T) {
	g, _, _ := newTestGuard(t, GuardConfig{MaxConcurrency: 1})
	llm := g.LLM(stubLLM{deltas: []string{"a", "b"}})
	func() {
		defer func() { _ = recover() }()
		for range llm.Stream(context.Background(), conversation.LLMRequest{}) {
			panic("consumer bug")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, err := range llm.Stream(ctx, conversation.LLMRequest{}) {
		if err != nil {
			t.Fatalf("second stream: %v (slot leaked after a consumer panic)", err)
		}
	}
}
