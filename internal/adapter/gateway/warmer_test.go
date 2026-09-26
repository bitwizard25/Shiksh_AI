package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWarmerHeadsEachIdleOriginOnce(t *testing.T) {
	var heads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			heads.Add(1)
		}
		w.WriteHeader(http.StatusNotFound) // any answer warms the connection
	}))
	defer srv.Close()

	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	w := NewWarmer(srv.Client(), func() []string { return []string{srv.URL + "/a", srv.URL + "/b?x=1", "::not a url"} })
	w.now = clock.Now

	w.Touch()
	w.Touch()
	w.Wait()
	if n := heads.Load(); n != 1 {
		t.Fatalf("HEADs = %d, want 1 (one origin, recently warmed)", n)
	}
	clock.Advance(46 * time.Second)
	w.Touch()
	w.Wait()
	if n := heads.Load(); n != 2 {
		t.Fatalf("HEADs = %d, want 2 after the idle window", n)
	}

	var nilWarmer *Warmer
	nilWarmer.Touch() // fake providers: no warmer, no panic
	nilWarmer.Wait()
	NewWarmer(srv.Client(), nil).Touch()
}
