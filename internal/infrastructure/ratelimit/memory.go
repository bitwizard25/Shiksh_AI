// Package ratelimit provides an in-memory keyed token-bucket limiter.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	idleTTL   = time.Hour // must exceed the slowest full refill (forgot-password: 3/hour)
	sweepSize = 10_000    // sweep idle buckets once the map grows this large
)

// Memory is a keyed token bucket kept in process memory, so limits apply per instance.
// A shared (Redis) limiter can replace it behind the same interface.
type Memory struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewMemory allows `burst` events at once, refilling one event every `interval`.
func NewMemory(interval time.Duration, burst int) *Memory {
	return &Memory{every: rate.Every(interval), burst: burst, buckets: make(map[string]*bucket), now: time.Now}
}

// Allow reports whether one more event for key is allowed now.
func (l *Memory) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= sweepSize {
			l.sweep(now)
		}
		b = &bucket{lim: rate.NewLimiter(l.every, l.burst)}
		l.buckets[key] = b
	}
	b.seen = now
	return b.lim.AllowN(now, 1)
}

func (l *Memory) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.seen) > idleTTL {
			delete(l.buckets, k)
		}
	}
}
