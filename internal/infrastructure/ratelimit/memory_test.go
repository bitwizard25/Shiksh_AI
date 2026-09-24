package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestMemoryAllowsBurstThenRefills(t *testing.T) {
	now := time.Unix(1_000, 0)
	l := NewMemory(time.Minute, 2)
	l.now = func() time.Time { return now }
	if !l.Allow("k") || !l.Allow("k") {
		t.Fatal("burst of 2 not allowed")
	}
	if l.Allow("k") {
		t.Fatal("third request allowed inside the window")
	}
	if !l.Allow("other") {
		t.Fatal("independent key was limited")
	}
	now = now.Add(time.Minute)
	if !l.Allow("k") {
		t.Fatal("token not refilled after one interval")
	}
}

func TestMemoryIsBoundedLRU(t *testing.T) {
	l := NewMemory(time.Minute, 2)
	for i := 0; i < 3*maxKeys; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if len(l.items) > maxKeys {
		t.Fatalf("len(l.items) = %d, want <= %d", len(l.items), maxKeys)
	}

	// A recently-used key survives filling the map back up to capacity.
	recent := NewMemory(time.Minute, 2)
	if !recent.Allow("a") || !recent.Allow("a") {
		t.Fatal("burst of 2 for 'a' not allowed")
	}
	if recent.Allow("a") {
		t.Fatal("third request for 'a' should be limited")
	}
	recent.Allow("a") // touch again, moving it to the front of the LRU
	for i := 0; i < maxKeys-1; i++ {
		recent.Allow(fmt.Sprintf("other%d", i))
	}
	if recent.Allow("a") {
		t.Fatal("'a' should still be limited: it was recently used and must not have been evicted")
	}

	// A key untouched through maxKeys newer inserts is evicted and allowed again.
	stale := NewMemory(time.Minute, 2)
	if !stale.Allow("stale") || !stale.Allow("stale") {
		t.Fatal("burst of 2 for 'stale' not allowed")
	}
	if stale.Allow("stale") {
		t.Fatal("third request for 'stale' should be limited")
	}
	for i := 0; i < maxKeys; i++ {
		stale.Allow(fmt.Sprintf("fresh%d", i))
	}
	if !stale.Allow("stale") {
		t.Fatal("'stale' should have been evicted and its bucket start full again")
	}
}
