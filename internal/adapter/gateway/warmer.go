package gateway

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Warmer keeps TLS connections to the provider hosts warm. Touch, called when a learner starts
// speaking, sends a background HEAD to every origin not warmed in the last 45 s, so the ASR, LLM
// and TTS calls a moment later reuse a live connection instead of paying for a TLS handshake.
type Warmer struct {
	client    *http.Client
	targets   func() []string
	idleAfter time.Duration
	now       func() time.Time

	mu       sync.Mutex
	last     map[string]time.Time // origin -> when its last warm-up started
	inflight sync.WaitGroup
}

// NewWarmer returns a warmer for the origins of the URLs targets returns (evaluated on each Touch,
// so endpoints resolved later are included). A nil *Warmer is a valid no-op.
func NewWarmer(client *http.Client, targets func() []string) *Warmer {
	return &Warmer{client: client, targets: targets, idleAfter: 45 * time.Second, now: time.Now, last: map[string]time.Time{}}
}

// Touch starts warm-ups in the background and never blocks.
func (w *Warmer) Touch() {
	if w == nil || w.targets == nil {
		return
	}
	now := w.now()
	for _, target := range w.targets() {
		u, err := url.Parse(target)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		w.mu.Lock()
		if t, ok := w.last[origin]; ok && now.Sub(t) < w.idleAfter {
			w.mu.Unlock()
			continue
		}
		w.last[origin] = now
		w.mu.Unlock()
		w.inflight.Add(1)
		go w.head(origin)
	}
}

// Wait blocks until in-flight warm-ups finish.
func (w *Warmer) Wait() {
	if w != nil {
		w.inflight.Wait()
	}
}

func (w *Warmer) head(origin string) {
	defer w.inflight.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, origin+"/", nil)
	if err != nil {
		return
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return // best effort: the real call will dial if this failed
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
