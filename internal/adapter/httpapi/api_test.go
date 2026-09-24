package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/crypto"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/ratelimit"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

type captureMailer struct {
	mu   sync.Mutex
	sent []usecase.Message
}

func (c *captureMailer) Send(_ context.Context, m usecase.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func (c *captureMailer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *captureMailer) lastBody(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		t.Fatal("no email was sent")
	}
	return c.sent[len(c.sent)-1].Body
}

type testServer struct {
	*httptest.Server
	mailer *captureMailer
}

// newTestServer wires the real stack: Postgres repositories, TxManager, crypto and the use cases.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	hasher := crypto.NewArgon2Hasher(4)
	m := &captureMailer{}
	auth := usecase.NewAuth(usecase.AuthDeps{
		Users: users, Tokens: tokens, Tx: database.NewTxManager(pool), Hasher: hasher,
		Access: crypto.NewJWTIssuer(strings.Repeat("k", 32), 15*time.Minute), Opaque: crypto.Opaque{}, Mailer: m,
	}, usecase.AuthConfig{RefreshTTL: 720 * time.Hour, RefreshReuseGrace: 20 * time.Second, ResetTTL: 30 * time.Minute, AppBaseURL: "https://app.example"})
	limits := DefaultLimits(func(interval time.Duration, burst int) RateLimiter { return ratelimit.NewMemory(interval, burst) })
	srv := httptest.NewServer(New(Options{
		Auth: auth, Accounts: usecase.NewAccounts(users, hasher, nil), Limits: limits,
		Log: discardLog, AllowedOrigins: []string{"https://app.example"},
	}))
	t.Cleanup(srv.Close)
	return &testServer{Server: srv, mailer: m}
}

// do sends a request (body may be a string for raw bodies, or any value to JSON-encode) with an
// optional raw Authorization header, decodes a JSON response into out when out is non-nil, and
// returns the status code.
func (s *testServer) do(t *testing.T, method, path, authorization string, body, out any) int {
	t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, s.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s (status %d): %v", method, path, resp.StatusCode, err)
		}
	}
	return resp.StatusCode
}

func registerBody(email string) map[string]any {
	return map[string]any{"email": email, "password": "correct horse", "display_name": "Asha", "terms_accepted": true}
}

func (s *testServer) register(t *testing.T, email string) authResponse {
	t.Helper()
	var reg authResponse
	if code := s.do(t, "POST", "/v1/auth/register", "", registerBody(email), &reg); code != http.StatusCreated {
		t.Fatalf("register status = %d", code)
	}
	return reg
}

func TestAccountLifecycle(t *testing.T) {
	s := newTestServer(t)
	reg := s.register(t, "asha@example.com")
	bearer := "Bearer " + reg.AccessToken
	if reg.User.Email != "asha@example.com" || reg.TokenType != "Bearer" || reg.ExpiresIn != 900 {
		t.Fatalf("register response = %+v", reg)
	}

	var me userResponse
	if code := s.do(t, "GET", "/v1/me", bearer, nil, &me); code != 200 || me.ID != reg.User.ID {
		t.Fatalf("GET /v1/me = %d %+v", code, me)
	}
	if code := s.do(t, "PATCH", "/v1/me", bearer, map[string]any{"grade": 6, "preferred_lang": "ta"}, &me); code != 200 || me.PreferredLang != "ta" || me.Grade == nil || *me.Grade != 6 {
		t.Fatalf("PATCH /v1/me = %d %+v", code, me)
	}
	var unchanged userResponse
	if code := s.do(t, "PATCH", "/v1/me", bearer, map[string]any{}, &unchanged); code != 200 || unchanged.PreferredLang != "ta" || *unchanged.Grade != 6 {
		t.Fatalf("empty PATCH = %d %+v, want unchanged", code, unchanged)
	}

	var tokens tokenResponse
	if code := s.do(t, "POST", "/v1/auth/refresh", "", map[string]string{"refresh_token": reg.RefreshToken}, &tokens); code != 200 || tokens.RefreshToken == reg.RefreshToken {
		t.Fatalf("refresh = %d %+v", code, tokens)
	}
	if code := s.do(t, "POST", "/v1/auth/logout", "", map[string]string{"refresh_token": tokens.RefreshToken}, nil); code != 204 {
		t.Fatalf("logout = %d", code)
	}
	var e envelope
	if code := s.do(t, "POST", "/v1/auth/refresh", "", map[string]string{"refresh_token": tokens.RefreshToken}, &e); code != 401 || e.Error.Code != "invalid_token" {
		t.Fatalf("refresh after logout = %d %+v", code, e)
	}

	if code := s.do(t, "DELETE", "/v1/me", bearer, map[string]string{"password": "wrong horse"}, &e); code != 401 || e.Error.Code != "invalid_credentials" {
		t.Fatalf("delete with wrong password = %d %+v", code, e)
	}
	if code := s.do(t, "DELETE", "/v1/me", bearer, map[string]string{"password": "correct horse"}, nil); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code := s.do(t, "GET", "/v1/me", bearer, nil, &e); code != 404 {
		t.Fatalf("GET /v1/me after delete = %d", code)
	}
}

func TestRegisterErrors(t *testing.T) {
	s := newTestServer(t)
	s.register(t, "asha@example.com")

	var e envelope
	if code := s.do(t, "POST", "/v1/auth/register", "", registerBody("ASHA@example.com"), &e); code != 409 || e.Error.Code != "email_taken" {
		t.Errorf("duplicate = %d %+v", code, e)
	}
	short := registerBody("x@example.com")
	short["password"] = "short"
	if code := s.do(t, "POST", "/v1/auth/register", "", short, &e); code != 400 || e.Error.Code != "validation_failed" || e.Error.Field != "password" {
		t.Errorf("short password = %d %+v", code, e)
	}
	unknown := registerBody("y@example.com")
	unknown["is_admin"] = true
	if code := s.do(t, "POST", "/v1/auth/register", "", unknown, &e); code != 400 || e.Error.Code != "bad_request" {
		t.Errorf("unknown field = %d %+v", code, e)
	}
	wrongType := `{"email":"z@example.com","password":"correct horse","display_name":"Z","terms_accepted":true,"grade":"5"}`
	if code := s.do(t, "POST", "/v1/auth/register", "", wrongType, &e); code != 400 || !strings.Contains(e.Error.Message, "grade") {
		t.Errorf("wrong type = %d %+v", code, e)
	}
	if code := s.do(t, "POST", "/v1/auth/register", "", "", &e); code != 400 || e.Error.Code != "bad_request" {
		t.Errorf("empty body = %d %+v", code, e)
	}
	if code := s.do(t, "POST", "/v1/auth/register", "", "not json", &e); code != 400 {
		t.Errorf("non-JSON body = %d %+v", code, e)
	}
}

func TestLoginErrorsAndRateLimit(t *testing.T) {
	s := newTestServer(t)
	s.register(t, "asha@example.com")

	var e envelope
	for attempt := 1; attempt <= 5; attempt++ {
		if code := s.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "asha@example.com", "password": "wrong horse"}, &e); code != 401 || e.Error.Code != "invalid_credentials" {
			t.Fatalf("attempt %d = %d %+v", attempt, code, e)
		}
	}
	if code := s.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "ASHA@example.com", "password": "correct horse"}, &e); code != 429 || e.Error.Code != "rate_limited" {
		t.Fatalf("6th attempt = %d %+v, want 429 rate_limited", code, e)
	}
}

func TestAuthHeaderHandling(t *testing.T) {
	s := newTestServer(t)
	reg := s.register(t, "asha@example.com")
	for _, h := range []string{"Bearer " + reg.AccessToken, "bearer " + reg.AccessToken, "Bearer   " + reg.AccessToken + " "} {
		if code := s.do(t, "GET", "/v1/me", h, nil, nil); code != 200 {
			t.Errorf("Authorization %q -> %d, want 200", h, code)
		}
	}
	var e envelope
	for _, h := range []string{"", "Basic abc", "Bearer", "Bearer not-a-jwt"} {
		if code := s.do(t, "GET", "/v1/me", h, nil, &e); code != 401 || e.Error.Code != "invalid_token" {
			t.Errorf("Authorization %q -> %d %+v, want 401 invalid_token", h, code, e)
		}
	}
}

func TestPasswordResetOverHTTP(t *testing.T) {
	s := newTestServer(t)
	s.register(t, "asha@example.com")

	if code := s.do(t, "POST", "/v1/auth/password/forgot", "", map[string]string{"email": "asha@example.com"}, nil); code != 202 {
		t.Fatalf("forgot = %d", code)
	}
	m := regexp.MustCompile(`/reset\?token=([A-Za-z0-9_-]+)`).FindStringSubmatch(s.mailer.lastBody(t))
	if m == nil {
		t.Fatal("no reset link in the email")
	}
	token := m[1]

	if code := s.do(t, "POST", "/v1/auth/password/reset", "", map[string]string{"token": token, "new_password": "brand new pass"}, nil); code != 204 {
		t.Fatalf("reset = %d", code)
	}
	if code := s.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "asha@example.com", "password": "brand new pass"}, nil); code != 200 {
		t.Fatalf("login with new password = %d", code)
	}
	var e envelope
	if code := s.do(t, "POST", "/v1/auth/password/reset", "", map[string]string{"token": token, "new_password": "another pass 1"}, &e); code != 401 || e.Error.Code != "invalid_token" {
		t.Fatalf("reused reset token = %d %+v", code, e)
	}
	before := s.mailer.count()
	if code := s.do(t, "POST", "/v1/auth/password/forgot", "", map[string]string{"email": "nobody@example.com"}, nil); code != 202 || s.mailer.count() != before {
		t.Fatalf("forgot for unknown email = %d, mails %d -> %d", code, before, s.mailer.count())
	}
}

func TestLanguages(t *testing.T) {
	s := newTestServer(t)
	var langs []languageResponse
	if code := s.do(t, "GET", "/v1/languages", "", nil, &langs); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if len(langs) != 9 || langs[0].Code != "hi" || langs[0].NativeName != "हिन्दी" {
		t.Fatalf("languages = %+v", langs)
	}
}
