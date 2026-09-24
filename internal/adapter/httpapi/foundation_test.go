package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type envelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Field     string `json:"field"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.NewDecoder(rec.Body).Decode(&e); err != nil {
		t.Fatalf("decode error envelope: %v (body %q)", err, rec.Body.String())
	}
	return e
}

func TestDecodeJSON(t *testing.T) {
	type target struct {
		Name  string `json:"name"`
		Grade *int   `json:"grade"`
	}
	cases := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		wantInMsg  string
	}{
		{"valid", `{"name":"a","grade":3}`, true, 0, ""},
		{"empty body", ``, false, 400, "required"},
		{"not json", `hello`, false, 400, "invalid JSON"},
		{"unknown field", `{"name":"a","admin":true}`, false, 400, "unknown field"},
		{"wrong type", `{"grade":"5"}`, false, 400, `"grade"`},
		{"trailing data", `{"name":"a"} {"name":"b"}`, false, 400, "single JSON object"},
		{"too large", `{"name":"` + strings.Repeat("a", 1<<20) + `"}`, false, 413, "1 MB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			var dst target
			ok := decodeJSON(rec, req, &dst)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (body %q)", ok, tc.wantOK, rec.Body.String())
			}
			if tc.wantOK {
				return
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if e := decodeEnvelope(t, rec); !strings.Contains(e.Error.Message, tc.wantInMsg) {
				t.Fatalf("message = %q, want it to contain %q", e.Error.Message, tc.wantInMsg)
			}
		})
	}
}

func TestServiceErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
		field  string
	}{
		{&entity.ValidationError{Field: "email", Message: "bad"}, 400, "validation_failed", "email"},
		{entity.ErrEmailTaken, 409, "email_taken", ""},
		{entity.ErrInvalidCredentials, 401, "invalid_credentials", ""},
		{entity.ErrTokenInvalid, 401, "invalid_token", ""},
		{entity.ErrNotFound, 404, "not_found", ""},
		{errors.New("db exploded"), 500, "internal", ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		serviceError(discardLog, rec, httptest.NewRequest(http.MethodGet, "/", nil), tc.err)
		e := decodeEnvelope(t, rec)
		if rec.Code != tc.status || e.Error.Code != tc.code || e.Error.Field != tc.field {
			t.Errorf("%v -> %d %q field %q; want %d %q field %q", tc.err, rec.Code, e.Error.Code, e.Error.Field, tc.status, tc.code, tc.field)
		}
	}
}

func TestErrorEnvelopeAlwaysCarriesRequestID(t *testing.T) {
	// Behind withRequestID the id is present and non-empty.
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusBadRequest, "bad_request", "nope")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if e := decodeEnvelope(t, rec); e.Error.RequestID == "" || e.Error.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("request_id = %q, header = %q; want the same non-empty id", e.Error.RequestID, rec.Header().Get("X-Request-ID"))
	}
	// Even without the middleware the key is emitted, so a wiring bug is visible, not silent.
	rec = httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusBadRequest, "bad_request", "nope")
	if !strings.Contains(rec.Body.String(), `"request_id":""`) {
		t.Fatalf("body %q lacks an explicit request_id key", rec.Body.String())
	}
}

func TestRequestID(t *testing.T) {
	var seen string
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = requestIDFrom(r.Context()) }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "client-id-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen != "client-id-123" || rec.Header().Get("X-Request-ID") != "client-id-123" {
		t.Fatalf("valid incoming id not kept: ctx %q, header %q", seen, rec.Header().Get("X-Request-ID"))
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "bad id with spaces")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(seen) {
		t.Fatalf("invalid incoming id not replaced, got %q", seen)
	}
}

func TestRecoverPanics(t *testing.T) {
	h := recoverPanics(discardLog)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 500 || decodeEnvelope(t, rec).Error.Code != "internal" {
		t.Fatalf("status %d body %q, want 500 internal", rec.Code, rec.Body.String())
	}
}

func TestCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := cors([]string{"https://app.example"})(next)

	pre := httptest.NewRequest(http.MethodOptions, "/v1/me", nil)
	pre.Header.Set("Origin", "https://app.example")
	pre.Header.Set("Access-Control-Request-Method", "PATCH")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example" ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight: status %d headers %v", rec.Code, rec.Header())
	}

	other := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	other.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, other)
	if rec.Code != http.StatusTeapot || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin: status %d ACAO %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]struct {
		token string
		ok    bool
	}{
		"Bearer abc":     {"abc", true},
		"bearer abc":     {"abc", true},
		"Bearer   abc  ": {"abc", true},
		"Basic abc":      {"", false},
		"Bearer":         {"", false},
		"Bearer    ":     {"", false},
		"":               {"", false},
	}
	for header, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		got, ok := bearerToken(r)
		if got != want.token || ok != want.ok {
			t.Errorf("bearerToken(%q) = %q, %v; want %q, %v", header, got, ok, want.token, want.ok)
		}
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:5555"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9")
	if got := clientIP(r, false); got != "10.0.0.5" {
		t.Errorf("untrusted proxy: got %q, want 10.0.0.5", got)
	}
	if got := clientIP(r, true); got != "203.0.113.9" {
		t.Errorf("trusted proxy: got %q, want the address the proxy appended (203.0.113.9)", got)
	}
}

func TestAccessLogOmitsQueryString(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := accessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/ws?ticket=super-secret", nil))
	out := buf.String()
	if !strings.Contains(out, "/v1/ws") || !strings.Contains(out, "status=202") || strings.Contains(out, "super-secret") {
		t.Fatalf("access log = %q", out)
	}
}

func TestDefaultLimitsFillsEveryLimiter(t *testing.T) {
	type spec struct {
		interval string
		burst    int
	}
	var got []spec
	l := DefaultLimits(func(interval time.Duration, burst int) RateLimiter {
		got = append(got, spec{interval.String(), burst})
		return allowAll{}
	})
	for name, lim := range map[string]RateLimiter{"Register": l.Register, "Login": l.Login, "LoginIP": l.LoginIP, "Refresh": l.Refresh, "Forgot": l.Forgot, "ForgotIP": l.ForgotIP, "DeleteAccount": l.DeleteAccount} {
		if lim == nil {
			t.Errorf("%s limiter is nil", name)
		}
	}
	want := []spec{{"6s", 10}, {"12s", 5}, {"1s", 60}, {"2s", 30}, {"20m0s", 3}, {"6s", 10}, {"12s", 5}}
	if len(got) != len(want) {
		t.Fatalf("created %d limiters, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("limiter %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

type allowAll struct{}

func (allowAll) Allow(string) bool { return true }
