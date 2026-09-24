// Package ratelimit provides an in-memory keyed token-bucket limiter.
package ratelimit

import (
	"container/list"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxKeys bounds the limiter's memory: at most this many distinct keys are tracked at once.
const maxKeys = 20_000

// Memory is a keyed token bucket kept in process memory, so limits apply per instance.
// A shared (Redis) limiter can replace it behind the same interface. Keys are bounded by an
// LRU eviction policy: at capacity, the least-recently-used key is dropped. An evicted bucket
// simply starts full again on its next use, since rate.Limiter refills idle buckets anyway.
type Memory struct {
	mu    sync.Mutex
	every rate.Limit
	burst int
	items map[string]*list.Element
	order *list.List // front = most recently used
	now   func() time.Time
}

type bucket struct {
	key string
	lim *rate.Limiter
}

// NewMemory allows `burst` events at once, refilling one event every `interval`.
func NewMemory(interval time.Duration, burst int) *Memory {
	return &Memory{
		every: rate.Every(interval),
		burst: burst,
		items: make(map[string]*list.Element),
		order: list.New(),
		now:   time.Now,
	}
}

// Allow reports whether one more event for key is allowed now.
func (l *Memory) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if el, ok := l.items[key]; ok {
		l.order.MoveToFront(el)
		return el.Value.(*bucket).lim.AllowN(now, 1)
	}
	if len(l.items) >= maxKeys {
		oldest := l.order.Back()
		if oldest != nil {
			l.order.Remove(oldest)
			delete(l.items, oldest.Value.(*bucket).key)
		}
	}
	b := &bucket{key: key, lim: rate.NewLimiter(l.every, l.burst)}
	el := l.order.PushFront(b)
	l.items[key] = el
	return b.lim.AllowN(now, 1)
}
