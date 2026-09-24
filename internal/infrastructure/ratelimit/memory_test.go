package ratelimit

import (
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
