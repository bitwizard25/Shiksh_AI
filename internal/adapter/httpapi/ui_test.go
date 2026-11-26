package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/httpapi"
)

func TestStudentUIServedAtRoot(t *testing.T) {
	t.Parallel()
	h := httpapi.New(httpapi.Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "Shiksha AI") {
		t.Fatalf("expected Shiksha AI student UI HTML")
	}
}
