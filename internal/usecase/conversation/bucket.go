package conversation

import "time"

// bucket is a token bucket guarding against runaway provider spend. It is used by one goroutine.
type bucket struct {
	tokens   float64
	burst    float64
	perToken time.Duration
	last     time.Time
}

func newBucket(burst int, perMinute int, now time.Time) *bucket {
	return &bucket{tokens: float64(burst), burst: float64(burst), perToken: time.Minute / time.Duration(max(perMinute, 1)), last: now}
}

// allow takes a token if one is available at now.
func (b *bucket) allow(now time.Time) bool {
	b.tokens = min(b.burst, b.tokens+float64(now.Sub(b.last))/float64(b.perToken))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
