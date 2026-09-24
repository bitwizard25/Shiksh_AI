package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func adminGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAdminEndpoints(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	h := NewAdminHandler(fakePinger{}, &ready)

	if rec := adminGet(t, h, "/healthz"); rec.Code != 200 {
		t.Errorf("/healthz = %d", rec.Code)
	}
	if rec := adminGet(t, h, "/readyz"); rec.Code != 200 {
		t.Errorf("/readyz (healthy) = %d", rec.Code)
	}
	if rec := adminGet(t, h, "/metrics"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Errorf("/metrics = %d", rec.Code)
	}

	ready.Store(false)
	if rec := adminGet(t, h, "/readyz"); rec.Code != 503 {
		t.Errorf("/readyz while shutting down = %d, want 503", rec.Code)
	}
	ready.Store(true)
	if rec := adminGet(t, NewAdminHandler(fakePinger{err: errors.New("down")}, &ready), "/readyz"); rec.Code != 503 {
		t.Errorf("/readyz with DB down = %d, want 503", rec.Code)
	}
}
