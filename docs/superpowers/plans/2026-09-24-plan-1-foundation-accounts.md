# Plan 1: Foundation + Accounts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Go module skeleton, the Postgres persistence layer and a complete, tested accounts API: register, login, rotating refresh, logout, password reset, profile and account deletion. Also add the admin endpoints and a runnable server.

**Architecture:**
- A single Go binary on stdlib `net/http`.
- Hand-written SQL on `pgx/v5`, with goose migrations embedded in the binary.
- argon2id passwords, HS256 access JWTs, and opaque refresh tokens stored as SHA-256 that rotate with reuse detection.
- Tests run against a **real Postgres started by `embedded-postgres`**. Docker isn't installed on the dev machine, so this keeps the DB tests running instead of being skipped.

**Tech Stack:** Go 1.27, `github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, `github.com/fergusstrange/embedded-postgres`, `github.com/caarlos0/env/v11`, `github.com/golang-jwt/jwt/v5`, `golang.org/x/crypto/argon2`, `golang.org/x/time/rate`, `github.com/google/uuid`, `github.com/prometheus/client_golang`.

**Spec:** `docs/design.md`. This plan implements §16 Phase 0 and Phase 1: §4, §5, §6, §7 (auth, me and languages rows), and the admin/lifecycle parts of §14. **This is Plan 1 of 5.** Plans 2–5 (providers, sessions + tutor, realtime core, latency polish + hardening) are written after this one lands. The full schema, including the session tables, is created here. The session repositories that use it (ticket redemption, epoch-fenced message writes, `ClaimStale`) and `GET /v1/subjects` belong to Plan 3, next to the session code that calls them.

## Global Constraints

- Module path: `github.com/bitwizard25/Shiksh_AI`. Go 1.27.
- Server binary builds with `CGO_ENABLED=0`. Tests run with `go test -race ./...` (CGO + mingw gcc are available locally).
- HTTP: stdlib `net/http` ServeMux with method patterns. No web framework.
- SQL: hand-written with `pgx/v5`; migrations with goose v3 via an embedded FS; no ORM, no sqlc.
- Passwords: argon2id, m=19 MiB (19456 KiB), t=2, p=1, 16-byte salt, 32-byte key, PHC string. Concurrent hashes are limited by a semaphore sized 2×NumCPU.
- Access JWT: HS256, `iss=shiksha-ai`, `aud=api`, TTL 15m. `JWT_SECRET` must be ≥ 32 bytes.
- Refresh token: 32 random bytes, base64url, stored as sha256, TTL 720h, rotated on every use with a **20 s reuse grace**.
- Password reset token: 32 random bytes, stored as sha256, 30 min TTL, single use. A reset revokes all of the user's refresh tokens.
- Passwords are 8–128 **runes**; display names are 1–80 runes; emails are ≤254 bytes and normalized with trim + lower.
- Error envelope: `{"error":{"code":"…","message":"…","field":"…?","request_id":"…"}}`.
- JSON bodies: 1 MB cap, `DisallowUnknownFields`, exactly one JSON object.
- Rate limits (per instance, in memory):
  - register: per IP 10/min
  - login: per (IP, email) 5/min, plus per IP 60/min
  - refresh: per IP 30/min
  - forgot and reset password: per email 3/hour, plus per IP 10/min
- Logs: slog JSON. Access logs record the path only, **never the query string**. Passwords and tokens are never logged.
- Every commit message ends with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Before each commit, `gofmt -l .` must print nothing and `go vet ./...` must pass.

## Review Focus

These are inputs and failure modes the spec implies but that no task would otherwise test. Each one has a test in the task that owns it.
1. **Email typed differently at login** (`" ASHA@Example.com "` after registering `asha@example.com`) must log into the same account → Task 8 `TestLogin`.
2. **Non-Latin text** (Devanagari names and passwords) must be length-checked in runes, not bytes, so a 7-character Hindi password (21 bytes) is rejected and a 9-character one is accepted → Task 8 `TestRegisterValidation`, `TestRegisterAcceptsDevanagariNameAndPassword`.
3. **Malformed bodies** (empty body, non-JSON, a wrong-typed field like `"grade":"5"`) must return 400 with a readable message, never 500 → Task 9 `TestDecodeJSON`, Task 10 `TestRegisterErrors`.
4. **`PATCH /v1/me` with `{}`** must return 200 with the profile unchanged → Task 8 `TestUpdateProfile`, Task 10 `TestAccountLifecycle`.
5. **Authorization header variants** (lowercase `bearer`, extra spaces) must authenticate; `Basic …`, bare `Bearer` and junk tokens must get 401 → Task 9 `TestBearerToken`, Task 10 `TestAuthHeaderHandling`.

---

## File Structure

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | module + dependencies |
| `.gitignore`, `.dockerignore`, `.env.example` | repo hygiene, sample config |
| `internal/config/config.go` | env → `Config`, validation, `.env` loader |
| `internal/domain/errors.go`, `user.go` | shared sentinel errors, `ValidationError`, `User` |
| `internal/lang/registry.go` | supported tutoring languages |
| `internal/store/store.go` | pool setup with retry, `Store` aggregate, transactions |
| `internal/store/migrate.go`, `migrations/00001_init.sql` | embedded goose migrations (full schema from spec §6) |
| `internal/store/errors.go` | Postgres error helpers |
| `internal/store/users.go` | users repository |
| `internal/store/tokens.go`, `auth_tx.go` | refresh/reset token repository + transactional rotate/reset |
| `internal/store/storetest/storetest.go` | embedded Postgres + per-test databases cloned from a migrated template |
| `internal/auth/password.go` | argon2id hasher |
| `internal/auth/tokens.go` | JWT issuer + opaque tokens |
| `internal/auth/validate.go` | input validation |
| `internal/auth/service.go` | account use cases |
| `internal/mail/mail.go` | `Mailer`, SMTP and log implementations |
| `internal/httpapi/respond.go` | JSON I/O, error envelope, error mapping |
| `internal/httpapi/middleware.go` | request id, access log, panic recovery, CORS, client IP, bearer parsing |
| `internal/httpapi/ratelimit.go` | keyed token-bucket limiter |
| `internal/httpapi/router.go` | public API wiring, auth guard |
| `internal/httpapi/auth_handlers.go`, `user_handlers.go` | endpoints |
| `internal/httpapi/admin.go` | `/healthz`, `/readyz`, `/metrics` |
| `cmd/server/main.go` | wiring + graceful shutdown |
| `cmd/devdb/main.go` | local Postgres for development without Docker |
| `Dockerfile`, `docker-compose.yml`, `README.md` | packaging + docs (with Upcoming Features) |

---

### Task 1: Module skeleton and configuration

**Files:**
- Create: `go.mod`, `.gitignore`, `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `config.Load() (config.Config, error)`
  - `config.LoadFrom(map[string]string) (config.Config, error)`
  - `config.LoadDotEnv(path string) error`
  - `(config.Config).SlogLevel() slog.Level`
  - `Config` fields: `HTTPAddr, AdminAddr, DatabaseURL, JWTSecret string; AccessTokenTTL, RefreshTokenTTL, ShutdownTimeout time.Duration; AllowedOrigins []string; TrustProxy bool; AppBaseURL, LogLevel string; SMTP config.SMTPConfig{Host string; Port int; User, Pass, From string}`

- [ ] **Step 1: Initialise the module and ignore files**

Run:
```bash
go mod init github.com/bitwizard25/Shiksh_AI
go get github.com/caarlos0/env/v11@latest
```

Create `.gitignore`:
```gitignore
# local runtime data and secrets
.devdata/
.env
out/
bin/
*.exe
*.test
coverage.out
```

- [ ] **Step 2: Write the failing tests**

Create `internal/config/config_test.go`:
```go
package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/config"
)

func baseEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://localhost/shiksha",
		"JWT_SECRET":   strings.Repeat("k", 32),
	}
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	cfg, err := config.LoadFrom(baseEnv())
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.AdminAddr != ":9090" {
		t.Errorf("addrs = %q, %q; want :8080, :9090", cfg.HTTPAddr, cfg.AdminAddr)
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Errorf("AccessTokenTTL = %v, want 15m", cfg.AccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != 720*time.Hour {
		t.Errorf("RefreshTokenTTL = %v, want 720h", cfg.RefreshTokenTTL)
	}
	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port = %d, want 587", cfg.SMTP.Port)
	}
	if cfg.SlogLevel() != slog.LevelInfo {
		t.Errorf("SlogLevel = %v, want info", cfg.SlogLevel())
	}
}

func TestLoadFromRequiresDatabaseURLAndSecret(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "JWT_SECRET"} {
		t.Run(key, func(t *testing.T) {
			env := baseEnv()
			delete(env, key)
			_, err := config.LoadFrom(env)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v, want it to mention %s", err, key)
			}
		})
	}
}

func TestLoadFromRejectsShortSecret(t *testing.T) {
	env := baseEnv()
	env["JWT_SECRET"] = "too-short"
	_, err := config.LoadFrom(env)
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("err = %v, want JWT_SECRET length error", err)
	}
}

func TestLoadFromRejectsUnknownLogLevel(t *testing.T) {
	env := baseEnv()
	env["LOG_LEVEL"] = "loud"
	_, err := config.LoadFrom(env)
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("err = %v, want LOG_LEVEL error", err)
	}
}

func TestLoadFromSplitsAndTrimsOrigins(t *testing.T) {
	env := baseEnv()
	env["ALLOWED_ORIGINS"] = "https://a.example, https://b.example,"
	cfg, err := config.LoadFrom(env)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	want := []string{"https://a.example", "https://b.example"}
	if !slices.Equal(cfg.AllowedOrigins, want) {
		t.Fatalf("AllowedOrigins = %q, want %q", cfg.AllowedOrigins, want)
	}
}

func TestLoadDotEnvDoesNotOverrideExistingVars(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nSHIKSHA_TEST_A=1\n\nSHIKSHA_TEST_B=\"two words\"\nexport SHIKSHA_TEST_C=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHIKSHA_TEST_C", "already-set")
	t.Cleanup(func() {
		os.Unsetenv("SHIKSHA_TEST_A")
		os.Unsetenv("SHIKSHA_TEST_B")
	})

	if err := config.LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	for key, want := range map[string]string{
		"SHIKSHA_TEST_A": "1",
		"SHIKSHA_TEST_B": "two words",
		"SHIKSHA_TEST_C": "already-set",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestLoadDotEnvMissingFileIsNotAnError(t *testing.T) {
	if err := config.LoadDotEnv(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Fatalf("LoadDotEnv(missing) = %v, want nil", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL, the build fails because `config.LoadFrom` and the other functions are undefined.

- [ ] **Step 4: Implement the config package**

Create `internal/config/config.go`:
```go
// Package config loads process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds runtime settings. Later plans add provider and voice settings.
type Config struct {
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	AdminAddr       string        `env:"ADMIN_ADDR" envDefault:":9090"`
	DatabaseURL     string        `env:"DATABASE_URL,required"`
	JWTSecret       string        `env:"JWT_SECRET,required"`
	AccessTokenTTL  time.Duration `env:"ACCESS_TOKEN_TTL" envDefault:"15m"`
	RefreshTokenTTL time.Duration `env:"REFRESH_TOKEN_TTL" envDefault:"720h"`
	AllowedOrigins  []string      `env:"ALLOWED_ORIGINS" envSeparator:","`
	TrustProxy      bool          `env:"TRUST_PROXY" envDefault:"false"`
	AppBaseURL      string        `env:"APP_BASE_URL" envDefault:"http://localhost:3000"`
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"20s"`
	SMTP            SMTPConfig
}

// SMTPConfig configures outgoing mail. An empty Host means mail is logged instead of sent.
type SMTPConfig struct {
	Host string `env:"SMTP_HOST"`
	Port int    `env:"SMTP_PORT" envDefault:"587"`
	User string `env:"SMTP_USER"`
	Pass string `env:"SMTP_PASS"`
	From string `env:"SMTP_FROM" envDefault:"Shiksha AI <no-reply@localhost>"`
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(envMap(os.Environ()))
}

// LoadFrom reads configuration from the given variables. Tests use it to avoid touching the process environment.
func LoadFrom(environ map[string]string) (Config, error) {
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environ}); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	cfg.AllowedOrigins = cleanList(cfg.AllowedOrigins)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks values that struct tags cannot express.
func (c Config) Validate() error {
	var errs []error
	if len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET must be at least 32 bytes"))
	}
	if c.AccessTokenTTL <= 0 || c.RefreshTokenTTL <= 0 {
		errs = append(errs, errors.New("ACCESS_TOKEN_TTL and REFRESH_TOKEN_TTL must be positive"))
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.LogLevel)); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL %q must be one of debug, info, warn, error", c.LogLevel))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// SlogLevel returns the parsed LOG_LEVEL. Validate guarantees it parses.
func (c Config) SlogLevel() slog.Level {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(c.LogLevel))
	return lvl
}

// LoadDotEnv sets variables from a KEY=VALUE file without overriding variables that are already set.
// A missing file is not an error. Blank lines and lines starting with # are ignored, an optional
// "export " prefix is accepted, and values may be wrapped in matching single or double quotes.
func LoadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for i, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, i+1)
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func envMap(kvs []string) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[k] = v
		}
	}
	return m
}

func cleanList(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/config/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/config`

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum .gitignore internal/config
git commit -m "feat(config): env-based configuration with .env loader" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Store foundation, schema migration and the embedded-Postgres test harness

**Files:**
- Create: `internal/store/store.go`, `internal/store/migrate.go`, `internal/store/migrations/00001_init.sql`, `internal/store/storetest/storetest.go`
- Test: `internal/store/main_test.go`, `internal/store/migrate_test.go`

**Interfaces:**
- Produces:
  - `store.Store{Pool *pgxpool.Pool}` (later tasks add fields)
  - `store.New(*pgxpool.Pool) *store.Store`
  - `store.Open(ctx, databaseURL string) (*store.Store, error)`
  - `(*Store).Ping(ctx) error`, `(*Store).Close()`
  - `store.Migrate(ctx, *pgxpool.Pool) error`
  - `store.DBTX` interface
  - `storetest.Main(*testing.M) int`, `storetest.NewStore(testing.TB) *store.Store`

- [ ] **Step 1: Add dependencies**

Run:
```bash
go get github.com/jackc/pgx/v5@latest github.com/pressly/goose/v3@latest github.com/fergusstrange/embedded-postgres@latest github.com/google/uuid@latest
```

- [ ] **Step 2: Write the migration (full schema from spec §6)**

Create `internal/store/migrations/00001_init.sql`:
```sql
-- +goose Up
CREATE TABLE users (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email               text NOT NULL,
    password_hash       text NOT NULL,
    display_name        text NOT NULL,
    preferred_lang      text NOT NULL DEFAULT 'hi',
    grade               smallint CHECK (grade BETWEEN 1 AND 12),
    terms_accepted_at   timestamptz NOT NULL,
    guardian_consent_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_uq ON users (lower(email));

CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id  uuid NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);

CREATE TABLE password_reset_tokens (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX password_reset_tokens_user_idx ON password_reset_tokens (user_id);

CREATE TABLE tutoring_sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject        text NOT NULL,
    language       text NOT NULL,
    grade          smallint,
    status         text NOT NULL DEFAULT 'created' CHECK (status IN ('created', 'active', 'ended')),
    conn_epoch     int NOT NULL DEFAULT 0,
    end_reason     text,
    summary        text,
    turn_count     int NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    last_active_at timestamptz,
    ended_at       timestamptz
);
CREATE INDEX tutoring_sessions_user_created_idx ON tutoring_sessions (user_id, created_at DESC);
CREATE INDEX tutoring_sessions_summary_idx ON tutoring_sessions (user_id, subject, ended_at DESC) WHERE summary IS NOT NULL;
CREATE INDEX tutoring_sessions_stale_idx ON tutoring_sessions ((coalesce(last_active_at, created_at))) WHERE status <> 'ended';

CREATE TABLE ws_tickets (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES tutoring_sessions(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE TABLE messages (
    id         bigserial PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES tutoring_sessions(id) ON DELETE CASCADE,
    turn_no    int NOT NULL,
    role       text NOT NULL CHECK (role IN ('learner', 'tutor')),
    input_mode text CHECK (input_mode IN ('voice', 'text')),
    content    text NOT NULL,
    status     text NOT NULL DEFAULT 'complete' CHECK (status IN ('complete', 'interrupted', 'failed')),
    audio_ms   int,
    latency    jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, turn_no, role)
);

-- +goose Down
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS ws_tickets;
DROP TABLE IF EXISTS tutoring_sessions;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;
```

- [ ] **Step 3: Write the store core and migrator**

Create `internal/store/store.go`:
```go
// Package store is the Postgres persistence layer. All SQL lives here.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx, so repositories work inside or outside a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store groups the repositories that share one connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// New wraps an existing pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool}
}

// Open connects to Postgres, retrying for a while so the server can start alongside the database.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	backoff := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return New(pool), nil
		}
		if attempt == 10 {
			pool.Close()
			return nil, fmt.Errorf("ping database after %d attempts: %w", attempt, err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

// Ping checks database connectivity (used by /readyz).
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// Close releases all connections.
func (s *Store) Close() { s.Pool.Close() }
```

Create `internal/store/migrate.go`:
```go
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies all pending migrations. A Postgres advisory lock serializes instances that start together.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	fsys, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migration locker: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool) // closing db does not close the pool
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Write the test harness**

Create `internal/store/storetest/storetest.go`:
```go
// Package storetest provides throwaway, fully migrated Postgres databases for tests.
//
// By default it starts an embedded Postgres, so no Docker is needed. The first run downloads the
// Postgres binaries (~20 MB) into ~/.embedded-postgres-go. Set TEST_DATABASE_URL to use an
// existing server instead; that role needs the CREATEDB privilege.
package storetest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/store"
)

var (
	serverURL    string     // maintenance database on the test server
	templateName string     // migrated template cloned for each test
	createMu     sync.Mutex // CREATE DATABASE ... TEMPLATE must not run concurrently
	dbCounter    atomic.Int64
)

// Main starts the database server, builds the migrated template, runs the tests and cleans up.
// Call it from TestMain: func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
func Main(m *testing.M) int {
	ctx := context.Background()
	stop := func() {}
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		serverURL = u
	} else {
		u, stopEmbedded, err := startEmbedded()
		if err != nil {
			fmt.Fprintln(os.Stderr, "storetest: start embedded postgres:", err)
			return 1
		}
		serverURL, stop = u, stopEmbedded
	}
	defer stop()

	templateName = fmt.Sprintf("shiksha_tpl_%d", os.Getpid())
	if err := createTemplate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "storetest: create template:", err)
		return 1
	}
	defer dropDatabase(ctx, templateName)

	return m.Run()
}

// NewStore returns a Store on a fresh database cloned from the migrated template.
// The database is dropped when the test ends.
func NewStore(t testing.TB) *store.Store {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), dbCounter.Add(1))

	createMu.Lock()
	err := execAdmin(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateName))
	createMu.Unlock()
	if err != nil {
		t.Fatalf("storetest: create database: %v", err)
	}

	pool, err := pgxpool.New(ctx, databaseURL(name))
	if err != nil {
		t.Fatalf("storetest: connect: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropDatabase(ctx, name)
	})
	return store.New(pool)
}

func createTemplate(ctx context.Context) error {
	dropDatabase(ctx, templateName)
	if err := execAdmin(ctx, "CREATE DATABASE "+templateName); err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL(templateName))
	if err != nil {
		return err
	}
	defer pool.Close() // a template must have no open connections when it is cloned
	return store.Migrate(ctx, pool)
}

func dropDatabase(ctx context.Context, name string) {
	_ = execAdmin(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
}

func execAdmin(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func databaseURL(name string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		panic(fmt.Sprintf("storetest: bad server URL: %v", err))
	}
	u.Path = "/" + name
	return u.String()
}

func startEmbedded() (string, func(), error) {
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "shiksha-pg-")
	if err != nil {
		return "", nil, err
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		BinariesPath(filepath.Join(dir, "bin")).
		StartTimeout(60 * time.Second).
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	u := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	return u, func() {
		_ = pg.Stop()
		_ = os.RemoveAll(dir)
	}, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
```

- [ ] **Step 5: Write the failing tests**

Create `internal/store/main_test.go`:
```go
package store_test

import (
	"os"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
```

Create `internal/store/migrate_test.go`:
```go
package store_test

import (
	"context"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/store"
	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

func TestMigrationsCreateSchema(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	for _, table := range []string{"users", "refresh_tokens", "password_reset_tokens", "tutoring_sessions", "ws_tickets", "messages"} {
		var exists bool
		if err := st.Pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s was not created", table)
		}
	}
}

func TestMigrateIsIdempotentAndLeavesPoolUsable(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, st.Pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := st.Ping(ctx); err != nil {
		t.Fatalf("pool unusable after Migrate: %v", err)
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `go mod tidy && go test -race ./internal/store/...`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/store`. The first run downloads Postgres and can take about a minute.
- If `pg.Start()` fails with an initdb locale error on Windows, add `.Locale("C")` to the config chain in `startEmbedded` and re-run.
- Always run this package once on its own before running the whole suite in parallel. That way the binary download happens once, instead of racing across packages.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/store
git commit -m "feat(store): pgx pool, embedded goose migrations, embedded-postgres test harness" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Domain types and users repository

**Files:**
- Create: `internal/domain/errors.go`, `internal/domain/user.go`, `internal/store/errors.go`, `internal/store/users.go`
- Modify: `internal/store/store.go` (add `Users` to `Store`)
- Test: `internal/store/users_test.go`

**Interfaces:**
- Consumes: `store.DBTX`, `storetest.NewStore` (Task 2)
- Produces:
  - `domain.ErrNotFound`, `domain.ErrEmailTaken`, `domain.ErrInvalidCredentials`, `domain.ErrTokenInvalid`
  - `domain.ValidationError{Field, Message string}`
  - `domain.User{ID uuid.UUID; Email, PasswordHash, DisplayName, PreferredLang string; Grade *int; TermsAcceptedAt time.Time; GuardianConsentAt *time.Time; CreatedAt, UpdatedAt time.Time}`
  - `store.NewUser{Email, PasswordHash, DisplayName, PreferredLang string; Grade *int; GuardianConsent bool}`
  - `store.ProfileUpdate{DisplayName, PreferredLang *string; Grade *int}`
  - `(*store.Users).Create(ctx, NewUser) (domain.User, error)`
  - `GetByEmail(ctx, string) (domain.User, error)`
  - `GetByID(ctx, uuid.UUID) (domain.User, error)`
  - `UpdateProfile(ctx, uuid.UUID, ProfileUpdate) (domain.User, error)`
  - `Delete(ctx, uuid.UUID) error`

- [ ] **Step 1: Write the domain types**

Create `internal/domain/errors.go`:
```go
// Package domain holds types shared across packages. It imports no other internal package.
package domain

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrTokenInvalid       = errors.New("token invalid or expired")
)

// ValidationError reports a rejected input field. HTTP handlers map it to 400.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }
```

Create `internal/domain/user.go`:
```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// User is a learner account.
type User struct {
	ID                uuid.UUID
	Email             string // normalized: trimmed, lower case
	PasswordHash      string // argon2id PHC string
	DisplayName       string
	PreferredLang     string
	Grade             *int // 1-12, nil when not given
	TermsAcceptedAt   time.Time
	GuardianConsentAt *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/store/users_test.go`:
```go
package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
	"github.com/bitwizard25/Shiksh_AI/internal/store"
	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

func newUser(email string) store.NewUser {
	return store.NewUser{Email: email, PasswordHash: "hash", DisplayName: "Asha", PreferredLang: "hi"}
}

func TestUsersCreateAndGet(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	grade := 7
	n := newUser("asha@example.com")
	n.Grade = &grade
	n.GuardianConsent = true

	created, err := st.Users.Create(ctx, n)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil || created.Grade == nil || *created.Grade != 7 {
		t.Fatalf("created = %+v, want id set and grade 7", created)
	}
	if created.GuardianConsentAt == nil || created.TermsAcceptedAt.IsZero() {
		t.Fatalf("consent timestamps not set: %+v", created)
	}

	byEmail, err := st.Users.GetByEmail(ctx, "ASHA@example.com")
	if err != nil || byEmail.ID != created.ID {
		t.Fatalf("GetByEmail = %+v, %v; want id %s", byEmail, err, created.ID)
	}
	byID, err := st.Users.GetByID(ctx, created.ID)
	if err != nil || byID.Email != "asha@example.com" {
		t.Fatalf("GetByID = %+v, %v", byID, err)
	}
}

func TestUsersCreateRejectsDuplicateEmailCaseInsensitively(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	if _, err := st.Users.Create(ctx, newUser("dup@example.com")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := st.Users.Create(ctx, newUser("DUP@example.com"))
	if !errors.Is(err, domain.ErrEmailTaken) {
		t.Fatalf("second Create err = %v, want ErrEmailTaken", err)
	}
}

func TestUsersGetMissingReturnsNotFound(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	if _, err := st.Users.GetByID(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetByID err = %v, want ErrNotFound", err)
	}
	if _, err := st.Users.GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetByEmail err = %v, want ErrNotFound", err)
	}
}

func TestUsersUpdateProfileChangesOnlyGivenFields(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	u, err := st.Users.Create(ctx, newUser("p@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	name, grade := "Asha K", 9
	updated, err := st.Users.UpdateProfile(ctx, u.ID, store.ProfileUpdate{DisplayName: &name, Grade: &grade})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.DisplayName != "Asha K" || updated.Grade == nil || *updated.Grade != 9 || updated.PreferredLang != "hi" {
		t.Fatalf("updated = %+v", updated)
	}
	same, err := st.Users.UpdateProfile(ctx, u.ID, store.ProfileUpdate{})
	if err != nil {
		t.Fatalf("empty UpdateProfile: %v", err)
	}
	if same.DisplayName != "Asha K" || *same.Grade != 9 {
		t.Fatalf("empty update changed fields: %+v", same)
	}
	if _, err := st.Users.UpdateProfile(ctx, uuid.New(), store.ProfileUpdate{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateProfile(missing) err = %v, want ErrNotFound", err)
	}
}

func TestUsersDelete(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	u, err := st.Users.Create(ctx, newUser("d@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Users.Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Users.GetByID(ctx, u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID after delete err = %v", err)
	}
	if err := st.Users.Delete(ctx, u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second Delete err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL, the build fails because `st.Users` and `store.NewUser` are undefined.

- [ ] **Step 4: Implement the repository**

Create `internal/store/errors.go`:
```go
package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// isUniqueViolation reports whether err is a Postgres unique_violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

Create `internal/store/users.go`:
```go
package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
)

const userColumns = `id, email, password_hash, display_name, preferred_lang, grade,
	terms_accepted_at, guardian_consent_at, created_at, updated_at`

// Users is the users repository.
type Users struct{ db DBTX }

// NewUser is the input to Users.Create. Email must already be normalized.
type NewUser struct {
	Email           string
	PasswordHash    string
	DisplayName     string
	PreferredLang   string
	Grade           *int
	GuardianConsent bool
}

// ProfileUpdate changes only its non-nil fields.
type ProfileUpdate struct {
	DisplayName   *string
	PreferredLang *string
	Grade         *int
}

// Create inserts a user. It returns domain.ErrEmailTaken if the email exists in any letter case.
func (u *Users) Create(ctx context.Context, n NewUser) (domain.User, error) {
	user, err := scanUser(u.db.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, display_name, preferred_lang, grade, terms_accepted_at, guardian_consent_at)
		VALUES ($1, $2, $3, $4, $5, now(), CASE WHEN $6::boolean THEN now() END)
		RETURNING `+userColumns,
		n.Email, n.PasswordHash, n.DisplayName, n.PreferredLang, n.Grade, n.GuardianConsent))
	if isUniqueViolation(err) {
		return domain.User{}, domain.ErrEmailTaken
	}
	return user, err
}

// GetByEmail looks a user up case-insensitively.
func (u *Users) GetByEmail(ctx context.Context, email string) (domain.User, error) {
	return scanUser(u.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, email))
}

// GetByID returns domain.ErrNotFound if the user does not exist.
func (u *Users) GetByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	return scanUser(u.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// UpdateProfile applies the non-nil fields of p and returns the updated user.
func (u *Users) UpdateProfile(ctx context.Context, id uuid.UUID, p ProfileUpdate) (domain.User, error) {
	return scanUser(u.db.QueryRow(ctx, `
		UPDATE users SET
			display_name   = coalesce($2, display_name),
			preferred_lang = coalesce($3, preferred_lang),
			grade          = coalesce($4, grade),
			updated_at     = now()
		WHERE id = $1
		RETURNING `+userColumns,
		id, p.DisplayName, p.PreferredLang, p.Grade))
}

// Delete removes the user and, through ON DELETE CASCADE, all their tokens and sessions.
func (u *Users) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := u.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func scanUser(row pgx.Row) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.PreferredLang, &u.Grade,
		&u.TermsAcceptedAt, &u.GuardianConsentAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	return u, err
}
```

Modify `internal/store/store.go`: replace the `Store` struct and `New` with:
```go
// Store groups the repositories that share one connection pool.
type Store struct {
	Pool  *pgxpool.Pool
	Users *Users
}

// New wraps an existing pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, Users: &Users{db: pool}}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/store/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/store`

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/domain internal/store
git commit -m "feat(store): users repository and shared domain errors" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: argon2id password hasher

**Files:**
- Create: `internal/auth/password.go`
- Test: `internal/auth/password_test.go`

**Interfaces:**
- Produces:
  - `auth.NewPasswordHasher(maxConcurrent int) *auth.PasswordHasher`
  - `(*PasswordHasher).Hash(ctx, password string) (string, error)`
  - `(*PasswordHasher).Verify(ctx, password, encoded string) (bool, error)`
  - `(*PasswordHasher).VerifyDummy(ctx, password string)`

- [ ] **Step 1: Write the failing tests**

Run: `go get golang.org/x/crypto@latest`

Create `internal/auth/password_test.go`:
```go
package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHashThenVerify(t *testing.T) {
	h := NewPasswordHasher(2)
	ctx := context.Background()
	enc, err := h.Hash(ctx, "correct horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("encoded = %q, want argon2id PHC prefix with OWASP params", enc)
	}
	if ok, err := h.Verify(ctx, "correct horse", enc); err != nil || !ok {
		t.Fatalf("Verify(correct) = %v, %v; want true", ok, err)
	}
	if ok, err := h.Verify(ctx, "wrong horse", enc); err != nil || ok {
		t.Fatalf("Verify(wrong) = %v, %v; want false", ok, err)
	}
}

func TestHashUsesRandomSalt(t *testing.T) {
	h := NewPasswordHasher(2)
	a, _ := h.Hash(context.Background(), "same password")
	b, _ := h.Hash(context.Background(), "same password")
	if a == b {
		t.Fatal("two hashes of the same password are identical; salt is not random")
	}
}

func TestVerifyHandlesUnicodePasswords(t *testing.T) {
	h := NewPasswordHasher(2)
	enc, err := h.Hash(context.Background(), "पासवर्ड१२३")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.Verify(context.Background(), "पासवर्ड१२३", enc); !ok {
		t.Fatal("Devanagari password did not verify")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	h := NewPasswordHasher(1)
	for _, enc := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=19456,t=2,p=1$AAAA$AAAA",
		"$argon2id$v=18$m=19456,t=2,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=x,t=2,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=19456,t=2,p=1$!!!!$AAAA",
	} {
		if _, err := h.Verify(context.Background(), "pw", enc); err == nil {
			t.Errorf("Verify(%q) err = nil, want error", enc)
		}
	}
}

func TestHashRespectsContextWhenSaturated(t *testing.T) {
	h := NewPasswordHasher(1)
	h.sem <- struct{}{} // occupy the only slot
	defer func() { <-h.sem }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash err = %v, want context.Canceled", err)
	}
}

func TestVerifyDummyDoesNotPanic(t *testing.T) {
	NewPasswordHasher(1).VerifyDummy(context.Background(), "anything")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/`
Expected: FAIL, the build fails because `NewPasswordHasher` is undefined.

- [ ] **Step 3: Implement the hasher**

Create `internal/auth/password.go`:
```go
// Package auth implements accounts: password hashing, tokens and the account service.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: the OWASP minimum recommendation (19 MiB, 2 iterations, 1 lane).
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

var errMalformedHash = errors.New("auth: malformed password hash")

// PasswordHasher hashes and verifies passwords with argon2id. Every hash allocates about 19 MiB,
// so a semaphore caps concurrent hashes and bounds memory during a burst of logins.
type PasswordHasher struct {
	sem   chan struct{}
	dummy string
}

// NewPasswordHasher allows at most maxConcurrent hashes at a time (use 2×NumCPU in production).
func NewPasswordHasher(maxConcurrent int) *PasswordHasher {
	salt := make([]byte, argonSaltLen) // a fixed salt is fine: the dummy hash never matches a real password
	key := argon2.IDKey([]byte("dummy password"), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return &PasswordHasher{
		sem:   make(chan struct{}, max(1, maxConcurrent)),
		dummy: encodeHash(salt, key, argonMemoryKiB, argonTime, argonThreads),
	}
}

// Hash returns a PHC-formatted hash: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>.
func (h *PasswordHasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return encodeHash(salt, key, argonMemoryKiB, argonTime, argonThreads), nil
}

// Verify reports whether password matches encoded. Parameters are read from the hash itself,
// so hashes made with older parameters keep verifying after the constants change.
func (h *PasswordHasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	salt, key, m, t, p, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// VerifyDummy spends the same time as Verify. Call it when the account does not exist so that
// response timing does not reveal which emails are registered.
func (h *PasswordHasher) VerifyDummy(ctx context.Context, password string) {
	_, _ = h.Verify(ctx, password, h.dummy)
}

func (h *PasswordHasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *PasswordHasher) release() { <-h.sem }

func encodeHash(salt, key []byte, m, t uint32, p uint8) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, m, t, p,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func decodeHash(encoded string) (salt, key []byte, m, t uint32, p uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	return salt, key, m, t, p, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/auth/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/auth`

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/auth
git commit -m "feat(auth): argon2id password hasher with concurrency cap" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Access tokens (JWT) and opaque tokens

**Files:**
- Create: `internal/auth/tokens.go`
- Test: `internal/auth/tokens_test.go`

**Interfaces:**
- Consumes: `domain.ErrTokenInvalid` (Task 3)
- Produces:
  - `auth.NewTokenIssuer(secret string, ttl time.Duration) *auth.TokenIssuer`
  - `(*TokenIssuer).IssueAccess(uuid.UUID) (string, time.Duration, error)`
  - `(*TokenIssuer).VerifyAccess(string) (uuid.UUID, error)` (any failure returns `domain.ErrTokenInvalid`)
  - `auth.NewOpaqueToken() (plain string, hash []byte, err error)`
  - `auth.HashOpaque(string) []byte`

- [ ] **Step 1: Write the failing tests**

Run: `go get github.com/golang-jwt/jwt/v5@latest`

Create `internal/auth/tokens_test.go`:
```go
package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
)

var testSecret = strings.Repeat("s", 32)

func TestIssueAndVerifyAccess(t *testing.T) {
	ti := NewTokenIssuer(testSecret, 15*time.Minute)
	id := uuid.New()
	tok, ttl, err := ti.IssueAccess(id)
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	if ttl != 15*time.Minute {
		t.Errorf("ttl = %v, want 15m", ttl)
	}
	got, err := ti.VerifyAccess(tok)
	if err != nil || got != id {
		t.Fatalf("VerifyAccess = %v, %v; want %v", got, err, id)
	}
}

func TestVerifyAccessRejectsExpired(t *testing.T) {
	ti := NewTokenIssuer(testSecret, time.Minute)
	start := time.Now()
	ti.now = func() time.Time { return start }
	tok, _, err := ti.IssueAccess(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	ti.now = func() time.Time { return start.Add(2 * time.Minute) }
	if _, err := ti.VerifyAccess(tok); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyAccessRejectsForeignSignature(t *testing.T) {
	other := NewTokenIssuer(strings.Repeat("x", 32), time.Minute)
	tok, _, _ := other.IssueAccess(uuid.New())
	if _, err := NewTokenIssuer(testSecret, time.Minute).VerifyAccess(tok); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyAccessRejectsWrongAlgorithmAudienceAndMissingExpiry(t *testing.T) {
	ti := NewTokenIssuer(testSecret, time.Minute)
	base := jwt.RegisteredClaims{
		Subject:   uuid.NewString(),
		Issuer:    tokenIssuer,
		Audience:  jwt.ClaimStrings{tokenAudience},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	sign := func(m jwt.SigningMethod, c jwt.RegisteredClaims) string {
		s, err := jwt.NewWithClaims(m, c).SignedString([]byte(testSecret))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	wrongAud := base
	wrongAud.Audience = jwt.ClaimStrings{"ws"}
	noExp := base
	noExp.ExpiresAt = nil
	badSub := base
	badSub.Subject = "not-a-uuid"

	for name, tok := range map[string]string{
		"HS512":          sign(jwt.SigningMethodHS512, base),
		"wrong audience": sign(jwt.SigningMethodHS256, wrongAud),
		"no expiry":      sign(jwt.SigningMethodHS256, noExp),
		"bad subject":    sign(jwt.SigningMethodHS256, badSub),
		"garbage":        "not.a.jwt",
	} {
		if _, err := ti.VerifyAccess(tok); !errors.Is(err, domain.ErrTokenInvalid) {
			t.Errorf("%s: err = %v, want ErrTokenInvalid", name, err)
		}
	}
}

func TestOpaqueTokens(t *testing.T) {
	a, ha, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := NewOpaqueToken()
	if a == b {
		t.Fatal("two opaque tokens are equal")
	}
	if len(a) != 43 {
		t.Errorf("len(token) = %d, want 43 (32 bytes base64url)", len(a))
	}
	if len(ha) != 32 || !bytes.Equal(ha, HashOpaque(a)) {
		t.Errorf("hash mismatch: %x vs %x", ha, HashOpaque(a))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/`
Expected: FAIL, the build fails because `NewTokenIssuer` is undefined.

- [ ] **Step 3: Implement tokens**

Create `internal/auth/tokens.go`:
```go
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
)

const (
	tokenIssuer   = "shiksha-ai"
	tokenAudience = "api"
)

// TokenIssuer signs and verifies short-lived HS256 access tokens.
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewTokenIssuer creates an issuer. secret must be at least 32 bytes (config enforces this).
func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// IssueAccess returns a signed access token for userID and its lifetime.
func (ti *TokenIssuer) IssueAccess(userID uuid.UUID) (string, time.Duration, error) {
	now := ti.now()
	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		Issuer:    tokenIssuer,
		Audience:  jwt.ClaimStrings{tokenAudience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ti.ttl)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.secret)
	if err != nil {
		return "", 0, fmt.Errorf("sign access token: %w", err)
	}
	return signed, ti.ttl, nil
}

// VerifyAccess checks signature, algorithm, issuer, audience and expiry and returns the user id.
// Every failure is reported as domain.ErrTokenInvalid.
func (ti *TokenIssuer) VerifyAccess(token string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return ti.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(5*time.Second),
		jwt.WithTimeFunc(ti.now),
	)
	if err != nil {
		return uuid.Nil, domain.ErrTokenInvalid
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, domain.ErrTokenInvalid
	}
	return id, nil
}

// NewOpaqueToken returns a random URL-safe token and the SHA-256 hash to store in its place.
func NewOpaqueToken() (plain string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashOpaque(plain), nil
}

// HashOpaque returns the SHA-256 of an opaque token, which is the form stored in the database.
func HashOpaque(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/auth/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/auth`

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/auth
git commit -m "feat(auth): HS256 access tokens and opaque token helpers" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Token repository: refresh rotation, reuse detection, password reset

**Files:**
- Create: `internal/store/tokens.go`, `internal/store/auth_tx.go`
- Modify: `internal/store/store.go` (add `Tokens`, `withTx`)
- Test: `internal/store/tokens_test.go`

**Interfaces:**
- Consumes: `store.Users` (Task 3), `domain.ErrTokenInvalid`
- Produces:
  - `(*store.Tokens).InsertRefresh(ctx, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error`
  - `RevokeFamilyOf(ctx, hash []byte) error`
  - `InsertPasswordReset(ctx, userID uuid.UUID, hash []byte, expiresAt time.Time) error`
  - `(*store.Store).RotateRefresh(ctx, oldHash, newHash []byte, newExpiresAt time.Time, grace time.Duration) (uuid.UUID, error)`
  - `(*store.Store).ResetPassword(ctx, tokenHash []byte, newPasswordHash string) (uuid.UUID, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/store/tokens_test.go`:
```go
package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
	"github.com/bitwizard25/Shiksh_AI/internal/store"
	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

const grace = 20 * time.Second

func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func mustUser(t *testing.T, st *store.Store) domain.User {
	t.Helper()
	u, err := st.Users.Create(context.Background(), store.NewUser{
		Email: uuid.NewString() + "@example.com", PasswordHash: "old-hash", DisplayName: "Test", PreferredLang: "hi",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func mustInsertRefresh(t *testing.T, st *store.Store, userID uuid.UUID, token string, expiresAt time.Time) {
	t.Helper()
	if err := st.Tokens.InsertRefresh(context.Background(), userID, uuid.New(), hashOf(token), expiresAt); err != nil {
		t.Fatalf("InsertRefresh: %v", err)
	}
}

func rotate(st *store.Store, from, to string, g time.Duration) (uuid.UUID, error) {
	return st.RotateRefresh(context.Background(), hashOf(from), hashOf(to), time.Now().Add(time.Hour), g)
}

func TestRotateRefreshIssuesWorkingSuccessor(t *testing.T) {
	st := storetest.NewStore(t)
	u := mustUser(t, st)
	mustInsertRefresh(t, st, u.ID, "A", time.Now().Add(time.Hour))

	got, err := rotate(st, "A", "B", grace)
	if err != nil || got != u.ID {
		t.Fatalf("rotate A->B = %v, %v; want %v", got, err, u.ID)
	}
	if _, err := rotate(st, "B", "C", grace); err != nil {
		t.Fatalf("rotate B->C: %v", err)
	}
}

func TestRotateRefreshReuseInsideGraceKeepsFamily(t *testing.T) {
	st := storetest.NewStore(t)
	u := mustUser(t, st)
	mustInsertRefresh(t, st, u.ID, "A", time.Now().Add(time.Hour))
	if _, err := rotate(st, "A", "B", grace); err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(st, "A", "X", grace); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("reuse of A err = %v, want ErrTokenInvalid", err)
	}
	if _, err := rotate(st, "B", "C", grace); err != nil {
		t.Fatalf("B should still work after in-grace reuse of A: %v", err)
	}
}

func TestRotateRefreshReuseAfterGraceRevokesFamily(t *testing.T) {
	st := storetest.NewStore(t)
	u := mustUser(t, st)
	mustInsertRefresh(t, st, u.ID, "A", time.Now().Add(time.Hour))
	if _, err := rotate(st, "A", "B", grace); err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(st, "A", "X", 0); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("replay of A err = %v, want ErrTokenInvalid", err)
	}
	if _, err := rotate(st, "B", "C", grace); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("B after replay err = %v, want ErrTokenInvalid (family revoked)", err)
	}
}

func TestRotateRefreshRejectsExpiredAndUnknown(t *testing.T) {
	st := storetest.NewStore(t)
	u := mustUser(t, st)
	mustInsertRefresh(t, st, u.ID, "OLD", time.Now().Add(-time.Minute))
	if _, err := rotate(st, "OLD", "N", grace); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("expired err = %v, want ErrTokenInvalid", err)
	}
	if _, err := rotate(st, "never-issued", "N2", grace); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("unknown err = %v, want ErrTokenInvalid", err)
	}
}

func TestRotateRefreshConcurrentUseHasOneWinner(t *testing.T) {
	st := storetest.NewStore(t)
	u := mustUser(t, st)
	mustInsertRefresh(t, st, u.ID, "A", time.Now().Add(time.Hour))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = rotate(st, "A", fmt.Sprintf("N%d", i), grace)
		}()
	}
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != -1 {
				t.Fatal("both concurrent rotations succeeded")
			}
			winner = i
		case !errors.Is(err, domain.ErrTokenInvalid):
			t.Fatalf("rotation %d: unexpected error %v", i, err)
		}
	}
	if winner == -1 {
		t.Fatal("no concurrent rotation succeeded")
	}
	if _, err := rotate(st, fmt.Sprintf("N%d", winner), "Z", grace); err != nil {
		t.Fatalf("winner's token should still work (family intact): %v", err)
	}
}

func TestRevokeFamilyOf(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	u := mustUser(t, st)
	mustInsertRefresh(t, st, u.ID, "A", time.Now().Add(time.Hour))
	if _, err := rotate(st, "A", "B", grace); err != nil {
		t.Fatal(err)
	}
	if err := st.Tokens.RevokeFamilyOf(ctx, hashOf("A")); err != nil {
		t.Fatalf("RevokeFamilyOf: %v", err)
	}
	if _, err := rotate(st, "B", "C", grace); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("B after revoke err = %v, want ErrTokenInvalid", err)
	}
	if err := st.Tokens.RevokeFamilyOf(ctx, hashOf("unknown")); err != nil {
		t.Fatalf("RevokeFamilyOf(unknown) = %v, want nil", err)
	}
}

func TestResetPassword(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	u := mustUser(t, st)
	mustInsertRefresh(t, st, u.ID, "A", time.Now().Add(time.Hour))
	for _, tok := range []string{"R1", "R2"} {
		if err := st.Tokens.InsertPasswordReset(ctx, u.ID, hashOf(tok), time.Now().Add(30*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.ResetPassword(ctx, hashOf("R1"), "new-hash")
	if err != nil || got != u.ID {
		t.Fatalf("ResetPassword = %v, %v", got, err)
	}
	reloaded, _ := st.Users.GetByID(ctx, u.ID)
	if reloaded.PasswordHash != "new-hash" {
		t.Fatalf("password hash = %q, want new-hash", reloaded.PasswordHash)
	}
	if _, err := rotate(st, "A", "B", grace); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("refresh after reset err = %v, want ErrTokenInvalid", err)
	}
	if _, err := st.ResetPassword(ctx, hashOf("R1"), "again"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("reused reset token err = %v, want ErrTokenInvalid", err)
	}
	if _, err := st.ResetPassword(ctx, hashOf("R2"), "again"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("other outstanding reset token err = %v, want ErrTokenInvalid", err)
	}
}

func TestResetPasswordRejectsExpired(t *testing.T) {
	st := storetest.NewStore(t)
	ctx := context.Background()
	u := mustUser(t, st)
	if err := st.Tokens.InsertPasswordReset(ctx, u.ID, hashOf("R"), time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResetPassword(ctx, hashOf("R"), "x"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL, the build fails because `st.Tokens` and `st.RotateRefresh` are undefined.

- [ ] **Step 3: Implement the repository and transactions**

Create `internal/store/tokens.go`:
```go
package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Tokens stores refresh tokens and password reset tokens. Only SHA-256 hashes are stored.
type Tokens struct{ db DBTX }

// InsertRefresh stores a refresh token as the first member of rotation family familyID.
func (t *Tokens) InsertRefresh(ctx context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := t.db.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, familyID, hash, expiresAt)
	return err
}

// RevokeFamilyOf revokes every token in the rotation family of the token with this hash (logout).
// An unknown hash is a no-op.
func (t *Tokens) RevokeFamilyOf(ctx context.Context, hash []byte) error {
	_, err := t.db.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE revoked_at IS NULL
		  AND family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1)`, hash)
	return err
}

func (t *Tokens) revokeFamily(ctx context.Context, familyID uuid.UUID) error {
	_, err := t.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`, familyID)
	return err
}

// InsertPasswordReset stores a single-use password reset token.
func (t *Tokens) InsertPasswordReset(ctx context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := t.db.Exec(ctx, `
		INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hash, userID, expiresAt)
	return err
}
```

Create `internal/store/auth_tx.go`:
```go
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
)

// RotateRefresh atomically marks the presented refresh token as used and stores its successor in
// the same family. It returns the owner's id.
//
// If the presented token is unknown, expired, revoked or already used, it returns
// domain.ErrTokenInvalid. If the token was already used more than `grace` ago, it is treated as
// stolen and its whole family is revoked. Inside the grace window (two parallel refreshes when an
// app resumes, or a client retry) the family is left alone, so the other request's token keeps working.
func (s *Store) RotateRefresh(ctx context.Context, oldHash, newHash []byte, newExpiresAt time.Time, grace time.Duration) (uuid.UUID, error) {
	var (
		userID, familyID uuid.UUID
		replayed         bool
	)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE refresh_tokens SET used_at = now()
			WHERE token_hash = $1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at > now()
			RETURNING user_id, family_id`, oldHash).Scan(&userID, &familyID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `
				SELECT family_id, used_at IS NOT NULL AND used_at < now() - make_interval(secs => $2)
				FROM refresh_tokens WHERE token_hash = $1`, oldHash, grace.Seconds()).Scan(&familyID, &replayed)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return domain.ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
			userID, familyID, newHash, newExpiresAt)
		return err
	})
	if replayed {
		if rerr := s.Tokens.revokeFamily(ctx, familyID); rerr != nil {
			return uuid.Nil, rerr
		}
	}
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}

// ResetPassword consumes a single-use reset token, sets the new password hash, invalidates the
// user's other outstanding reset tokens and revokes all their refresh tokens, in one transaction.
func (s *Store) ResetPassword(ctx context.Context, tokenHash []byte, newPasswordHash string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE password_reset_tokens SET used_at = now()
			WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
			RETURNING user_id`, tokenHash).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, newPasswordHash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE password_reset_tokens SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}
```

Modify `internal/store/store.go`: replace the `Store` struct and `New`, and add `withTx`:
```go
// Store groups the repositories that share one connection pool.
type Store struct {
	Pool   *pgxpool.Pool
	Users  *Users
	Tokens *Tokens
}

// New wraps an existing pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, Users: &Users{db: pool}, Tokens: &Tokens{db: pool}}
}

// withTx runs fn in a transaction, committing when fn returns nil and rolling back otherwise.
func (s *Store) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, fn)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/store/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/store`

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/store
git commit -m "feat(store): refresh rotation with reuse detection and password reset tokens" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Mailer

**Files:**
- Create: `internal/mail/mail.go`
- Test: `internal/mail/mail_test.go`

**Interfaces:**
- Produces:
  - `mail.Message{To, Subject, Body string}`
  - `mail.Mailer` interface `{Send(ctx, Message) error}`
  - `mail.LogMailer{Log *slog.Logger}`
  - `mail.SMTPMailer{Host string; Port int; User, Pass, From string}`

- [ ] **Step 1: Write the failing tests**

Create `internal/mail/mail_test.go`:
```go
package mail

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func TestBuildMessage(t *testing.T) {
	msg, err := buildMessage("Shiksha AI <no-reply@example.com>", Message{
		To: "asha@example.com", Subject: "पासवर्ड reset", Body: "line1\nline2",
	}, testNow)
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	s := string(msg)
	for _, want := range []string{
		"From: Shiksha AI <no-reply@example.com>\r\n",
		"To: asha@example.com\r\n",
		"Subject: =?utf-8?q?",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"\r\n\r\nline1\r\nline2",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q:\n%s", want, s)
		}
	}
}

func TestBuildMessageRejectsHeaderInjection(t *testing.T) {
	from := "Shiksha AI <no-reply@example.com>"
	for name, m := range map[string]Message{
		"subject": {To: "a@example.com", Subject: "hi\r\nBcc: evil@example.com"},
		"to":      {To: "a@example.com\r\nBcc: evil@example.com", Subject: "hi"},
		"bad to":  {To: "not an address", Subject: "hi"},
	} {
		if _, err := buildMessage(from, m, testNow); err == nil {
			t.Errorf("%s: err = nil, want rejection", name)
		}
	}
}

func TestLogMailerLogsAndSucceeds(t *testing.T) {
	var buf bytes.Buffer
	m := LogMailer{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	if err := m.Send(context.Background(), Message{To: "a@example.com", Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(buf.String(), "a@example.com") {
		t.Fatalf("log output %q does not mention recipient", buf.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mail/`
Expected: FAIL, the build fails because `buildMessage` and `LogMailer` are undefined.

- [ ] **Step 3: Implement the mailer**

Create `internal/mail/mail.go`:
```go
// Package mail sends transactional email such as password reset links.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Message is a plain-text UTF-8 email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Mailer sends email.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// LogMailer writes messages to the log instead of sending them. Use it only in development.
type LogMailer struct{ Log *slog.Logger }

func (l LogMailer) Send(_ context.Context, m Message) error {
	l.Log.Info("email not sent (SMTP not configured)", "to", m.To, "subject", m.Subject, "body", m.Body)
	return nil
}

// SMTPMailer sends through an SMTP relay and upgrades to TLS with STARTTLS when the server offers it.
type SMTPMailer struct {
	Host string
	Port int
	User string
	Pass string
	From string // "Name <address>"
}

func (s SMTPMailer) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("mail: bad From address: %w", err)
	}
	msg, err := buildMessage(s.From, m, time.Now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: handshake: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	if s.User != "" {
		// PlainAuth refuses to send credentials over an unencrypted connection to a remote host.
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: end body: %w", err)
	}
	return c.Quit()
}

func buildMessage(from string, m Message, now time.Time) ([]byte, error) {
	for _, v := range []string{from, m.To, m.Subject} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("mail: header values must not contain line breaks")
		}
	}
	if _, err := mail.ParseAddress(m.To); err != nil {
		return nil, fmt.Errorf("mail: bad To address: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	body := strings.ReplaceAll(m.Body, "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String()), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/mail/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/mail`

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/mail
git commit -m "feat(mail): SMTP and log mailers with header-injection guard" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Language registry and the account service

**Files:**
- Create: `internal/lang/registry.go`, `internal/auth/validate.go`, `internal/auth/service.go`
- Test: `internal/lang/registry_test.go`, `internal/auth/main_test.go`, `internal/auth/service_test.go`

**Interfaces:**
- Consumes:
  - `store.Store` with `Users`, `Tokens`, `RotateRefresh`, `ResetPassword` (Tasks 3 and 6)
  - `PasswordHasher` (Task 4), `TokenIssuer`, `NewOpaqueToken` and `HashOpaque` (Task 5)
  - `mail.Mailer` (Task 7)
- Produces:
  - `lang.Language{Code, Name, NativeName string}`, `lang.All() []lang.Language`, `lang.Lookup(code string) (lang.Language, bool)`
  - `auth.ServiceConfig{RefreshTTL, RefreshReuseGrace, ResetTTL time.Duration; AppBaseURL string}`
  - `auth.NewService(*store.Store, *PasswordHasher, *TokenIssuer, mail.Mailer, ServiceConfig) *auth.Service`
  - `auth.TokenPair{AccessToken, RefreshToken string; ExpiresIn time.Duration}`
  - `auth.RegisterInput{Email, Password, DisplayName, PreferredLang string; Grade *int; TermsAccepted, GuardianConsent bool}`
  - `auth.ProfileInput{DisplayName, PreferredLang *string; Grade *int}`
  - Service methods:
    - `Register(ctx, RegisterInput) (domain.User, TokenPair, error)`
    - `Login(ctx, email, password string) (domain.User, TokenPair, error)`
    - `Refresh(ctx, refreshToken string) (TokenPair, error)`
    - `Logout(ctx, refreshToken string) error`
    - `ForgotPassword(ctx, email string) error`
    - `ResetPassword(ctx, token, newPassword string) error`
    - `Me(ctx, uuid.UUID) (domain.User, error)`
    - `UpdateProfile(ctx, uuid.UUID, ProfileInput) (domain.User, error)`
    - `DeleteAccount(ctx, uuid.UUID, password string) error`
    - `Authenticate(accessToken string) (uuid.UUID, error)`

- [ ] **Step 1: Write the language registry test**

Create `internal/lang/registry_test.go`:
```go
package lang

import "testing"

func TestLookup(t *testing.T) {
	hi, ok := Lookup("hi")
	if !ok || hi.Name != "Hindi" || hi.NativeName != "हिन्दी" {
		t.Fatalf("Lookup(hi) = %+v, %v", hi, ok)
	}
	if _, ok := Lookup("xx"); ok {
		t.Fatal("Lookup(xx) found a language")
	}
}

func TestAllReturnsACopyInDisplayOrder(t *testing.T) {
	langs := All()
	if len(langs) != 9 || langs[0].Code != "hi" || langs[8].Code != "en" {
		t.Fatalf("All() = %+v", langs)
	}
	langs[0].Code = "zz"
	if All()[0].Code != "hi" {
		t.Fatal("modifying All() result changed the registry")
	}
}
```

- [ ] **Step 2: Implement the registry and run its test**

Create `internal/lang/registry.go`:
```go
// Package lang lists the languages the tutor speaks.
package lang

import "slices"

// Language is a tutoring language. Codes are ISO 639-1, which is also what Bhashini uses.
type Language struct {
	Code       string
	Name       string
	NativeName string
}

var all = []Language{
	{Code: "hi", Name: "Hindi", NativeName: "हिन्दी"},
	{Code: "mr", Name: "Marathi", NativeName: "मराठी"},
	{Code: "bn", Name: "Bengali", NativeName: "বাংলা"},
	{Code: "ta", Name: "Tamil", NativeName: "தமிழ்"},
	{Code: "te", Name: "Telugu", NativeName: "తెలుగు"},
	{Code: "gu", Name: "Gujarati", NativeName: "ગુજરાતી"},
	{Code: "kn", Name: "Kannada", NativeName: "ಕನ್ನಡ"},
	{Code: "ml", Name: "Malayalam", NativeName: "മലയാളം"},
	{Code: "en", Name: "English", NativeName: "English"},
}

// All returns the supported languages in display order. The caller may modify the result.
func All() []Language { return slices.Clone(all) }

// Lookup returns the language with the given code.
func Lookup(code string) (Language, bool) {
	for _, l := range all {
		if l.Code == code {
			return l, true
		}
	}
	return Language{}, false
}
```

Run: `go test -race ./internal/lang/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/lang`

- [ ] **Step 3: Write the failing service tests**

Create `internal/auth/main_test.go`:
```go
package auth

import (
	"os"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
```

Create `internal/auth/service_test.go`:
```go
package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
	"github.com/bitwizard25/Shiksh_AI/internal/mail"
	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

type captureMailer struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (c *captureMailer) Send(_ context.Context, m mail.Message) error {
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

func (c *captureMailer) last(t *testing.T) mail.Message {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		t.Fatal("no email was sent")
	}
	return c.sent[len(c.sent)-1]
}

var resetLink = regexp.MustCompile(`/reset\?token=([A-Za-z0-9_-]+)`)

func newTestService(t *testing.T) (*Service, *captureMailer) {
	t.Helper()
	m := &captureMailer{}
	svc := NewService(storetest.NewStore(t), NewPasswordHasher(4), NewTokenIssuer(strings.Repeat("k", 32), 15*time.Minute), m, ServiceConfig{
		RefreshTTL:        30 * 24 * time.Hour,
		RefreshReuseGrace: 20 * time.Second,
		ResetTTL:          30 * time.Minute,
		AppBaseURL:        "https://app.example/",
	})
	return svc, m
}

func validRegistration() RegisterInput {
	return RegisterInput{Email: "asha@example.com", Password: "correct horse", DisplayName: "Asha", TermsAccepted: true}
}

func TestRegisterCreatesUserAndTokens(t *testing.T) {
	svc, _ := newTestService(t)
	in := validRegistration()
	in.Email = "  Asha@Example.com "
	user, pair, err := svc.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.Email != "asha@example.com" || user.PreferredLang != "hi" {
		t.Fatalf("user = %+v, want normalized email and default lang hi", user)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.ExpiresIn != 15*time.Minute {
		t.Fatalf("pair = %+v", pair)
	}
	if id, err := svc.Authenticate(pair.AccessToken); err != nil || id != user.ID {
		t.Fatalf("Authenticate = %v, %v; want %v", id, err, user.ID)
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	svc, _ := newTestService(t)
	if _, _, err := svc.Register(context.Background(), validRegistration()); err != nil {
		t.Fatal(err)
	}
	in := validRegistration()
	in.Email = "ASHA@example.com"
	if _, _, err := svc.Register(context.Background(), in); !errors.Is(err, domain.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	svc, _ := newTestService(t)
	grade13 := 13
	cases := []struct {
		name   string
		mutate func(*RegisterInput)
		field  string
	}{
		{"bad email", func(in *RegisterInput) { in.Email = "not-an-email" }, "email"},
		{"email with display name", func(in *RegisterInput) { in.Email = "Asha <asha@example.com>" }, "email"},
		{"email without dot in domain", func(in *RegisterInput) { in.Email = "asha@localhost" }, "email"},
		{"email too long", func(in *RegisterInput) { in.Email = strings.Repeat("a", 250) + "@example.com" }, "email"},
		{"short password", func(in *RegisterInput) { in.Password = "short" }, "password"},
		{"7-rune devanagari password", func(in *RegisterInput) { in.Password = "पासवर्ड" }, "password"},
		{"password too long", func(in *RegisterInput) { in.Password = strings.Repeat("p", 129) }, "password"},
		{"blank name", func(in *RegisterInput) { in.DisplayName = "   " }, "display_name"},
		{"name too long", func(in *RegisterInput) { in.DisplayName = strings.Repeat("n", 81) }, "display_name"},
		{"unsupported lang", func(in *RegisterInput) { in.PreferredLang = "xx" }, "preferred_lang"},
		{"grade out of range", func(in *RegisterInput) { in.Grade = &grade13 }, "grade"},
		{"terms not accepted", func(in *RegisterInput) { in.TermsAccepted = false }, "terms_accepted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validRegistration()
			tc.mutate(&in)
			_, _, err := svc.Register(context.Background(), in)
			var ve *domain.ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("err = %v, want validation error on %q", err, tc.field)
			}
		})
	}
}

func TestRegisterAcceptsDevanagariNameAndPassword(t *testing.T) {
	svc, _ := newTestService(t)
	in := validRegistration()
	in.DisplayName = "आशा"
	in.Password = "पासवर्ड१२" // 9 runes, 27 bytes
	user, _, err := svc.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.DisplayName != "आशा" {
		t.Fatalf("DisplayName = %q", user.DisplayName)
	}
	if _, _, err := svc.Login(context.Background(), in.Email, in.Password); err != nil {
		t.Fatalf("Login with Devanagari password: %v", err)
	}
}

func TestLogin(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, _, err := svc.Register(ctx, validRegistration()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, " ASHA@Example.com ", "correct horse"); err != nil {
		t.Fatalf("Login with differently typed email: %v", err)
	}
	if _, _, err := svc.Login(ctx, "asha@example.com", "wrong horse"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}
	if _, _, err := svc.Login(ctx, "nobody@example.com", "correct horse"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v", err)
	}
}

func TestRefreshRotatesAndLogoutRevokes(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, pair, err := svc.Register(ctx, validRegistration())
	if err != nil {
		t.Fatal(err)
	}
	next, err := svc.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if next.RefreshToken == pair.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if _, err := svc.Authenticate(next.AccessToken); err != nil {
		t.Fatalf("new access token invalid: %v", err)
	}
	if _, err := svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("reused refresh err = %v, want ErrTokenInvalid", err)
	}
	third, err := svc.Refresh(ctx, next.RefreshToken)
	if err != nil {
		t.Fatalf("family should survive in-grace reuse: %v", err)
	}
	if err := svc.Logout(ctx, third.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Refresh(ctx, third.RefreshToken); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("refresh after logout err = %v", err)
	}
	if err := svc.Logout(ctx, "garbage"); err != nil {
		t.Fatalf("Logout(unknown) = %v, want nil", err)
	}
}

func TestForgotAndResetPassword(t *testing.T) {
	svc, mailer := newTestService(t)
	ctx := context.Background()
	_, pair, err := svc.Register(ctx, validRegistration())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ForgotPassword(ctx, "ASHA@example.com"); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	msg := mailer.last(t)
	if msg.To != "asha@example.com" || !strings.Contains(msg.Body, "https://app.example/reset?token=") {
		t.Fatalf("email = %+v", msg)
	}
	token := resetLink.FindStringSubmatch(msg.Body)[1]

	var ve *domain.ValidationError
	if err := svc.ResetPassword(ctx, token, "short"); !errors.As(err, &ve) || ve.Field != "new_password" {
		t.Fatalf("short new password err = %v", err)
	}
	if err := svc.ResetPassword(ctx, token, "new password 1"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, _, err := svc.Login(ctx, "asha@example.com", "correct horse"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, _, err := svc.Login(ctx, "asha@example.com", "new password 1"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if _, err := svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("old sessions survived reset: %v", err)
	}
	if err := svc.ResetPassword(ctx, token, "another password"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("reset token reused: %v", err)
	}
	if err := svc.ResetPassword(ctx, "bogus", "long enough pw"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("bogus token err = %v", err)
	}
}

func TestForgotPasswordUnknownEmailSendsNothing(t *testing.T) {
	svc, mailer := newTestService(t)
	if err := svc.ForgotPassword(context.Background(), "nobody@example.com"); err != nil {
		t.Fatalf("ForgotPassword(unknown) = %v, want nil", err)
	}
	if mailer.count() != 0 {
		t.Fatal("email sent for unknown account")
	}
}

func TestUpdateProfile(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	user, _, err := svc.Register(ctx, validRegistration())
	if err != nil {
		t.Fatal(err)
	}
	name, code, grade := " Asha K ", "mr", 8
	got, err := svc.UpdateProfile(ctx, user.ID, ProfileInput{DisplayName: &name, PreferredLang: &code, Grade: &grade})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if got.DisplayName != "Asha K" || got.PreferredLang != "mr" || got.Grade == nil || *got.Grade != 8 {
		t.Fatalf("got = %+v", got)
	}
	bad := "xx"
	var ve *domain.ValidationError
	if _, err := svc.UpdateProfile(ctx, user.ID, ProfileInput{PreferredLang: &bad}); !errors.As(err, &ve) || ve.Field != "preferred_lang" {
		t.Fatalf("bad lang err = %v", err)
	}
	same, err := svc.UpdateProfile(ctx, user.ID, ProfileInput{})
	if err != nil || same.DisplayName != "Asha K" || same.PreferredLang != "mr" || *same.Grade != 8 {
		t.Fatalf("empty update = %+v, %v; want unchanged", same, err)
	}
}

func TestDeleteAccount(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	user, _, err := svc.Register(ctx, validRegistration())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAccount(ctx, user.ID, "wrong horse"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}
	if err := svc.DeleteAccount(ctx, user.ID, "correct horse"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, err := svc.Me(ctx, user.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Me after delete err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/auth/`
Expected: FAIL, the build fails because `NewService`, `RegisterInput` and the related types are undefined.

- [ ] **Step 5: Implement validation and the service**

Create `internal/auth/validate.go`:
```go
package auth

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
	"github.com/bitwizard25/Shiksh_AI/internal/lang"
)

const (
	minPasswordRunes    = 8
	maxPasswordRunes    = 128
	maxDisplayNameRunes = 80
	maxEmailBytes       = 254
)

// normalizeEmail trims and lower-cases an email and checks it is a bare address with a dotted domain.
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if len(email) > maxEmailBytes {
		return "", &domain.ValidationError{Field: "email", Message: "is too long"}
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return "", &domain.ValidationError{Field: "email", Message: "is not a valid email address"}
	}
	return email, nil
}

// validatePassword counts runes, not bytes, so non-Latin passwords get the same limits.
func validatePassword(field, password string) error {
	n := utf8.RuneCountInString(password)
	if n < minPasswordRunes {
		return &domain.ValidationError{Field: field, Message: fmt.Sprintf("must be at least %d characters", minPasswordRunes)}
	}
	if n > maxPasswordRunes {
		return &domain.ValidationError{Field: field, Message: fmt.Sprintf("must be at most %d characters", maxPasswordRunes)}
	}
	return nil
}

func validateDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(name)
	if n == 0 {
		return "", &domain.ValidationError{Field: "display_name", Message: "is required"}
	}
	if n > maxDisplayNameRunes {
		return "", &domain.ValidationError{Field: "display_name", Message: fmt.Sprintf("must be at most %d characters", maxDisplayNameRunes)}
	}
	return name, nil
}

func validateLang(code string) error {
	if _, ok := lang.Lookup(code); !ok {
		return &domain.ValidationError{Field: "preferred_lang", Message: "is not a supported language"}
	}
	return nil
}

func validateGrade(grade *int) error {
	if grade != nil && (*grade < 1 || *grade > 12) {
		return &domain.ValidationError{Field: "grade", Message: "must be between 1 and 12"}
	}
	return nil
}
```

Create `internal/auth/service.go`:
```go
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
	"github.com/bitwizard25/Shiksh_AI/internal/mail"
	"github.com/bitwizard25/Shiksh_AI/internal/store"
)

// ServiceConfig tunes token lifetimes and links.
type ServiceConfig struct {
	RefreshTTL        time.Duration // lifetime of each refresh token (720h)
	RefreshReuseGrace time.Duration // reuse inside this window does not revoke the family (20s)
	ResetTTL          time.Duration // lifetime of password reset links (30m)
	AppBaseURL        string        // password reset links point at <AppBaseURL>/reset?token=...
}

// Service implements account use cases. It is safe for concurrent use.
type Service struct {
	store  *store.Store
	hasher *PasswordHasher
	tokens *TokenIssuer
	mailer mail.Mailer
	cfg    ServiceConfig
}

func NewService(st *store.Store, hasher *PasswordHasher, tokens *TokenIssuer, mailer mail.Mailer, cfg ServiceConfig) *Service {
	return &Service{store: st, hasher: hasher, tokens: tokens, mailer: mailer, cfg: cfg}
}

// TokenPair is returned by Register, Login and Refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
}

// RegisterInput is the sign-up form.
type RegisterInput struct {
	Email           string
	Password        string
	DisplayName     string
	PreferredLang   string // defaults to "hi"
	Grade           *int
	TermsAccepted   bool
	GuardianConsent bool
}

// ProfileInput changes only its non-nil fields.
type ProfileInput struct {
	DisplayName   *string
	PreferredLang *string
	Grade         *int
}

// Register validates the form, creates the account and signs the user in.
func (s *Service) Register(ctx context.Context, in RegisterInput) (domain.User, TokenPair, error) {
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return domain.User{}, TokenPair{}, err
	}
	if err := validatePassword("password", in.Password); err != nil {
		return domain.User{}, TokenPair{}, err
	}
	name, err := validateDisplayName(in.DisplayName)
	if err != nil {
		return domain.User{}, TokenPair{}, err
	}
	if in.PreferredLang == "" {
		in.PreferredLang = "hi"
	}
	if err := validateLang(in.PreferredLang); err != nil {
		return domain.User{}, TokenPair{}, err
	}
	if err := validateGrade(in.Grade); err != nil {
		return domain.User{}, TokenPair{}, err
	}
	if !in.TermsAccepted {
		return domain.User{}, TokenPair{}, &domain.ValidationError{Field: "terms_accepted", Message: "must be accepted"}
	}

	hash, err := s.hasher.Hash(ctx, in.Password)
	if err != nil {
		return domain.User{}, TokenPair{}, err
	}
	user, err := s.store.Users.Create(ctx, store.NewUser{
		Email: email, PasswordHash: hash, DisplayName: name, PreferredLang: in.PreferredLang,
		Grade: in.Grade, GuardianConsent: in.GuardianConsent,
	})
	if err != nil {
		return domain.User{}, TokenPair{}, err
	}
	pair, err := s.issuePair(ctx, user.ID)
	return user, pair, err
}

// Login checks credentials. Unknown emails cost the same time as wrong passwords.
func (s *Service) Login(ctx context.Context, email, password string) (domain.User, TokenPair, error) {
	user, err := s.store.Users.GetByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, domain.ErrNotFound) {
		s.hasher.VerifyDummy(ctx, password)
		return domain.User{}, TokenPair{}, domain.ErrInvalidCredentials
	}
	if err != nil {
		return domain.User{}, TokenPair{}, err
	}
	ok, err := s.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return domain.User{}, TokenPair{}, err
	}
	if !ok {
		return domain.User{}, TokenPair{}, domain.ErrInvalidCredentials
	}
	pair, err := s.issuePair(ctx, user.ID)
	return user, pair, err
}

// Refresh rotates a refresh token and issues a new access token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	plain, hash, err := NewOpaqueToken()
	if err != nil {
		return TokenPair{}, err
	}
	userID, err := s.store.RotateRefresh(ctx, HashOpaque(refreshToken), hash, time.Now().Add(s.cfg.RefreshTTL), s.cfg.RefreshReuseGrace)
	if err != nil {
		return TokenPair{}, err
	}
	access, ttl, err := s.tokens.IssueAccess(userID)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, ExpiresIn: ttl}, nil
}

// Logout revokes the refresh token's whole family. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	return s.store.Tokens.RevokeFamilyOf(ctx, HashOpaque(refreshToken))
}

// ForgotPassword emails a single-use reset link. It never reveals whether the account exists.
func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	user, err := s.store.Users.GetByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, hash, err := NewOpaqueToken()
	if err != nil {
		return err
	}
	if err := s.store.Tokens.InsertPasswordReset(ctx, user.ID, hash, time.Now().Add(s.cfg.ResetTTL)); err != nil {
		return err
	}
	link := strings.TrimRight(s.cfg.AppBaseURL, "/") + "/reset?token=" + url.QueryEscape(plain)
	return s.mailer.Send(ctx, mail.Message{
		To:      user.Email,
		Subject: "Reset your Shiksha AI password",
		Body: fmt.Sprintf("Hi %s,\n\nUse this link within %d minutes to choose a new password:\n\n%s\n\nIf you did not ask for this, you can ignore this email.\n",
			user.DisplayName, int(s.cfg.ResetTTL.Minutes()), link),
	})
}

// ResetPassword sets a new password using a reset token and signs the user out everywhere.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := validatePassword("new_password", newPassword); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	_, err = s.store.ResetPassword(ctx, HashOpaque(token), hash)
	return err
}

// Me returns the user's profile.
func (s *Service) Me(ctx context.Context, userID uuid.UUID) (domain.User, error) {
	return s.store.Users.GetByID(ctx, userID)
}

// UpdateProfile validates and applies the non-nil fields.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, in ProfileInput) (domain.User, error) {
	var upd store.ProfileUpdate
	if in.DisplayName != nil {
		name, err := validateDisplayName(*in.DisplayName)
		if err != nil {
			return domain.User{}, err
		}
		upd.DisplayName = &name
	}
	if in.PreferredLang != nil {
		if err := validateLang(*in.PreferredLang); err != nil {
			return domain.User{}, err
		}
		upd.PreferredLang = in.PreferredLang
	}
	if in.Grade != nil {
		if err := validateGrade(in.Grade); err != nil {
			return domain.User{}, err
		}
		upd.Grade = in.Grade
	}
	return s.store.Users.UpdateProfile(ctx, userID, upd)
}

// DeleteAccount permanently deletes the account after re-checking the password.
func (s *Service) DeleteAccount(ctx context.Context, userID uuid.UUID, password string) error {
	user, err := s.store.Users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := s.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrInvalidCredentials
	}
	return s.store.Users.Delete(ctx, userID)
}

// Authenticate validates an access token and returns the user id.
func (s *Service) Authenticate(accessToken string) (uuid.UUID, error) {
	return s.tokens.VerifyAccess(accessToken)
}

func (s *Service) issuePair(ctx context.Context, userID uuid.UUID) (TokenPair, error) {
	access, ttl, err := s.tokens.IssueAccess(userID)
	if err != nil {
		return TokenPair{}, err
	}
	plain, hash, err := NewOpaqueToken()
	if err != nil {
		return TokenPair{}, err
	}
	if err := s.store.Tokens.InsertRefresh(ctx, userID, uuid.New(), hash, time.Now().Add(s.cfg.RefreshTTL)); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, ExpiresIn: ttl}, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/auth/ ./internal/lang/`
Expected: both packages print `ok`.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/lang internal/auth
git commit -m "feat(auth): account service with validation, rotation, reset and deletion" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: HTTP foundation: JSON I/O, errors, middleware, rate limiter

**Files:**
- Create: `internal/httpapi/respond.go`, `internal/httpapi/middleware.go`, `internal/httpapi/ratelimit.go`
- Test: `internal/httpapi/foundation_test.go`

**Interfaces:**
- Consumes: `domain` errors (Task 3)
- Produces (package-internal, used by Task 10):
  - `writeJSON(w, status int, v any)`
  - `writeError(w, r, status int, code, message string)`
  - `decodeJSON(w, r, dst any) bool`
  - `serviceError(log *slog.Logger, w, r, err error)`
  - `withRequestID(http.Handler) http.Handler`, `requestIDFrom(ctx) string`
  - `accessLog(*slog.Logger) func(http.Handler) http.Handler`
  - `recoverPanics(*slog.Logger) func(http.Handler) http.Handler`
  - `cors([]string) func(http.Handler) http.Handler`
  - `clientIP(r, trustProxy bool) string`
  - `bearerToken(r) (string, bool)`
  - `NewLimiter(interval time.Duration, burst int) *Limiter`, `(*Limiter).Allow(key string) bool`

- [ ] **Step 1: Write the failing tests**

Run: `go get golang.org/x/time@latest`

Create `internal/httpapi/foundation_test.go`:
```go
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

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
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

func TestLimiterAllowsBurstThenRefills(t *testing.T) {
	now := time.Unix(1_000, 0)
	l := NewLimiter(time.Minute, 2)
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
		err        error
		status     int
		code       string
		field      string
	}{
		{&domain.ValidationError{Field: "email", Message: "bad"}, 400, "validation_failed", "email"},
		{domain.ErrEmailTaken, 409, "email_taken", ""},
		{domain.ErrInvalidCredentials, 401, "invalid_credentials", ""},
		{domain.ErrTokenInvalid, 401, "invalid_token", ""},
		{domain.ErrNotFound, 404, "not_found", ""},
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
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/`
Expected: FAIL, the build fails because `NewLimiter`, `decodeJSON` and the other helpers are undefined.

- [ ] **Step 3: Implement the foundation**

Create `internal/httpapi/respond.go`:
```go
// Package httpapi is the HTTP layer: public JSON API and admin endpoints.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bitwizard25/Shiksh_AI/internal/domain"
)

const maxBodyBytes = 1 << 20

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Field     string `json:"field,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeErrorDetail(w, r, status, errorDetail{Code: code, Message: message})
}

func writeErrorDetail(w http.ResponseWriter, r *http.Request, status int, d errorDetail) {
	d.RequestID = requestIDFrom(r.Context())
	writeJSON(w, status, map[string]errorDetail{"error": d})
}

// decodeJSON reads exactly one JSON object into dst. It rejects unknown fields, trailing data
// and bodies over 1 MB. On failure it writes the error response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		if extra := dec.Decode(&struct{}{}); !errors.Is(extra, io.EOF) {
			err = errors.New("body must contain a single JSON object")
		}
	}
	if err == nil {
		return true
	}

	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body must be at most 1 MB")
	case errors.Is(err, io.EOF):
		writeError(w, r, http.StatusBadRequest, "bad_request", "request body is required")
	case errors.As(err, &typeErr):
		writeError(w, r, http.StatusBadRequest, "bad_request", fmt.Sprintf("field %q has the wrong type", typeErr.Field))
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		writeError(w, r, http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), "json: "))
	case err.Error() == "body must contain a single JSON object":
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
	default:
		writeError(w, r, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
	}
	return false
}

// serviceError maps domain errors to HTTP responses and logs anything unexpected.
func serviceError(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeErrorDetail(w, r, http.StatusBadRequest, errorDetail{Code: "validation_failed", Message: ve.Error(), Field: ve.Field})
	case errors.Is(err, domain.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, "email_taken", "an account with this email already exists")
	case errors.Is(err, domain.ErrInvalidCredentials):
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
	case errors.Is(err, domain.ErrTokenInvalid):
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "token is invalid or expired")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, context.Canceled):
		// The client went away; there is nobody to answer.
	default:
		log.ErrorContext(r.Context(), "request failed", "err", err, "request_id", requestIDFrom(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal", "something went wrong")
	}
}
```

Create `internal/httpapi/middleware.go`:
```go
package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

type ctxKey int

const ctxRequestID ctxKey = iota

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// withRequestID keeps a well-formed incoming X-Request-ID or generates one, and echoes it back.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// statusRecorder captures the response status for access logs. It forwards Hijack and Flush so
// WebSocket upgrades (Plan 4) and streaming keep working behind the middleware.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) Flush() { _ = http.NewResponseController(s.ResponseWriter).Flush() }

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if s.status == 0 {
		s.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(s.ResponseWriter).Hijack()
}

// accessLog writes one line per request. It logs the path only, never the query string, so
// WebSocket tickets and other secrets passed in URLs stay out of the logs.
func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			log.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", requestIDFrom(r.Context()))
		})
	}
}

// recoverPanics turns a handler panic into a logged 500 instead of a dropped connection.
func recoverPanics(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.ErrorContext(r.Context(), "panic serving request",
					"panic", fmt.Sprint(v), "stack", string(debug.Stack()), "request_id", requestIDFrom(r.Context()))
				writeError(w, r, http.StatusInternalServerError, "internal", "something went wrong")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// cors allows the configured browser origins ("*" allows any) and answers preflight requests.
func cors(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowed[origin] || allowed["*"]) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Expose-Headers", "X-Request-ID")
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
					h.Set("Access-Control-Max-Age", "600")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP returns the caller's IP. Behind a trusted reverse proxy it takes the last
// X-Forwarded-For entry, the one the proxy itself appended; earlier entries are client-controlled.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// bearerToken extracts the token from "Authorization: Bearer <token>". The scheme is case-insensitive (RFC 7235).
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
```

Create `internal/httpapi/ratelimit.go`:
```go
package httpapi

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	limiterIdleTTL   = time.Hour // must exceed the slowest full refill (forgot-password: 3/hour)
	limiterSweepSize = 10_000    // sweep idle buckets once the map grows this large
)

// Limiter is an in-memory keyed token bucket (per instance; Redis-backed limits are an upcoming feature).
type Limiter struct {
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

// NewLimiter allows `burst` events at once, refilling one event every `interval`.
func NewLimiter(interval time.Duration, burst int) *Limiter {
	return &Limiter{every: rate.Every(interval), burst: burst, buckets: make(map[string]*bucket), now: time.Now}
}

// Allow reports whether one more event for key is allowed now.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= limiterSweepSize {
			l.sweep(now)
		}
		b = &bucket{lim: rate.NewLimiter(l.every, l.burst)}
		l.buckets[key] = b
	}
	b.seen = now
	return b.lim.AllowN(now, 1)
}

func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.seen) > limiterIdleTTL {
			delete(l.buckets, k)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/httpapi/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/httpapi`

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/httpapi
git commit -m "feat(httpapi): JSON I/O, error envelope, middleware and rate limiter" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Account endpoints and router

**Files:**
- Create: `internal/httpapi/router.go`, `internal/httpapi/auth_handlers.go`, `internal/httpapi/user_handlers.go`
- Test: `internal/httpapi/main_test.go`, `internal/httpapi/api_test.go`

**Interfaces:**
- Consumes: `auth.Service` and its inputs (Task 8), `lang.All` (Task 8), and everything from Task 9
- Produces:
  - `httpapi.Options{Auth *auth.Service; Log *slog.Logger; AllowedOrigins []string; TrustProxy bool}`
  - `httpapi.New(Options) http.Handler`
  - JSON shapes: `userResponse`, `tokenResponse`, `authResponse`, `languageResponse`

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/main_test.go`:
```go
package httpapi

import (
	"os"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
```

Create `internal/httpapi/api_test.go`:
```go
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

	"github.com/bitwizard25/Shiksh_AI/internal/auth"
	"github.com/bitwizard25/Shiksh_AI/internal/mail"
	"github.com/bitwizard25/Shiksh_AI/internal/store/storetest"
)

type captureMailer struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (c *captureMailer) Send(_ context.Context, m mail.Message) error {
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

type testServer struct {
	*httptest.Server
	mailer *captureMailer
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	m := &captureMailer{}
	svc := auth.NewService(storetest.NewStore(t), auth.NewPasswordHasher(4), auth.NewTokenIssuer(strings.Repeat("k", 32), 15*time.Minute), m, auth.ServiceConfig{
		RefreshTTL: 30 * 24 * time.Hour, RefreshReuseGrace: 20 * time.Second, ResetTTL: 30 * time.Minute, AppBaseURL: "https://app.example",
	})
	srv := httptest.NewServer(New(Options{Auth: svc, Log: discardLog, AllowedOrigins: []string{"https://app.example"}}))
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
	if code := s.do(t, "PATCH", "/v1/me", bearer, map[string]any{"grade": 6, "preferred_lang": "ta"}, &me); code != 200 || me.PreferredLang != "ta" || *me.Grade != 6 {
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
	s.mailer.mu.Lock()
	body := s.mailer.sent[len(s.mailer.sent)-1].Body
	s.mailer.mu.Unlock()
	token := regexp.MustCompile(`/reset\?token=([A-Za-z0-9_-]+)`).FindStringSubmatch(body)[1]

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/`
Expected: FAIL, the build fails because `New`, `Options`, `authResponse` and the other API types are undefined.

- [ ] **Step 3: Implement the router and handlers**

Create `internal/httpapi/router.go`:
```go
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/auth"
)

// Options configures the public API handler.
type Options struct {
	Auth           *auth.Service
	Log            *slog.Logger
	AllowedOrigins []string
	TrustProxy     bool
}

// API holds handler dependencies.
type API struct {
	auth       *auth.Service
	log        *slog.Logger
	trustProxy bool
	limits     rateLimits
}

type rateLimits struct {
	register *Limiter // per IP
	login    *Limiter // per IP + email
	loginIP  *Limiter // per IP, looser because a school NAT shares one address
	refresh  *Limiter // per IP
	forgot   *Limiter // per email (forgot and reset)
	forgotIP *Limiter // per IP (forgot and reset)
}

func defaultLimits() rateLimits {
	return rateLimits{
		register: NewLimiter(time.Minute/10, 10),
		login:    NewLimiter(time.Minute/5, 5),
		loginIP:  NewLimiter(time.Second, 60),
		refresh:  NewLimiter(time.Minute/30, 30),
		forgot:   NewLimiter(time.Hour/3, 3),
		forgotIP: NewLimiter(time.Minute/10, 10),
	}
}

// New returns the public API handler with middleware applied.
func New(opts Options) http.Handler {
	a := &API{auth: opts.Auth, log: opts.Log, trustProxy: opts.TrustProxy, limits: defaultLimits()}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/register", a.handleRegister)
	mux.HandleFunc("POST /v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST /v1/auth/refresh", a.handleRefresh)
	mux.HandleFunc("POST /v1/auth/logout", a.handleLogout)
	mux.HandleFunc("POST /v1/auth/password/forgot", a.handleForgotPassword)
	mux.HandleFunc("POST /v1/auth/password/reset", a.handleResetPassword)
	mux.HandleFunc("GET /v1/me", a.requireAuth(a.handleGetMe))
	mux.HandleFunc("PATCH /v1/me", a.requireAuth(a.handlePatchMe))
	mux.HandleFunc("DELETE /v1/me", a.requireAuth(a.handleDeleteMe))
	mux.HandleFunc("GET /v1/languages", a.handleLanguages)

	var h http.Handler = mux
	h = cors(opts.AllowedOrigins)(h)
	h = accessLog(opts.Log)(h)
	h = recoverPanics(opts.Log)(h)
	return withRequestID(h)
}

// allow applies a rate limit and writes a 429 when it is exceeded.
func (a *API) allow(w http.ResponseWriter, r *http.Request, l *Limiter, key string) bool {
	if l.Allow(key) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests, try again later")
	return false
}

// requireAuth validates the bearer access token and passes the user id to next.
func (a *API) requireAuth(next func(http.ResponseWriter, *http.Request, uuid.UUID)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "missing bearer token")
			return
		}
		userID, err := a.auth.Authenticate(token)
		if err != nil {
			serviceError(a.log, w, r, err)
			return
		}
		next(w, r, userID)
	}
}
```

Create `internal/httpapi/auth_handlers.go`:
```go
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/auth"
	"github.com/bitwizard25/Shiksh_AI/internal/domain"
)

type userResponse struct {
	ID              uuid.UUID `json:"id"`
	Email           string    `json:"email"`
	DisplayName     string    `json:"display_name"`
	PreferredLang   string    `json:"preferred_lang"`
	Grade           *int      `json:"grade"`
	GuardianConsent bool      `json:"guardian_consent"`
	CreatedAt       time.Time `json:"created_at"`
}

func toUserResponse(u domain.User) userResponse {
	return userResponse{
		ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, PreferredLang: u.PreferredLang,
		Grade: u.Grade, GuardianConsent: u.GuardianConsentAt != nil, CreatedAt: u.CreatedAt,
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
}

func toTokenResponse(p auth.TokenPair) tokenResponse {
	return tokenResponse{AccessToken: p.AccessToken, RefreshToken: p.RefreshToken, TokenType: "Bearer", ExpiresIn: int64(p.ExpiresIn / time.Second)}
}

type authResponse struct {
	User userResponse `json:"user"`
	tokenResponse
}

type registerRequest struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	DisplayName     string `json:"display_name"`
	PreferredLang   string `json:"preferred_lang"`
	Grade           *int   `json:"grade"`
	TermsAccepted   bool   `json:"terms_accepted"`
	GuardianConsent bool   `json:"guardian_consent"`
}

func (a *API) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !a.allow(w, r, a.limits.register, clientIP(r, a.trustProxy)) {
		return
	}
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, pair, err := a.auth.Register(r.Context(), auth.RegisterInput{
		Email: req.Email, Password: req.Password, DisplayName: req.DisplayName, PreferredLang: req.PreferredLang,
		Grade: req.Grade, TermsAccepted: req.TermsAccepted, GuardianConsent: req.GuardianConsent,
	})
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, authResponse{User: toUserResponse(user), tokenResponse: toTokenResponse(pair)})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, a.trustProxy)
	if !a.allow(w, r, a.limits.loginIP, ip) {
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.allow(w, r, a.limits.login, ip+"|"+strings.ToLower(strings.TrimSpace(req.Email))) {
		return
	}
	user, pair, err := a.auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authResponse{User: toUserResponse(user), tokenResponse: toTokenResponse(pair)})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (a *API) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !a.allow(w, r, a.limits.refresh, clientIP(r, a.trustProxy)) {
		return
	}
	var req refreshRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	pair, err := a.auth.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTokenResponse(pair))
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.auth.Logout(r.Context(), req.RefreshToken); err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type forgotRequest struct {
	Email string `json:"email"`
}

func (a *API) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if !a.allow(w, r, a.limits.forgotIP, clientIP(r, a.trustProxy)) {
		return
	}
	var req forgotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.allow(w, r, a.limits.forgot, strings.ToLower(strings.TrimSpace(req.Email))) {
		return
	}
	if err := a.auth.ForgotPassword(r.Context(), req.Email); err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "if the account exists, a reset email has been sent"})
}

type resetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (a *API) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !a.allow(w, r, a.limits.forgotIP, clientIP(r, a.trustProxy)) {
		return
	}
	var req resetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.auth.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Create `internal/httpapi/user_handlers.go`:
```go
package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/auth"
	"github.com/bitwizard25/Shiksh_AI/internal/lang"
)

func (a *API) handleGetMe(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	user, err := a.auth.Me(r.Context(), userID)
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

// patchMeRequest: absent and null fields both mean "leave unchanged".
type patchMeRequest struct {
	DisplayName   *string `json:"display_name"`
	PreferredLang *string `json:"preferred_lang"`
	Grade         *int    `json:"grade"`
}

func (a *API) handlePatchMe(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var req patchMeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := a.auth.UpdateProfile(r.Context(), userID, auth.ProfileInput{
		DisplayName: req.DisplayName, PreferredLang: req.PreferredLang, Grade: req.Grade,
	})
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

type deleteMeRequest struct {
	Password string `json:"password"`
}

func (a *API) handleDeleteMe(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var req deleteMeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.auth.DeleteAccount(r.Context(), userID, req.Password); err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type languageResponse struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	NativeName string `json:"native_name"`
}

func (a *API) handleLanguages(w http.ResponseWriter, _ *http.Request) {
	all := lang.All()
	out := make([]languageResponse, 0, len(all))
	for _, l := range all {
		out = append(out, languageResponse{Code: l.Code, Name: l.Name, NativeName: l.NativeName})
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/httpapi/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/httpapi`

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/httpapi
git commit -m "feat(httpapi): account endpoints, auth guard and rate-limited router" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Admin endpoints, server wiring, dev database, packaging and README

**Files:**
- Create: `internal/httpapi/admin.go`, `cmd/server/main.go`, `cmd/devdb/main.go`, `.env.example`, `.dockerignore`, `Dockerfile`, `docker-compose.yml`
- Modify: `README.md` (replace its contents)
- Test: `internal/httpapi/admin_test.go`

**Interfaces:**
- Consumes: everything above
- Produces:
  - `httpapi.Pinger` interface `{Ping(ctx) error}`
  - `httpapi.NewAdminHandler(Pinger, *atomic.Bool) http.Handler`
  - runnable `cmd/server` and `cmd/devdb`

- [ ] **Step 1: Write the failing admin tests**

Run: `go get github.com/prometheus/client_golang@latest`

Create `internal/httpapi/admin_test.go`:
```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/ -run TestAdminEndpoints`
Expected: FAIL, the build fails because `NewAdminHandler` is undefined.

- [ ] **Step 3: Implement the admin handler**

Create `internal/httpapi/admin.go`:
```go
package httpapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Pinger reports database health.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewAdminHandler serves liveness, readiness and Prometheus metrics on the admin port.
// Readiness checks only the database, because a provider outage must not pull every instance out of rotation.
func NewAdminHandler(db Pinger, ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}
```

Run: `go mod tidy && go test -race ./internal/httpapi/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/httpapi`

- [ ] **Step 4: Write the server and the dev database commands**

Create `cmd/server/main.go`:
```go
// Command server runs the Shiksha AI backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/auth"
	"github.com/bitwizard25/Shiksh_AI/internal/config"
	"github.com/bitwizard25/Shiksh_AI/internal/httpapi"
	"github.com/bitwizard25/Shiksh_AI/internal/mail"
	"github.com/bitwizard25/Shiksh_AI/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return fmt.Errorf("load .env: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	if err := store.Migrate(ctx, st.Pool); err != nil {
		return err
	}

	var mailer mail.Mailer = mail.LogMailer{Log: log}
	if cfg.SMTP.Host != "" {
		mailer = mail.SMTPMailer{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, User: cfg.SMTP.User, Pass: cfg.SMTP.Pass, From: cfg.SMTP.From}
	} else {
		log.Warn("SMTP_HOST is not set; emails (including password reset links) are written to the log")
	}

	authSvc := auth.NewService(st,
		auth.NewPasswordHasher(2*runtime.NumCPU()),
		auth.NewTokenIssuer(cfg.JWTSecret, cfg.AccessTokenTTL),
		mailer,
		auth.ServiceConfig{
			RefreshTTL:        cfg.RefreshTokenTTL,
			RefreshReuseGrace: 20 * time.Second,
			ResetTTL:          30 * time.Minute,
			AppBaseURL:        cfg.AppBaseURL,
		})

	var ready atomic.Bool
	ready.Store(true)
	apiSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.New(httpapi.Options{Auth: authSvc, Log: log, AllowedOrigins: cfg.AllowedOrigins, TrustProxy: cfg.TrustProxy}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: WebSocket connections (Plan 4) are long-lived.
	}
	adminSrv := &http.Server{
		Addr:              cfg.AdminAddr,
		Handler:           httpapi.NewAdminHandler(st, &ready),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() { log.Info("api listening", "addr", cfg.HTTPAddr); errCh <- serve(apiSrv) }()
	go func() { log.Info("admin listening", "addr", cfg.AdminAddr); errCh <- serve(adminSrv) }()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	ready.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return errors.Join(apiSrv.Shutdown(shutdownCtx), adminSrv.Shutdown(shutdownCtx))
}

func serve(s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve %s: %w", s.Addr, err)
	}
	return nil
}
```

Create `cmd/devdb/main.go`:
```go
// Command devdb runs a local Postgres for development without Docker.
// Data persists in .devdata/pg between runs. Stop it with Ctrl+C.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func main() {
	port := flag.Uint("port", 5433, "port to listen on")
	dir := flag.String("dir", ".devdata/pg", "directory for binaries and data")
	flag.Parse()

	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("devdb: %v", err)
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(*port)).
		Database("shiksha").
		DataPath(filepath.Join(abs, "data")).
		RuntimePath(filepath.Join(abs, "runtime")).
		BinariesPath(filepath.Join(abs, "bin")).
		StartTimeout(60 * time.Second))
	if err := pg.Start(); err != nil {
		log.Fatalf("devdb: start postgres: %v", err)
	}
	fmt.Printf("Postgres is ready.\nDATABASE_URL=postgres://postgres:postgres@127.0.0.1:%d/shiksha?sslmode=disable\nPress Ctrl+C to stop.\n", *port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if err := pg.Stop(); err != nil {
		log.Fatalf("devdb: stop postgres: %v", err)
	}
}
```

- [ ] **Step 5: Add packaging files**

Create `.env.example`:
```dotenv
# Copy to .env (git-ignored) and adjust. The server reads .env from its working directory.
DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5433/shiksha?sslmode=disable
JWT_SECRET=replace-with-a-random-string-of-at-least-32-bytes
HTTP_ADDR=:8080
ADMIN_ADDR=:9090
APP_BASE_URL=http://localhost:3000
ALLOWED_ORIGINS=http://localhost:3000
LOG_LEVEL=info
# Leave SMTP_HOST empty in development: reset emails are then written to the log.
SMTP_HOST=
SMTP_PORT=587
SMTP_USER=
SMTP_PASS=
SMTP_FROM=Shiksha AI <no-reply@example.com>
```

Create `.dockerignore`:
```text
.git
.devdata
.env
out
bin
docs
```

Create `Dockerfile`:
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/server"]
```

Create `docker-compose.yml`:
```yaml
services:
  db:
    image: postgres:17
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: shiksha
    ports: ["5432:5432"]
    volumes: [pgdata:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres -d shiksha"]
      interval: 2s
      timeout: 3s
      retries: 30
  server:
    build: .
    env_file: .env
    environment:
      DATABASE_URL: postgres://postgres:postgres@db:5432/shiksha?sslmode=disable
    ports: ["8080:8080", "9090:9090"]
    depends_on:
      db:
        condition: service_healthy
volumes:
  pgdata:
```

- [ ] **Step 6: Replace README.md**

Replace the entire contents of `README.md` with:
````markdown
# Shiksha AI

Shiksha AI is a voice-first tutor for Indian-language learners: speech in, model reasoning, speech out. The turn structure and latency budget are designed so a session feels like talking to a person. This repository is the **Go backend**.

- Design (HLD + LLD + diagrams): [docs/design.md](docs/design.md)
- Implementation plans: [docs/superpowers/plans/](docs/superpowers/plans/)

## Status

| Plan | Scope | State |
|---|---|---|
| 1 | Module, Postgres store, accounts API, admin endpoints | ✅ this branch |
| 2 | Bhashini ASR/TTS + Gemini providers, latency benchmark gate | next |
| 3 | Tutoring sessions, prompts, summaries, sweeper | planned |
| 4 | Realtime voice core (WebSocket, turn pipeline, barge-in) | planned |
| 5 | Latency polish, hardening, protocol docs | planned |

## Run locally (Windows, no Docker)

You need Go 1.27. In the first terminal, start a local Postgres (the first run downloads it):

```powershell
go run ./cmd/devdb
```

In a second terminal:

```powershell
Copy-Item .env.example .env   # then set JWT_SECRET to a random 32+ character string
go run ./cmd/server
```

Try it:

```powershell
$body = @{ email="asha@example.com"; password="correct horse"; display_name="Asha"; terms_accepted=$true } | ConvertTo-Json
$r = Invoke-RestMethod -Method Post -Uri http://localhost:8080/v1/auth/register -ContentType 'application/json' -Body $body
Invoke-RestMethod -Uri http://localhost:8080/v1/me -Headers @{ Authorization = "Bearer $($r.access_token)" }
Invoke-RestMethod -Uri http://localhost:9090/readyz
```

## Run with Docker

```bash
cp .env.example .env   # set JWT_SECRET
docker compose up --build
```

## Tests

```bash
go test -race ./...
```

Database tests start a real embedded Postgres, so no Docker is needed. The first run downloads the binaries. Run `go test ./internal/store/...` once on its own before the full suite, so the download doesn't race across packages. To use an existing server instead, set `TEST_DATABASE_URL` to a role with `CREATEDB`.

## API (current)

| Method | Path | Auth |
|---|---|---|
| POST | `/v1/auth/register` | – |
| POST | `/v1/auth/login` | – |
| POST | `/v1/auth/refresh` | – |
| POST | `/v1/auth/logout` | – |
| POST | `/v1/auth/password/forgot` | – |
| POST | `/v1/auth/password/reset` | – |
| GET / PATCH / DELETE | `/v1/me` | Bearer |
| GET | `/v1/languages` | – |
| GET | `:9090/healthz`, `:9090/readyz`, `:9090/metrics` | – |

Errors use `{"error":{"code","message","field?","request_id"}}`.

## Upcoming features

- **Structured lessons:** topic catalogue, lesson plans, tutor-driven steps, quizzes, progress tracking.
- **Language-learning mode:** pronunciation feedback and conversation practice.
- **Streaming ASR** through Bhashini's socket.io API, so the learner is transcribed while they speak.
- **Gemini Live** native-audio engine as an alternative pipeline.
- **Opus audio downlink**, or resampling, to cut mobile bandwidth.
- **Phone OTP and Google sign-in.**
- **Verifiable parental consent** (DPDP Act 2023).
- **Parent and teacher dashboards** with learning analytics.
- **Opt-in audio retention** for quality review.
- **Usage quotas and plans.**
- **Horizontal scaling:** Redis-backed rate limits, cross-instance session ownership, OpenTelemetry tracing.
- **Web and mobile clients.**
````

- [ ] **Step 7: Full verification and manual smoke test**

Run:
```bash
gofmt -l .
go vet ./...
go test -race ./...
CGO_ENABLED=0 go build -o bin/server.exe ./cmd/server
```
Expected: `gofmt` prints nothing, vet passes, every package prints `ok`, and the build produces `bin/server.exe`.

Then, manually:
1. Start `go run ./cmd/devdb`.
2. Create `.env` from `.env.example` with a 32+ character `JWT_SECRET`.
3. Start `go run ./cmd/server`.
4. Run the three PowerShell commands from the README. Expected results:
   - register returns tokens;
   - `/v1/me` returns the user;
   - `/readyz` returns `ready`.
5. Press Ctrl+C on the server. It should log `shutdown signal received` and exit 0.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/httpapi cmd .env.example .dockerignore Dockerfile docker-compose.yml README.md
git commit -m "feat: admin endpoints, server wiring, dev database, packaging and README" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
