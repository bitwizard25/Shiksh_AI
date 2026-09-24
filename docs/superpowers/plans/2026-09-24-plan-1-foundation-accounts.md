# Plan 1: Foundation + Accounts (Clean Architecture) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Go module in classic Clean Architecture layers. It should contain the Postgres persistence layer and a complete, tested accounts API (register, login, rotating refresh, logout, password reset, profile, account deletion), admin endpoints, and one `shiksha` binary that runs roles (`serve --roles=api`) or migrations (`migrate`).

**Architecture:**
- **Layers:** `entity` (enterprise rules) → `usecase` (interactors + ports) → `adapter` (HTTP controllers, Postgres repositories) → `infrastructure` (config, database, crypto, mail, rate limiter). `bootstrap` is the composition root.
- **Dependency rule:** source dependencies point inward only, enforced by `internal/archtest`.
- **Transactions:** use cases run them through a `TxManager` port that carries the `pgx.Tx` in the context, so the core stays SQL-free.
- **Testing:** use cases are tested against in-memory fakes. Adapters are tested against a real Postgres started by `embedded-postgres`, since Docker isn't installed.

**Tech Stack:** Go 1.27, `github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, `github.com/fergusstrange/embedded-postgres`, `github.com/caarlos0/env/v11`, `github.com/golang-jwt/jwt/v5`, `golang.org/x/crypto/argon2`, `golang.org/x/time/rate`, `github.com/google/uuid`, `github.com/prometheus/client_golang`.

**Spec:** `docs/design.md`. §19 is the authority for architecture and scaling. This plan implements §16 Phase 0 and Phase 1 in the §5/§19 layout: §4, §6, §7 (the auth, me and languages rows), and the roles/admin/lifecycle parts of §14 and §19.4.
- **This is Plan 1 of 5.** Plans 2–5 are providers, sessions + tutor (with River jobs and the LISTEN/NOTIFY bus), realtime core, and latency polish + hardening. Each is written after the previous one lands.
- **Schema vs. repositories:** the full schema, including the session tables, is created here. The session repositories and `GET /v1/subjects` belong to Plan 3.

## Global Constraints

- **Module and toolchain:**
  - Module path: `github.com/bitwizard25/Shiksh_AI`. Go 1.27.
  - The binary builds with `CGO_ENABLED=0`. Tests run with `go test -race ./...` (CGO and mingw gcc are available locally).
- **Clean layers (spec §19.1):**
  - `internal/entity` imports only the stdlib and `github.com/google/uuid`.
  - `internal/usecase` imports only `internal/entity` from this module.
  - `internal/adapter` and `internal/infrastructure` never import `internal/bootstrap`, and `internal/infrastructure` never imports `internal/adapter`.
  - `internal/archtest` enforces all of the above.
- **Ports:** declared in `internal/usecase/ports.go` and implemented by outer layers. The one exception is `RateLimiter`, which is consumer-side in `adapter/httpapi`.
- **Time:** use cases take time from an injected `Now func() time.Time`, and repositories store the timestamps they are given.
- **HTTP:** stdlib `net/http` ServeMux with method patterns. No web framework.
- **SQL:** hand-written with `pgx/v5`; goose v3 migrations from an embedded FS; no ORM, no sqlc.
- **Passwords:** argon2id, m=19 MiB (19456 KiB), t=2, p=1, 16-byte salt, 32-byte key, PHC string. Concurrent hashes are limited by a semaphore of 2×NumCPU.
- **Access JWT:** HS256, `iss=shiksha-ai`, `aud=api`, TTL 15m. `JWT_SECRET` must be ≥ 32 bytes.
- **Refresh token:** 32 random bytes, base64url, stored as sha256, TTL 720h. It rotates on every use with a **20 s reuse grace**. Reuse after the grace window revokes the whole family.
- **Password reset token:** 32 random bytes, stored as sha256, 30 min TTL, single use. A reset invalidates the user's other reset tokens and revokes all their refresh tokens, atomically.
- **Input limits:** passwords are 8–128 **runes**, display names 1–80 runes, emails ≤254 bytes (normalized with trim + lower).
- **Error envelope:** `{"error":{"code":"…","message":"…","field":"…?","request_id":"…"}}`.
- **JSON bodies:** 1 MB cap, `DisallowUnknownFields`, exactly one JSON object.
- **Rate limits** (per instance, behind the `RateLimiter` port):
  - register: per IP 10/min
  - login: per (IP, email) 5/min, plus per IP 60/min
  - refresh: per IP 30/min
  - forgot and reset password: per email 3/hour, plus per IP 10/min
- **Logging:** slog JSON. Access logs record the path only, **never the query string**. Passwords and tokens are never logged.
- **Commits:** every commit message ends with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Before each commit, `gofmt -l .` must print nothing and `go vet ./...` must pass.

## Review Focus

These are inputs and failure modes the spec implies but no task would otherwise test. Each has a test in the task that owns it.
1. **Email typed differently at login.** `" ASHA@Example.com "` after registering `asha@example.com` must log into the same account → Task 4 `TestLogin`.
2. **Non-Latin text.** Devanagari names and passwords are length-checked in runes, not bytes: a 7-character Hindi password (21 bytes) is rejected and a 9-character one is accepted → Task 2 `TestValidatePassword`, Task 4 `TestRegisterAcceptsDevanagariNameAndPassword`.
3. **Malformed bodies.** An empty body, non-JSON, or a wrong-typed field like `"grade":"5"` must return 400 with a readable message, never 500 → Task 9 `TestDecodeJSON`, Task 10 `TestRegisterErrors`.
4. **`PATCH /v1/me` with `{}`** must return 200 with the profile unchanged → Task 4 `TestUpdateProfile`, Task 10 `TestAccountLifecycle`.
5. **Authorization header variants.** Lowercase `bearer` and extra spaces must authenticate; `Basic …`, bare `Bearer` and junk tokens must get 401 → Task 9 `TestBearerToken`, Task 10 `TestAuthHeaderHandling`.

---

## File Structure

| Layer | File | Responsibility |
|---|---|---|
| root | `go.mod`, `go.sum`, `.gitignore`, `.dockerignore`, `.env.example` | module, dependencies, hygiene, sample config |
| entity | `internal/entity/errors.go` | sentinel errors, `ValidationError` |
| entity | `internal/entity/user.go` | `User`, `Email` value object, password / display-name / grade rules |
| entity | `internal/entity/language.go` | supported tutoring languages |
| entity | `internal/entity/token.go` | `RefreshToken` and its replay rule |
| usecase | `internal/usecase/ports.go` | repository, transaction, crypto and mail ports + their input types |
| usecase | `internal/usecase/auth.go` | register, login, refresh, logout, forgot/reset password, authenticate |
| usecase | `internal/usecase/accounts.go` | profile read/update, account deletion |
| usecase | `internal/usecase/catalog.go` | language catalogue |
| adapter | `internal/adapter/repository/users.go`, `tokens.go`, `errors.go` | Postgres implementations of the repository ports |
| adapter | `internal/adapter/httpapi/respond.go`, `middleware.go`, `ratelimit.go` | JSON I/O, error mapping, middleware, `RateLimiter` port |
| adapter | `internal/adapter/httpapi/router.go`, `auth_handlers.go`, `account_handlers.go`, `admin.go` | REST controllers and admin endpoints |
| infrastructure | `internal/infrastructure/config/config.go` | env config + `.env` loader |
| infrastructure | `internal/infrastructure/database/database.go`, `tx.go`, `migrate.go`, `migrations/00001_init.sql` | pool, `TxManager`, migrations |
| infrastructure | `internal/infrastructure/database/dbtest/dbtest.go` | embedded Postgres + per-test databases |
| infrastructure | `internal/infrastructure/crypto/password.go`, `jwt.go`, `opaque.go` | argon2id, JWT, opaque tokens |
| infrastructure | `internal/infrastructure/mail/mail.go` | SMTP and log mailers |
| infrastructure | `internal/infrastructure/ratelimit/memory.go` | in-memory keyed token bucket |
| root of composition | `internal/bootstrap/bootstrap.go`, `roles.go` | role parsing, wiring, servers, graceful shutdown |
| arch | `internal/archtest/archtest_test.go` | dependency-rule test |
| cmd | `cmd/shiksha/main.go`, `cmd/devdb/main.go` | the binary (`serve`, `migrate`) and the local dev database |
| docs | `Dockerfile`, `docker-compose.yml`, `README.md` | packaging + docs (with Upcoming Features) |

---

### Task 1: Module skeleton and configuration

**Files:**
- Create: `go.mod`, `.gitignore`, `internal/infrastructure/config/config.go`
- Test: `internal/infrastructure/config/config_test.go`

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

Create `internal/infrastructure/config/config_test.go`:
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

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
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

Run: `go test ./internal/infrastructure/config/`
Expected: FAIL, the build fails because `config.LoadFrom` and the other functions are undefined.

- [ ] **Step 4: Implement the config package**

Create `internal/infrastructure/config/config.go`:
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

Run: `go mod tidy && go test -race ./internal/infrastructure/config/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config`

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum .gitignore internal/infrastructure/config
git commit -m "feat(config): env-based configuration with .env loader" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Entities and the dependency-rule test

**Files:**
- Create: `internal/entity/errors.go`, `internal/entity/user.go`, `internal/entity/language.go`, `internal/entity/token.go`
- Test: `internal/entity/entity_test.go`, `internal/archtest/archtest_test.go`

**Interfaces:**
- Produces:
  - Sentinel errors: `entity.ErrNotFound`, `ErrEmailTaken`, `ErrInvalidCredentials`, `ErrTokenInvalid`
  - `entity.ValidationError{Field, Message string}`
  - `entity.Email` (a string type), `entity.ParseEmail(raw string) (Email, error)`
  - Validation helpers:
    - `entity.ValidatePassword(field, plain string) error`
    - `entity.ParseDisplayName(raw string) (string, error)`
    - `entity.ValidateGrade(*int) error`
  - `entity.User{ID uuid.UUID; Email Email; PasswordHash, DisplayName, PreferredLang string; Grade *int; TermsAcceptedAt time.Time; GuardianConsentAt *time.Time; CreatedAt, UpdatedAt time.Time}`
  - Languages:
    - `entity.Language{Code, Name, NativeName string}`
    - `entity.DefaultLanguage = "hi"`
    - `entity.Languages() []Language`
    - `entity.LookupLanguage(code) (Language, bool)`
    - `entity.ValidateLanguage(code) error`
  - Refresh tokens:
    - `entity.RefreshToken{UserID, FamilyID uuid.UUID; ExpiresAt time.Time; UsedAt, RevokedAt *time.Time}`
    - `(RefreshToken).IsReplay(now time.Time, grace time.Duration) bool`

- [ ] **Step 1: Write the failing entity tests**

Run: `go get github.com/google/uuid@latest`

Create `internal/entity/entity_test.go`:
```go
package entity_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

func wantField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *entity.ValidationError
	if !errors.As(err, &ve) || ve.Field != field {
		t.Fatalf("err = %v, want validation error on %q", err, field)
	}
}

func TestParseEmail(t *testing.T) {
	got, err := entity.ParseEmail("  Asha@Example.COM ")
	if err != nil || got != "asha@example.com" {
		t.Fatalf("ParseEmail = %q, %v; want normalized asha@example.com", got, err)
	}
	for _, bad := range []string{
		"not-an-email",
		"Asha <asha@example.com>",
		"asha@localhost",
		strings.Repeat("a", 250) + "@example.com",
		"",
	} {
		_, err := entity.ParseEmail(bad)
		wantField(t, err, "email")
	}
}

func TestValidatePassword(t *testing.T) {
	for _, ok := range []string{"correct horse", "पासवर्ड१२" /* 9 runes, 27 bytes */, strings.Repeat("p", 128)} {
		if err := entity.ValidatePassword("password", ok); err != nil {
			t.Errorf("ValidatePassword(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"short", "पासवर्ड" /* 7 runes, 21 bytes */, strings.Repeat("p", 129)} {
		wantField(t, entity.ValidatePassword("new_password", bad), "new_password")
	}
}

func TestParseDisplayName(t *testing.T) {
	if got, err := entity.ParseDisplayName("  आशा "); err != nil || got != "आशा" {
		t.Fatalf("ParseDisplayName = %q, %v", got, err)
	}
	_, err := entity.ParseDisplayName("   ")
	wantField(t, err, "display_name")
	_, err = entity.ParseDisplayName(strings.Repeat("n", 81))
	wantField(t, err, "display_name")
}

func TestValidateGrade(t *testing.T) {
	for _, g := range []int{1, 12} {
		if err := entity.ValidateGrade(&g); err != nil {
			t.Errorf("grade %d rejected: %v", g, err)
		}
	}
	if err := entity.ValidateGrade(nil); err != nil {
		t.Errorf("nil grade rejected: %v", err)
	}
	for _, g := range []int{0, 13} {
		wantField(t, entity.ValidateGrade(&g), "grade")
	}
}

func TestLanguages(t *testing.T) {
	hi, ok := entity.LookupLanguage("hi")
	if !ok || hi.Name != "Hindi" || hi.NativeName != "हिन्दी" {
		t.Fatalf("LookupLanguage(hi) = %+v, %v", hi, ok)
	}
	if _, ok := entity.LookupLanguage("xx"); ok {
		t.Fatal("LookupLanguage(xx) found a language")
	}
	wantField(t, entity.ValidateLanguage("xx"), "preferred_lang")
	if err := entity.ValidateLanguage(entity.DefaultLanguage); err != nil {
		t.Fatalf("default language invalid: %v", err)
	}
	langs := entity.Languages()
	if len(langs) != 9 || langs[0].Code != "hi" || langs[8].Code != "en" {
		t.Fatalf("Languages() = %+v", langs)
	}
	langs[0].Code = "zz"
	if entity.Languages()[0].Code != "hi" {
		t.Fatal("modifying Languages() result changed the registry")
	}
}

func TestRefreshTokenIsReplay(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	used := func(ago time.Duration) *time.Time { t := now.Add(-ago); return &t }
	grace := 20 * time.Second
	cases := []struct {
		name  string
		token entity.RefreshToken
		want  bool
	}{
		{"never used", entity.RefreshToken{}, false},
		{"used inside grace", entity.RefreshToken{UsedAt: used(5 * time.Second)}, false},
		{"used exactly at grace", entity.RefreshToken{UsedAt: used(grace)}, false},
		{"used after grace", entity.RefreshToken{UsedAt: used(21 * time.Second)}, true},
	}
	for _, tc := range cases {
		if got := tc.token.IsReplay(now, grace); got != tc.want {
			t.Errorf("%s: IsReplay = %v, want %v", tc.name, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/entity/`
Expected: FAIL, the build fails because `entity.ParseEmail` and the other entity functions are undefined.

- [ ] **Step 3: Implement the entities**

Create `internal/entity/errors.go`:
```go
// Package entity holds the enterprise business rules. It imports only the standard library and uuid.
package entity

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrTokenInvalid       = errors.New("token invalid or expired")
)

// ValidationError reports a rejected input field. The HTTP adapter maps it to 400.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }
```

Create `internal/entity/user.go`:
```go
package entity

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MinPasswordRunes    = 8
	MaxPasswordRunes    = 128
	MaxDisplayNameRunes = 80
	maxEmailBytes       = 254
)

// Email is a normalized (trimmed, lower-case) email address. Build it with ParseEmail.
type Email string

func (e Email) String() string { return string(e) }

// ParseEmail normalizes raw and checks it is a bare address with a dotted domain.
func ParseEmail(raw string) (Email, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if len(email) > maxEmailBytes {
		return "", &ValidationError{Field: "email", Message: "is too long"}
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return "", &ValidationError{Field: "email", Message: "is not a valid email address"}
	}
	return Email(email), nil
}

// ValidatePassword enforces the password length policy in runes, so non-Latin passwords get the
// same limits as Latin ones. field names the input being checked (e.g. "password", "new_password").
func ValidatePassword(field, plain string) error {
	n := utf8.RuneCountInString(plain)
	if n < MinPasswordRunes {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must be at least %d characters", MinPasswordRunes)}
	}
	if n > MaxPasswordRunes {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must be at most %d characters", MaxPasswordRunes)}
	}
	return nil
}

// ParseDisplayName trims raw and checks it is 1-80 runes.
func ParseDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(name)
	if n == 0 {
		return "", &ValidationError{Field: "display_name", Message: "is required"}
	}
	if n > MaxDisplayNameRunes {
		return "", &ValidationError{Field: "display_name", Message: fmt.Sprintf("must be at most %d characters", MaxDisplayNameRunes)}
	}
	return name, nil
}

// ValidateGrade accepts nil (not given) or a school grade from 1 to 12.
func ValidateGrade(grade *int) error {
	if grade != nil && (*grade < 1 || *grade > 12) {
		return &ValidationError{Field: "grade", Message: "must be between 1 and 12"}
	}
	return nil
}

// User is a learner account.
type User struct {
	ID                uuid.UUID
	Email             Email
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

Create `internal/entity/language.go`:
```go
package entity

import "slices"

// DefaultLanguage is used when a learner does not choose one.
const DefaultLanguage = "hi"

// Language is a tutoring language. Codes are ISO 639-1, which is also what Bhashini uses.
type Language struct {
	Code       string
	Name       string
	NativeName string
}

var languages = []Language{
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

// Languages returns the supported languages in display order. The caller may modify the result.
func Languages() []Language { return slices.Clone(languages) }

// LookupLanguage returns the language with the given code.
func LookupLanguage(code string) (Language, bool) {
	for _, l := range languages {
		if l.Code == code {
			return l, true
		}
	}
	return Language{}, false
}

// ValidateLanguage checks code is a supported language.
func ValidateLanguage(code string) error {
	if _, ok := LookupLanguage(code); !ok {
		return &ValidationError{Field: "preferred_lang", Message: "is not a supported language"}
	}
	return nil
}
```

Create `internal/entity/token.go`:
```go
package entity

import (
	"time"

	"github.com/google/uuid"
)

// RefreshToken is one link in a rotation family. Only its hash is ever stored.
type RefreshToken struct {
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	ExpiresAt time.Time
	UsedAt    *time.Time // set when the token was rotated
	RevokedAt *time.Time
}

// IsReplay reports whether presenting this token at `now` signals theft: it was already rotated
// more than `grace` ago. Reuse inside the grace window is a benign race, such as two parallel
// refreshes when an app resumes or a client retry, and must not log the learner out.
func (t RefreshToken) IsReplay(now time.Time, grace time.Duration) bool {
	return t.UsedAt != nil && now.Sub(*t.UsedAt) > grace
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/entity/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/entity`

- [ ] **Step 5: Write the dependency-rule test**

Create `internal/archtest/archtest_test.go`:
```go
// Package archtest enforces the Clean Architecture dependency rule: source dependencies point inward.
package archtest

import (
	"bufio"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// moduleRoot walks up from the test's working directory to the directory holding go.mod
// and returns it with the module path.
func moduleRoot(t *testing.T) (dir, module string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		f, err := os.Open(filepath.Join(dir, "go.mod"))
		if err == nil {
			defer f.Close()
			s := bufio.NewScanner(f)
			for s.Scan() {
				if m, ok := strings.CutPrefix(strings.TrimSpace(s.Text()), "module "); ok {
					return dir, strings.TrimSpace(m)
				}
			}
			t.Fatal("go.mod has no module line")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestDependencyRule(t *testing.T) {
	root, module := moduleRoot(t)
	internal := module + "/internal/"
	rules := []struct {
		dir       string
		forbidden []string
	}{
		{"internal/entity", []string{internal}},
		{"internal/usecase", []string{internal + "adapter", internal + "infrastructure", internal + "bootstrap", internal + "archtest"}},
		{"internal/adapter", []string{internal + "bootstrap"}},
		{"internal/infrastructure", []string{internal + "adapter", internal + "bootstrap"}},
	}
	for _, rule := range rules {
		base := filepath.Join(root, filepath.FromSlash(rule.dir))
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				for _, bad := range rule.forbidden {
					if strings.HasPrefix(p, bad) {
						rel, _ := filepath.Rel(root, path)
						t.Errorf("%s imports %s: %s must not depend on %s", filepath.ToSlash(rel), p, rule.dir, strings.TrimPrefix(bad, module+"/"))
					}
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("walk %s: %v", rule.dir, err)
		}
	}
}
```

- [ ] **Step 6: Run the architecture test**

Run: `go test ./internal/archtest/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/archtest`. Directories that don't exist yet are skipped.

Then prove the test catches a violation. Temporarily create `internal/entity/zz_violation.go` containing:
```go
package entity

import _ "github.com/bitwizard25/Shiksh_AI/internal/archtest"
```
Run: `go test ./internal/archtest/`
Expected: FAIL, reporting `internal/entity/zz_violation.go imports .../internal/archtest: internal/entity must not depend on internal/`.
Then delete `internal/entity/zz_violation.go` and re-run to see `ok`.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/entity internal/archtest
git commit -m "feat(entity): user, language and refresh-token rules plus dependency-rule test" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Database infrastructure: pool, TxManager, migrations, test harness

**Files:**
- Create: `internal/infrastructure/database/database.go`, `tx.go`, `migrate.go`, `migrations/00001_init.sql`, `internal/infrastructure/database/dbtest/dbtest.go`
- Test: `internal/infrastructure/database/main_test.go`, `internal/infrastructure/database/database_test.go`

**Interfaces:**
- Produces:
  - `database.DBTX` interface
  - `database.Open(ctx, databaseURL string) (*pgxpool.Pool, error)`
  - `database.Migrate(ctx, *pgxpool.Pool) error`
  - `database.NewTxManager(*pgxpool.Pool) *database.TxManager`, whose method `WithinTx(ctx, fn func(ctx context.Context) error) error` satisfies `usecase.TxManager` (Task 4)
  - `database.Conn(ctx, *pgxpool.Pool) database.DBTX`
  - `dbtest.Main(*testing.M) int`, `dbtest.NewPool(testing.TB) *pgxpool.Pool`

- [ ] **Step 1: Add dependencies**

Run:
```bash
go get github.com/jackc/pgx/v5@latest github.com/pressly/goose/v3@latest github.com/fergusstrange/embedded-postgres@latest
```

- [ ] **Step 2: Write the migration (full schema from spec §6)**

Create `internal/infrastructure/database/migrations/00001_init.sql`:
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

- [ ] **Step 3: Write the pool, transaction manager and migrator**

Create `internal/infrastructure/database/database.go`:
```go
// Package database provides the Postgres connection pool, transactions and migrations.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Open connects to Postgres, retrying for a while so the service can start alongside the database.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
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
			return pool, nil
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
```

Create `internal/infrastructure/database/tx.go`:
```go
package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type txKey struct{}

// TxManager runs functions inside one database transaction carried in the context.
// It satisfies usecase.TxManager, so use cases get atomicity without seeing SQL.
type TxManager struct{ pool *pgxpool.Pool }

func NewTxManager(pool *pgxpool.Pool) *TxManager { return &TxManager{pool: pool} }

// WithinTx runs fn in a transaction, committing when fn returns nil and rolling back otherwise.
// A nested call joins the outer transaction.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}
	return pgx.BeginFunc(ctx, m.pool, func(tx pgx.Tx) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// Conn returns the transaction carried in ctx, or pool when there is none. Repositories call it
// for every query so they automatically take part in a use case's transaction.
func Conn(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}
```

Create `internal/infrastructure/database/migrate.go`:
```go
package database

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

Create `internal/infrastructure/database/dbtest/dbtest.go`:
```go
// Package dbtest provides throwaway, fully migrated Postgres databases for tests.
//
// By default it starts an embedded Postgres, so no Docker is needed. The first run downloads the
// Postgres binaries (~20 MB) into ~/.embedded-postgres-go. Set TEST_DATABASE_URL to use an
// existing server instead; that role needs the CREATEDB privilege.
package dbtest

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

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
)

var (
	serverURL    string     // maintenance database on the test server
	templateName string     // migrated template cloned for each test
	createMu     sync.Mutex // CREATE DATABASE ... TEMPLATE must not run concurrently
	dbCounter    atomic.Int64
)

// Main starts the database server, builds the migrated template, runs the tests and cleans up.
// Call it from TestMain: func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func Main(m *testing.M) int {
	ctx := context.Background()
	stop := func() {}
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		serverURL = u
	} else {
		u, stopEmbedded, err := startEmbedded()
		if err != nil {
			fmt.Fprintln(os.Stderr, "dbtest: start embedded postgres:", err)
			return 1
		}
		serverURL, stop = u, stopEmbedded
	}
	defer stop()

	templateName = fmt.Sprintf("shiksha_tpl_%d", os.Getpid())
	if err := createTemplate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest: create template:", err)
		return 1
	}
	defer dropDatabase(ctx, templateName)

	return m.Run()
}

// NewPool returns a pool on a fresh database cloned from the migrated template.
// The database is dropped when the test ends.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), dbCounter.Add(1))

	createMu.Lock()
	err := execAdmin(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateName))
	createMu.Unlock()
	if err != nil {
		t.Fatalf("dbtest: create database: %v", err)
	}

	pool, err := pgxpool.New(ctx, databaseURL(name))
	if err != nil {
		t.Fatalf("dbtest: connect: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropDatabase(ctx, name)
	})
	return pool
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
	return database.Migrate(ctx, pool)
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
		panic(fmt.Sprintf("dbtest: bad server URL: %v", err))
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

Create `internal/infrastructure/database/main_test.go`:
```go
package database_test

import (
	"os"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
```

Create `internal/infrastructure/database/database_test.go`:
```go
package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestMigrationsCreateSchema(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	for _, table := range []string{"users", "refresh_tokens", "password_reset_tokens", "tutoring_sessions", "ws_tickets", "messages"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s was not created", table)
		}
	}
}

func TestMigrateIsIdempotentAndLeavesPoolUsable(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool unusable after Migrate: %v", err)
	}
}

func TestTxManager(t *testing.T) {
	pool := dbtest.NewPool(t)
	tm := database.NewTxManager(pool)
	ctx := context.Background()
	boom := errors.New("boom")

	insert := func(ctx context.Context, email string) error {
		_, err := database.Conn(ctx, pool).Exec(ctx,
			`INSERT INTO users (email, password_hash, display_name, terms_accepted_at) VALUES ($1, 'h', 'n', now())`, email)
		return err
	}
	count := func(email string) int { // always reads through the pool, i.e. outside any transaction
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = $1`, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("commits", func(t *testing.T) {
		if err := tm.WithinTx(ctx, func(ctx context.Context) error { return insert(ctx, "commit@example.com") }); err != nil {
			t.Fatal(err)
		}
		if count("commit@example.com") != 1 {
			t.Fatal("committed row missing")
		}
	})
	t.Run("uses the transaction", func(t *testing.T) {
		err := tm.WithinTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, "isolated@example.com"); err != nil {
				return err
			}
			if n := count("isolated@example.com"); n != 0 {
				t.Errorf("uncommitted row visible outside the transaction (%d)", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rolls back on error", func(t *testing.T) {
		err := tm.WithinTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, "rollback@example.com"); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) || count("rollback@example.com") != 0 {
			t.Fatalf("err = %v, rows = %d; want boom and 0", err, count("rollback@example.com"))
		}
	})
	t.Run("nested call joins the outer transaction", func(t *testing.T) {
		err := tm.WithinTx(ctx, func(ctx context.Context) error {
			if err := tm.WithinTx(ctx, func(ctx context.Context) error { return insert(ctx, "inner@example.com") }); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) || count("inner@example.com") != 0 {
			t.Fatalf("inner write survived outer rollback: err=%v rows=%d", err, count("inner@example.com"))
		}
	})
	t.Run("Conn without a transaction uses the pool", func(t *testing.T) {
		if err := insert(ctx, "direct@example.com"); err != nil || count("direct@example.com") != 1 {
			t.Fatalf("direct insert: err=%v rows=%d", err, count("direct@example.com"))
		}
	})
}
```

- [ ] **Step 6: Run the tests**

Run: `go mod tidy && go test -race ./internal/infrastructure/database/...`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database`. The first run downloads Postgres and can take about a minute.
- If `pg.Start()` fails with an initdb locale error on Windows, add `.Locale("C")` to the config chain in `startEmbedded` and re-run.
- Always run this package on its own once before running the whole suite in parallel, so the binary download happens once.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/infrastructure/database
git commit -m "feat(database): pgx pool, context-carried TxManager, embedded migrations and test harness" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Use-case ports, registration, login and account management

**Files:**
- Create: `internal/usecase/ports.go`, `internal/usecase/auth.go`, `internal/usecase/accounts.go`, `internal/usecase/catalog.go`
- Test: `internal/usecase/fakes_test.go`, `internal/usecase/auth_test.go`, `internal/usecase/accounts_test.go`

**Interfaces:**
- Consumes: everything from `entity` (Task 2)
- Produces (Task 5 extends `Auth`; Tasks 6–8 and 10–11 implement or use these):
  - Port input types:
    - `usecase.NewUser{Email entity.Email; PasswordHash, DisplayName, PreferredLang string; Grade *int; AcceptedAt time.Time; GuardianConsent bool}`
    - `usecase.ProfilePatch{DisplayName, PreferredLang *string; Grade *int}`
  - `usecase.UserRepository`:
    - `Create(ctx, NewUser) (entity.User, error)`
    - `GetByEmail(ctx, entity.Email) (entity.User, error)`
    - `GetByID(ctx, uuid.UUID) (entity.User, error)`
    - `UpdateProfile(ctx, uuid.UUID, ProfilePatch, time.Time) (entity.User, error)`
    - `UpdatePassword(ctx, uuid.UUID, string, time.Time) error`
    - `Delete(ctx, uuid.UUID) error`
  - `usecase.TokenRepository`:
    - `CreateRefresh(ctx, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error`
    - `ConsumeRefresh(ctx, hash []byte, now time.Time) (entity.RefreshToken, error)`
    - `FindRefresh(ctx, hash []byte) (entity.RefreshToken, error)`
    - `RevokeFamily(ctx, familyID uuid.UUID, now time.Time) error`
    - `RevokeFamilyOf(ctx, hash []byte, now time.Time) error`
    - `RevokeAllForUser(ctx, userID uuid.UUID, now time.Time) error`
    - `CreatePasswordReset(ctx, userID uuid.UUID, hash []byte, expiresAt time.Time) error`
    - `ConsumePasswordReset(ctx, hash []byte, now time.Time) (uuid.UUID, error)`
    - `InvalidatePasswordResets(ctx, userID uuid.UUID, now time.Time) error`
  - `usecase.TxManager`: `WithinTx(ctx, func(ctx context.Context) error) error`
  - `usecase.PasswordHasher`: `Hash(ctx, plain) (string, error)`, `Verify(ctx, plain, encoded) (bool, error)`, `VerifyDummy(ctx, plain)`
  - `usecase.AccessTokens`: `Issue(uuid.UUID) (string, time.Duration, error)`, `Verify(string) (uuid.UUID, error)`
  - `usecase.OpaqueTokens`: `New() (plain string, hash []byte, err error)`, `Hash(plain string) []byte`
  - `usecase.Message{To, Subject, Body string}`, `usecase.Mailer`: `Send(ctx, Message) error`
  - Auth interactor:
    - `usecase.AuthConfig{RefreshTTL, RefreshReuseGrace, ResetTTL time.Duration; AppBaseURL string}`
    - `usecase.AuthDeps{Users UserRepository; Tokens TokenRepository; Tx TxManager; Hasher PasswordHasher; Access AccessTokens; Opaque OpaqueTokens; Mailer Mailer; Now func() time.Time}`
    - `usecase.NewAuth(AuthDeps, AuthConfig) *usecase.Auth`
    - `usecase.TokenPair{AccessToken, RefreshToken string; ExpiresIn time.Duration}`
    - `usecase.RegisterInput{Email, Password, DisplayName, PreferredLang string; Grade *int; TermsAccepted, GuardianConsent bool}`
    - Methods: `(*Auth).Register(ctx, RegisterInput) (entity.User, TokenPair, error)`, `Login(ctx, email, password string) (entity.User, TokenPair, error)`, `Authenticate(accessToken string) (uuid.UUID, error)`
  - Accounts interactor:
    - `usecase.NewAccounts(UserRepository, PasswordHasher, now func() time.Time) *usecase.Accounts`
    - `usecase.ProfileInput{DisplayName, PreferredLang *string; Grade *int}`
    - Methods: `(*Accounts).Me(ctx, uuid.UUID) (entity.User, error)`, `UpdateProfile(ctx, uuid.UUID, ProfileInput) (entity.User, error)`, `DeleteAccount(ctx, uuid.UUID, password string) error`
  - `usecase.Languages() []entity.Language`

- [ ] **Step 1: Write the in-memory fakes used by use-case tests**

Create `internal/usecase/fakes_test.go`:
```go
package usecase_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// memUsers is an in-memory UserRepository with the same contract as the Postgres one.
type memUsers struct {
	mu   sync.Mutex
	byID map[uuid.UUID]entity.User
}

func (m *memUsers) Create(_ context.Context, u usecase.NewUser) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.byID {
		if existing.Email == u.Email {
			return entity.User{}, entity.ErrEmailTaken
		}
	}
	user := entity.User{
		ID: uuid.New(), Email: u.Email, PasswordHash: u.PasswordHash, DisplayName: u.DisplayName,
		PreferredLang: u.PreferredLang, Grade: u.Grade,
		TermsAcceptedAt: u.AcceptedAt, CreatedAt: u.AcceptedAt, UpdatedAt: u.AcceptedAt,
	}
	if u.GuardianConsent {
		at := u.AcceptedAt
		user.GuardianConsentAt = &at
	}
	m.byID[user.ID] = user
	return user, nil
}

func (m *memUsers) GetByEmail(_ context.Context, email entity.Email) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.byID {
		if u.Email == email {
			return u, nil
		}
	}
	return entity.User{}, entity.ErrNotFound
}

func (m *memUsers) GetByID(_ context.Context, id uuid.UUID) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return entity.User{}, entity.ErrNotFound
	}
	return u, nil
}

func (m *memUsers) UpdateProfile(_ context.Context, id uuid.UUID, p usecase.ProfilePatch, now time.Time) (entity.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return entity.User{}, entity.ErrNotFound
	}
	if p.DisplayName != nil {
		u.DisplayName = *p.DisplayName
	}
	if p.PreferredLang != nil {
		u.PreferredLang = *p.PreferredLang
	}
	if p.Grade != nil {
		g := *p.Grade
		u.Grade = &g
	}
	u.UpdatedAt = now
	m.byID[id] = u
	return u, nil
}

func (m *memUsers) UpdatePassword(_ context.Context, id uuid.UUID, hash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return entity.ErrNotFound
	}
	u.PasswordHash, u.UpdatedAt = hash, now
	m.byID[id] = u
	return nil
}

func (m *memUsers) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return entity.ErrNotFound
	}
	delete(m.byID, id)
	return nil
}

// memTokens is an in-memory TokenRepository with the same contract as the Postgres one.
type memTokens struct {
	mu      sync.Mutex
	refresh map[string]*entity.RefreshToken
	resets  map[string]*memReset
}

type memReset struct {
	userID    uuid.UUID
	expiresAt time.Time
	usedAt    *time.Time
}

func (m *memTokens) CreateRefresh(_ context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh[string(hash)] = &entity.RefreshToken{UserID: userID, FamilyID: familyID, ExpiresAt: expiresAt}
	return nil
}

func (m *memTokens) ConsumeRefresh(_ context.Context, hash []byte, now time.Time) (entity.RefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.refresh[string(hash)]
	if !ok || tok.UsedAt != nil || tok.RevokedAt != nil || !now.Before(tok.ExpiresAt) {
		return entity.RefreshToken{}, entity.ErrTokenInvalid
	}
	used := now
	tok.UsedAt = &used
	return *tok, nil
}

func (m *memTokens) FindRefresh(_ context.Context, hash []byte) (entity.RefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.refresh[string(hash)]
	if !ok {
		return entity.RefreshToken{}, entity.ErrNotFound
	}
	return *tok, nil
}

func (m *memTokens) RevokeFamily(_ context.Context, familyID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokeWhere(func(t *entity.RefreshToken) bool { return t.FamilyID == familyID }, now)
	return nil
}

func (m *memTokens) RevokeFamilyOf(_ context.Context, hash []byte, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.refresh[string(hash)]
	if !ok {
		return nil
	}
	family := tok.FamilyID
	m.revokeWhere(func(t *entity.RefreshToken) bool { return t.FamilyID == family }, now)
	return nil
}

func (m *memTokens) RevokeAllForUser(_ context.Context, userID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokeWhere(func(t *entity.RefreshToken) bool { return t.UserID == userID }, now)
	return nil
}

func (m *memTokens) revokeWhere(match func(*entity.RefreshToken) bool, now time.Time) {
	for _, t := range m.refresh {
		if t.RevokedAt == nil && match(t) {
			at := now
			t.RevokedAt = &at
		}
	}
}

func (m *memTokens) CreatePasswordReset(_ context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resets[string(hash)] = &memReset{userID: userID, expiresAt: expiresAt}
	return nil
}

func (m *memTokens) ConsumePasswordReset(_ context.Context, hash []byte, now time.Time) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resets[string(hash)]
	if !ok || r.usedAt != nil || !now.Before(r.expiresAt) {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	used := now
	r.usedAt = &used
	return r.userID, nil
}

func (m *memTokens) InvalidatePasswordResets(_ context.Context, userID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.resets {
		if r.userID == userID && r.usedAt == nil {
			used := now
			r.usedAt = &used
		}
	}
	return nil
}

// noTx runs fn directly: the in-memory fakes need no transaction. Atomicity is tested against
// Postgres in the repository tests.
type noTx struct{}

func (noTx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

// fakeHasher "hashes" by prefixing and counts dummy verifications.
type fakeHasher struct{ dummyCalls atomic.Int32 }

func (h *fakeHasher) Hash(_ context.Context, plain string) (string, error) { return "hashed:" + plain, nil }

func (h *fakeHasher) Verify(_ context.Context, plain, encoded string) (bool, error) {
	return encoded == "hashed:"+plain, nil
}

func (h *fakeHasher) VerifyDummy(context.Context, string) { h.dummyCalls.Add(1) }

// fakeAccess issues readable access tokens: "access:<user id>".
type fakeAccess struct{}

func (fakeAccess) Issue(id uuid.UUID) (string, time.Duration, error) {
	return "access:" + id.String(), 15 * time.Minute, nil
}

func (fakeAccess) Verify(token string) (uuid.UUID, error) {
	s, ok := strings.CutPrefix(token, "access:")
	if !ok {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	return id, nil
}

// fakeOpaque issues sequential tokens "tok-1", "tok-2", ... with hash "h:<token>".
type fakeOpaque struct{ n atomic.Int64 }

func (o *fakeOpaque) New() (string, []byte, error) {
	plain := fmt.Sprintf("tok-%d", o.n.Add(1))
	return plain, o.Hash(plain), nil
}

func (o *fakeOpaque) Hash(plain string) []byte { return []byte("h:" + plain) }

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

func (c *captureMailer) last(t *testing.T) usecase.Message {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		t.Fatal("no email was sent")
	}
	return c.sent[len(c.sent)-1]
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

const (
	refreshTTL = 30 * 24 * time.Hour
	reuseGrace = 20 * time.Second
	resetTTL   = 30 * time.Minute
)

// env is a fully wired use-case layer on fakes.
type env struct {
	auth     *usecase.Auth
	accounts *usecase.Accounts
	users    *memUsers
	tokens   *memTokens
	hasher   *fakeHasher
	mailer   *captureMailer
	clock    *fakeClock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		users:  &memUsers{byID: map[uuid.UUID]entity.User{}},
		tokens: &memTokens{refresh: map[string]*entity.RefreshToken{}, resets: map[string]*memReset{}},
		hasher: &fakeHasher{},
		mailer: &captureMailer{},
		clock:  &fakeClock{now: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)},
	}
	e.auth = usecase.NewAuth(usecase.AuthDeps{
		Users: e.users, Tokens: e.tokens, Tx: noTx{}, Hasher: e.hasher, Access: fakeAccess{},
		Opaque: &fakeOpaque{}, Mailer: e.mailer, Now: e.clock.Now,
	}, usecase.AuthConfig{RefreshTTL: refreshTTL, RefreshReuseGrace: reuseGrace, ResetTTL: resetTTL, AppBaseURL: "https://app.example/"})
	e.accounts = usecase.NewAccounts(e.users, e.hasher, e.clock.Now)
	return e
}

func validRegistration() usecase.RegisterInput {
	return usecase.RegisterInput{Email: "asha@example.com", Password: "correct horse", DisplayName: "Asha", TermsAccepted: true}
}

func mustRegister(t *testing.T, e *env) (entity.User, usecase.TokenPair) {
	t.Helper()
	user, pair, err := e.auth.Register(context.Background(), validRegistration())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return user, pair
}
```

- [ ] **Step 2: Write the failing registration, login and account tests**

Create `internal/usecase/auth_test.go`:
```go
package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestRegisterCreatesUserAndTokens(t *testing.T) {
	e := newEnv(t)
	in := validRegistration()
	in.Email = "  Asha@Example.com "
	in.GuardianConsent = true
	user, pair, err := e.auth.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.Email != "asha@example.com" || user.PreferredLang != entity.DefaultLanguage || user.PasswordHash != "hashed:correct horse" {
		t.Fatalf("user = %+v", user)
	}
	if !user.TermsAcceptedAt.Equal(e.clock.Now()) || user.GuardianConsentAt == nil {
		t.Fatalf("consent timestamps = %v, %v", user.TermsAcceptedAt, user.GuardianConsentAt)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.ExpiresIn != 15*time.Minute {
		t.Fatalf("pair = %+v", pair)
	}
	if id, err := e.auth.Authenticate(pair.AccessToken); err != nil || id != user.ID {
		t.Fatalf("Authenticate = %v, %v; want %v", id, err, user.ID)
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	e := newEnv(t)
	mustRegister(t, e)
	in := validRegistration()
	in.Email = "ASHA@example.com"
	if _, _, err := e.auth.Register(context.Background(), in); !errors.Is(err, entity.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := newEnv(t)
	grade13 := 13
	cases := []struct {
		name   string
		mutate func(*usecase.RegisterInput)
		field  string
	}{
		{"bad email", func(in *usecase.RegisterInput) { in.Email = "not-an-email" }, "email"},
		{"short password", func(in *usecase.RegisterInput) { in.Password = "short" }, "password"},
		{"blank name", func(in *usecase.RegisterInput) { in.DisplayName = "   " }, "display_name"},
		{"unsupported lang", func(in *usecase.RegisterInput) { in.PreferredLang = "xx" }, "preferred_lang"},
		{"grade out of range", func(in *usecase.RegisterInput) { in.Grade = &grade13 }, "grade"},
		{"terms not accepted", func(in *usecase.RegisterInput) { in.TermsAccepted = false }, "terms_accepted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validRegistration()
			tc.mutate(&in)
			_, _, err := e.auth.Register(context.Background(), in)
			var ve *entity.ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("err = %v, want validation error on %q", err, tc.field)
			}
		})
	}
}

func TestRegisterAcceptsDevanagariNameAndPassword(t *testing.T) {
	e := newEnv(t)
	in := validRegistration()
	in.DisplayName = "आशा"
	in.Password = "पासवर्ड१२" // 9 runes, 27 bytes
	user, _, err := e.auth.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.DisplayName != "आशा" {
		t.Fatalf("DisplayName = %q", user.DisplayName)
	}
	if _, _, err := e.auth.Login(context.Background(), in.Email, in.Password); err != nil {
		t.Fatalf("Login with Devanagari password: %v", err)
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user, _ := mustRegister(t, e)

	got, pair, err := e.auth.Login(ctx, " ASHA@Example.com ", "correct horse")
	if err != nil || got.ID != user.ID || pair.RefreshToken == "" {
		t.Fatalf("Login with differently typed email = %+v, %+v, %v", got, pair, err)
	}
	if _, _, err := e.auth.Login(ctx, "asha@example.com", "wrong horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}

	before := e.hasher.dummyCalls.Load()
	if _, _, err := e.auth.Login(ctx, "nobody@example.com", "correct horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v", err)
	}
	if _, _, err := e.auth.Login(ctx, "not an email", "correct horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("malformed email err = %v", err)
	}
	if got := e.hasher.dummyCalls.Load() - before; got != 2 {
		t.Fatalf("VerifyDummy called %d times for unknown/malformed emails, want 2 (timing equalization)", got)
	}
}

func TestAuthenticateRejectsGarbage(t *testing.T) {
	e := newEnv(t)
	if _, err := e.auth.Authenticate("garbage"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}
```

Create `internal/usecase/accounts_test.go`:
```go
package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestUpdateProfile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user, _ := mustRegister(t, e)

	name, code, grade := " Asha K ", "mr", 8
	got, err := e.accounts.UpdateProfile(ctx, user.ID, usecase.ProfileInput{DisplayName: &name, PreferredLang: &code, Grade: &grade})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if got.DisplayName != "Asha K" || got.PreferredLang != "mr" || got.Grade == nil || *got.Grade != 8 {
		t.Fatalf("got = %+v", got)
	}

	bad := "xx"
	var ve *entity.ValidationError
	if _, err := e.accounts.UpdateProfile(ctx, user.ID, usecase.ProfileInput{PreferredLang: &bad}); !errors.As(err, &ve) || ve.Field != "preferred_lang" {
		t.Fatalf("bad lang err = %v", err)
	}

	same, err := e.accounts.UpdateProfile(ctx, user.ID, usecase.ProfileInput{})
	if err != nil || same.DisplayName != "Asha K" || same.PreferredLang != "mr" || *same.Grade != 8 {
		t.Fatalf("empty update = %+v, %v; want unchanged", same, err)
	}
	if _, err := e.accounts.UpdateProfile(ctx, uuid.New(), usecase.ProfileInput{}); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("unknown user err = %v, want ErrNotFound", err)
	}
}

func TestDeleteAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user, _ := mustRegister(t, e)

	if err := e.accounts.DeleteAccount(ctx, user.ID, "wrong horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}
	if err := e.accounts.DeleteAccount(ctx, user.ID, "correct horse"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, err := e.accounts.Me(ctx, user.ID); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("Me after delete err = %v, want ErrNotFound", err)
	}
}

func TestLanguagesCatalog(t *testing.T) {
	langs := usecase.Languages()
	if len(langs) != 9 || langs[0].Code != "hi" {
		t.Fatalf("Languages() = %+v", langs)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/usecase/`
Expected: FAIL, the build fails because `usecase.NewAuth`, `usecase.NewUser` and the other types are undefined.

- [ ] **Step 4: Write the ports**

Create `internal/usecase/ports.go`:
```go
// Package usecase holds the application business rules (interactors) and the ports they need.
// It imports only internal/entity from this module; outer layers implement the ports.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// NewUser is the input to UserRepository.Create.
type NewUser struct {
	Email           entity.Email
	PasswordHash    string
	DisplayName     string
	PreferredLang   string
	Grade           *int
	AcceptedAt      time.Time // terms (and guardian consent, when given) were accepted at this time
	GuardianConsent bool
}

// ProfilePatch changes only its non-nil fields.
type ProfilePatch struct {
	DisplayName   *string
	PreferredLang *string
	Grade         *int
}

// UserRepository persists learner accounts.
type UserRepository interface {
	// Create returns entity.ErrEmailTaken if the email is already registered.
	Create(ctx context.Context, u NewUser) (entity.User, error)
	// GetByEmail and GetByID return entity.ErrNotFound if there is no such user.
	GetByEmail(ctx context.Context, email entity.Email) (entity.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (entity.User, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, p ProfilePatch, now time.Time) (entity.User, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string, now time.Time) error
	// Delete removes the user and everything they own. entity.ErrNotFound if absent.
	Delete(ctx context.Context, id uuid.UUID) error
}

// TokenRepository persists refresh and password-reset tokens by their hashes.
type TokenRepository interface {
	CreateRefresh(ctx context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error
	// ConsumeRefresh marks an unused, unrevoked, unexpired token as used at now and returns it.
	// Any other token gets entity.ErrTokenInvalid. At most one concurrent caller succeeds.
	ConsumeRefresh(ctx context.Context, hash []byte, now time.Time) (entity.RefreshToken, error)
	// FindRefresh returns the token in any state, or entity.ErrNotFound.
	FindRefresh(ctx context.Context, hash []byte) (entity.RefreshToken, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID, now time.Time) error
	// RevokeFamilyOf revokes the family of the token with this hash; unknown hashes are a no-op.
	RevokeFamilyOf(ctx context.Context, hash []byte, now time.Time) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID, now time.Time) error
	CreatePasswordReset(ctx context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error
	// ConsumePasswordReset marks a valid reset token used and returns its user, or entity.ErrTokenInvalid.
	ConsumePasswordReset(ctx context.Context, hash []byte, now time.Time) (uuid.UUID, error)
	InvalidatePasswordResets(ctx context.Context, userID uuid.UUID, now time.Time) error
}

// TxManager runs fn atomically. Repositories called with the ctx passed to fn join the transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// PasswordHasher hashes and verifies passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, plain string) (string, error)
	Verify(ctx context.Context, plain, encoded string) (bool, error)
	// VerifyDummy costs as much as Verify; call it when no account exists so timing reveals nothing.
	VerifyDummy(ctx context.Context, plain string)
}

// AccessTokens issues and verifies short-lived access tokens.
type AccessTokens interface {
	Issue(userID uuid.UUID) (token string, ttl time.Duration, err error)
	// Verify returns entity.ErrTokenInvalid for any invalid or expired token.
	Verify(token string) (uuid.UUID, error)
}

// OpaqueTokens creates random secrets (refresh and reset tokens) and hashes them for storage.
type OpaqueTokens interface {
	New() (plain string, hash []byte, err error)
	Hash(plain string) []byte
}

// Message is a plain-text email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Mailer sends email.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}
```

- [ ] **Step 5: Write the interactors**

Create `internal/usecase/auth.go`:
```go
package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// AuthConfig tunes token lifetimes and links.
type AuthConfig struct {
	RefreshTTL        time.Duration // lifetime of each refresh token (720h)
	RefreshReuseGrace time.Duration // reuse inside this window does not revoke the family (20s)
	ResetTTL          time.Duration // lifetime of password reset links (30m)
	AppBaseURL        string        // reset links point at <AppBaseURL>/reset?token=...
}

// AuthDeps are the ports the Auth interactor needs.
type AuthDeps struct {
	Users  UserRepository
	Tokens TokenRepository
	Tx     TxManager
	Hasher PasswordHasher
	Access AccessTokens
	Opaque OpaqueTokens
	Mailer Mailer
	Now    func() time.Time // defaults to time.Now
}

// Auth is the authentication interactor: sign-up, sign-in, token rotation and password reset.
type Auth struct {
	d   AuthDeps
	cfg AuthConfig
}

func NewAuth(d AuthDeps, cfg AuthConfig) *Auth {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Auth{d: d, cfg: cfg}
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
	PreferredLang   string // defaults to entity.DefaultLanguage
	Grade           *int
	TermsAccepted   bool
	GuardianConsent bool
}

// Register validates the form, creates the account and its first refresh token atomically, and signs the user in.
func (a *Auth) Register(ctx context.Context, in RegisterInput) (entity.User, TokenPair, error) {
	email, err := entity.ParseEmail(in.Email)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if err := entity.ValidatePassword("password", in.Password); err != nil {
		return entity.User{}, TokenPair{}, err
	}
	name, err := entity.ParseDisplayName(in.DisplayName)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if in.PreferredLang == "" {
		in.PreferredLang = entity.DefaultLanguage
	}
	if err := entity.ValidateLanguage(in.PreferredLang); err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if err := entity.ValidateGrade(in.Grade); err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if !in.TermsAccepted {
		return entity.User{}, TokenPair{}, &entity.ValidationError{Field: "terms_accepted", Message: "must be accepted"}
	}

	hash, err := a.d.Hasher.Hash(ctx, in.Password)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	now := a.d.Now()
	var (
		user entity.User
		pair TokenPair
	)
	err = a.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		user, err = a.d.Users.Create(ctx, NewUser{
			Email: email, PasswordHash: hash, DisplayName: name, PreferredLang: in.PreferredLang,
			Grade: in.Grade, AcceptedAt: now, GuardianConsent: in.GuardianConsent,
		})
		if err != nil {
			return err
		}
		pair, err = a.issuePair(ctx, user.ID, now)
		return err
	})
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	return user, pair, nil
}

// Login checks credentials. Unknown or malformed emails cost the same time as a wrong password.
func (a *Auth) Login(ctx context.Context, email, password string) (entity.User, TokenPair, error) {
	addr, err := entity.ParseEmail(email)
	if err != nil {
		a.d.Hasher.VerifyDummy(ctx, password)
		return entity.User{}, TokenPair{}, entity.ErrInvalidCredentials
	}
	user, err := a.d.Users.GetByEmail(ctx, addr)
	if errors.Is(err, entity.ErrNotFound) {
		a.d.Hasher.VerifyDummy(ctx, password)
		return entity.User{}, TokenPair{}, entity.ErrInvalidCredentials
	}
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	ok, err := a.d.Hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	if !ok {
		return entity.User{}, TokenPair{}, entity.ErrInvalidCredentials
	}
	pair, err := a.issuePair(ctx, user.ID, a.d.Now())
	if err != nil {
		return entity.User{}, TokenPair{}, err
	}
	return user, pair, nil
}

// Authenticate validates an access token and returns the user id.
func (a *Auth) Authenticate(accessToken string) (uuid.UUID, error) {
	return a.d.Access.Verify(accessToken)
}

// issuePair creates an access token and the first refresh token of a new rotation family.
func (a *Auth) issuePair(ctx context.Context, userID uuid.UUID, now time.Time) (TokenPair, error) {
	access, ttl, err := a.d.Access.Issue(userID)
	if err != nil {
		return TokenPair{}, err
	}
	plain, hash, err := a.d.Opaque.New()
	if err != nil {
		return TokenPair{}, err
	}
	if err := a.d.Tokens.CreateRefresh(ctx, userID, uuid.New(), hash, now.Add(a.cfg.RefreshTTL)); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, ExpiresIn: ttl}, nil
}
```

Create `internal/usecase/accounts.go`:
```go
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// ProfileInput changes only its non-nil fields.
type ProfileInput struct {
	DisplayName   *string
	PreferredLang *string
	Grade         *int
}

// Accounts is the interactor for a signed-in learner's own account.
type Accounts struct {
	users  UserRepository
	hasher PasswordHasher
	now    func() time.Time
}

// NewAccounts builds the interactor. now defaults to time.Now.
func NewAccounts(users UserRepository, hasher PasswordHasher, now func() time.Time) *Accounts {
	if now == nil {
		now = time.Now
	}
	return &Accounts{users: users, hasher: hasher, now: now}
}

// Me returns the learner's profile.
func (a *Accounts) Me(ctx context.Context, userID uuid.UUID) (entity.User, error) {
	return a.users.GetByID(ctx, userID)
}

// UpdateProfile validates and applies the non-nil fields.
func (a *Accounts) UpdateProfile(ctx context.Context, userID uuid.UUID, in ProfileInput) (entity.User, error) {
	var patch ProfilePatch
	if in.DisplayName != nil {
		name, err := entity.ParseDisplayName(*in.DisplayName)
		if err != nil {
			return entity.User{}, err
		}
		patch.DisplayName = &name
	}
	if in.PreferredLang != nil {
		if err := entity.ValidateLanguage(*in.PreferredLang); err != nil {
			return entity.User{}, err
		}
		patch.PreferredLang = in.PreferredLang
	}
	if in.Grade != nil {
		if err := entity.ValidateGrade(in.Grade); err != nil {
			return entity.User{}, err
		}
		patch.Grade = in.Grade
	}
	return a.users.UpdateProfile(ctx, userID, patch, a.now())
}

// DeleteAccount permanently deletes the account after re-checking the password.
func (a *Accounts) DeleteAccount(ctx context.Context, userID uuid.UUID, password string) error {
	user, err := a.users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := a.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return entity.ErrInvalidCredentials
	}
	return a.users.Delete(ctx, userID)
}
```

Create `internal/usecase/catalog.go`:
```go
package usecase

import "github.com/bitwizard25/Shiksh_AI/internal/entity"

// Languages lists the languages the tutor speaks, in display order.
func Languages() []entity.Language { return entity.Languages() }
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/usecase/ ./internal/archtest/`
Expected: both packages print `ok`. The archtest now also checks `internal/usecase`.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/usecase
git commit -m "feat(usecase): ports plus registration, login and account interactors" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Use cases: refresh rotation, logout, forgot and reset password

**Files:**
- Modify: `internal/usecase/auth.go` (import block, and append four methods plus one helper)
- Test: `internal/usecase/tokens_test.go`

**Interfaces:**
- Consumes: the ports, `AuthDeps` and `AuthConfig` from Task 4, and `entity.RefreshToken.IsReplay` from Task 2
- Produces:
  - `(*Auth).Refresh(ctx, refreshToken string) (TokenPair, error)`
  - `(*Auth).Logout(ctx, refreshToken string) error`
  - `(*Auth).ForgotPassword(ctx, email string) error`
  - `(*Auth).ResetPassword(ctx, token, newPassword string) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/usecase/tokens_test.go`:
```go
package usecase_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

var resetLink = regexp.MustCompile(`/reset\?token=([A-Za-z0-9_-]+)`)

func resetTokenFrom(t *testing.T, body string) string {
	t.Helper()
	m := resetLink.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no reset link in email body:\n%s", body)
	}
	return m[1]
}

func TestRefreshRotates(t *testing.T) {
	e := newEnv(t)
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(context.Background(), pair.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if next.RefreshToken == pair.RefreshToken || next.ExpiresIn != 15*time.Minute {
		t.Fatalf("next = %+v", next)
	}
	if _, err := e.auth.Authenticate(next.AccessToken); err != nil {
		t.Fatalf("new access token invalid: %v", err)
	}
}

func TestRefreshReuseInsideGraceKeepsFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(5 * time.Second)
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("reuse err = %v, want ErrTokenInvalid", err)
	}
	if _, err := e.auth.Refresh(ctx, next.RefreshToken); err != nil {
		t.Fatalf("successor should survive an in-grace reuse: %v", err)
	}
}

func TestRefreshReplayAfterGraceRevokesFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(reuseGrace + time.Second)
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("replay err = %v, want ErrTokenInvalid", err)
	}
	if _, err := e.auth.Refresh(ctx, next.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("successor after replay err = %v, want ErrTokenInvalid (family revoked)", err)
	}
}

func TestRefreshRejectsExpiredAndUnknown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	if _, err := e.auth.Refresh(ctx, "never-issued"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("unknown err = %v", err)
	}
	e.clock.Advance(refreshTTL)
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("expired err = %v", err)
	}
}

func TestLogoutRevokesWholeFamily(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)
	next, err := e.auth.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.auth.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := e.auth.Refresh(ctx, next.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("refresh after logout err = %v", err)
	}
	if err := e.auth.Logout(ctx, "garbage"); err != nil {
		t.Fatalf("Logout(unknown) = %v, want nil", err)
	}
}

func TestForgotAndResetPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, pair := mustRegister(t, e)

	if err := e.auth.ForgotPassword(ctx, "ASHA@example.com"); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	msg := e.mailer.last(t)
	if msg.To != "asha@example.com" || !strings.Contains(msg.Body, "https://app.example/reset?token=") {
		t.Fatalf("email = %+v", msg)
	}
	token := resetTokenFrom(t, msg.Body)

	var ve *entity.ValidationError
	if err := e.auth.ResetPassword(ctx, token, "short"); !errors.As(err, &ve) || ve.Field != "new_password" {
		t.Fatalf("short new password err = %v", err)
	}
	if err := e.auth.ResetPassword(ctx, token, "new password 1"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, _, err := e.auth.Login(ctx, "asha@example.com", "correct horse"); !errors.Is(err, entity.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, _, err := e.auth.Login(ctx, "asha@example.com", "new password 1"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if _, err := e.auth.Refresh(ctx, pair.RefreshToken); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("old sessions survived the reset: %v", err)
	}
	if err := e.auth.ResetPassword(ctx, token, "another password"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("reset token reused: %v", err)
	}
	if err := e.auth.ResetPassword(ctx, "bogus", "long enough pw"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("bogus token err = %v", err)
	}
}

func TestResetInvalidatesOtherOutstandingLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mustRegister(t, e)
	for range 2 {
		if err := e.auth.ForgotPassword(ctx, "asha@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	e.mailer.mu.Lock()
	first, second := resetTokenFrom(t, e.mailer.sent[0].Body), resetTokenFrom(t, e.mailer.sent[1].Body)
	e.mailer.mu.Unlock()

	if err := e.auth.ResetPassword(ctx, second, "new password 1"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if err := e.auth.ResetPassword(ctx, first, "new password 2"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("older link still works: %v", err)
	}
}

func TestResetLinkExpires(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mustRegister(t, e)
	if err := e.auth.ForgotPassword(ctx, "asha@example.com"); err != nil {
		t.Fatal(err)
	}
	token := resetTokenFrom(t, e.mailer.last(t).Body)
	e.clock.Advance(resetTTL)
	if err := e.auth.ResetPassword(ctx, token, "new password 1"); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("expired link err = %v", err)
	}
}

func TestForgotPasswordUnknownOrMalformedEmailSendsNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, email := range []string{"nobody@example.com", "not an email"} {
		if err := e.auth.ForgotPassword(ctx, email); err != nil {
			t.Fatalf("ForgotPassword(%q) = %v, want nil", email, err)
		}
	}
	if e.mailer.count() != 0 {
		t.Fatal("email sent for an account that does not exist")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usecase/`
Expected: FAIL, the build fails because `e.auth.Refresh` and the other new methods are undefined.

- [ ] **Step 3: Implement the methods**

In `internal/usecase/auth.go`, replace the import block with:
```go
import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)
```

Then append to `internal/usecase/auth.go`:
```go
// Refresh rotates a refresh token: it consumes the presented token and issues a successor in the
// same family, atomically. Presenting a token that was rotated more than RefreshReuseGrace ago
// revokes the whole family (theft detection). Reuse inside the grace window is only rejected.
func (a *Auth) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	now := a.d.Now()
	oldHash := a.d.Opaque.Hash(refreshToken)
	plain, newHash, err := a.d.Opaque.New()
	if err != nil {
		return TokenPair{}, err
	}
	var userID uuid.UUID
	err = a.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		tok, err := a.d.Tokens.ConsumeRefresh(ctx, oldHash, now)
		if err != nil {
			return err
		}
		userID = tok.UserID
		return a.d.Tokens.CreateRefresh(ctx, tok.UserID, tok.FamilyID, newHash, now.Add(a.cfg.RefreshTTL))
	})
	if errors.Is(err, entity.ErrTokenInvalid) {
		if rerr := a.revokeIfReplayed(ctx, oldHash, now); rerr != nil {
			return TokenPair{}, rerr
		}
		return TokenPair{}, entity.ErrTokenInvalid
	}
	if err != nil {
		return TokenPair{}, err
	}
	access, ttl, err := a.d.Access.Issue(userID)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, ExpiresIn: ttl}, nil
}

// revokeIfReplayed revokes the family of an already-rotated token presented after the grace window.
func (a *Auth) revokeIfReplayed(ctx context.Context, hash []byte, now time.Time) error {
	tok, err := a.d.Tokens.FindRefresh(ctx, hash)
	if errors.Is(err, entity.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if tok.IsReplay(now, a.cfg.RefreshReuseGrace) {
		return a.d.Tokens.RevokeFamily(ctx, tok.FamilyID, now)
	}
	return nil
}

// Logout revokes the refresh token's whole family. Unknown tokens are ignored.
func (a *Auth) Logout(ctx context.Context, refreshToken string) error {
	return a.d.Tokens.RevokeFamilyOf(ctx, a.d.Opaque.Hash(refreshToken), a.d.Now())
}

// ForgotPassword emails a single-use reset link. It never reveals whether the account exists.
func (a *Auth) ForgotPassword(ctx context.Context, email string) error {
	addr, err := entity.ParseEmail(email)
	if err != nil {
		return nil // not an address anyone could have registered with
	}
	user, err := a.d.Users.GetByEmail(ctx, addr)
	if errors.Is(err, entity.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, hash, err := a.d.Opaque.New()
	if err != nil {
		return err
	}
	if err := a.d.Tokens.CreatePasswordReset(ctx, user.ID, hash, a.d.Now().Add(a.cfg.ResetTTL)); err != nil {
		return err
	}
	link := strings.TrimRight(a.cfg.AppBaseURL, "/") + "/reset?token=" + url.QueryEscape(plain)
	return a.d.Mailer.Send(ctx, Message{
		To:      user.Email.String(),
		Subject: "Reset your Shiksha AI password",
		Body: fmt.Sprintf("Hi %s,\n\nUse this link within %d minutes to choose a new password:\n\n%s\n\nIf you did not ask for this, you can ignore this email.\n",
			user.DisplayName, int(a.cfg.ResetTTL.Minutes()), link),
	})
}

// ResetPassword sets a new password using a reset token. In one transaction it consumes the token,
// updates the password, invalidates the user's other reset links and signs them out everywhere.
func (a *Auth) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := entity.ValidatePassword("new_password", newPassword); err != nil {
		return err
	}
	hash, err := a.d.Hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	now := a.d.Now()
	return a.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		userID, err := a.d.Tokens.ConsumePasswordReset(ctx, a.d.Opaque.Hash(token), now)
		if err != nil {
			return err
		}
		if err := a.d.Users.UpdatePassword(ctx, userID, hash, now); err != nil {
			return err
		}
		if err := a.d.Tokens.InvalidatePasswordResets(ctx, userID, now); err != nil {
			return err
		}
		return a.d.Tokens.RevokeAllForUser(ctx, userID, now)
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/usecase/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/usecase`

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/usecase
git commit -m "feat(usecase): refresh rotation with replay detection, logout and password reset" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Postgres repositories

**Files:**
- Create: `internal/adapter/repository/errors.go`, `internal/adapter/repository/users.go`, `internal/adapter/repository/tokens.go`
- Test: `internal/adapter/repository/main_test.go`, `internal/adapter/repository/users_test.go`, `internal/adapter/repository/tokens_test.go`

**Interfaces:**
- Consumes:
  - From Task 4: `usecase.UserRepository`, `usecase.TokenRepository`, `usecase.NewUser`, `usecase.ProfilePatch`
  - From Task 3: `database.Conn`, `database.NewTxManager`, `dbtest.Main`, `dbtest.NewPool`
  - Entity types from Task 2
- Produces:
  - `repository.NewUsers(*pgxpool.Pool) *repository.Users` (implements `usecase.UserRepository`)
  - `repository.NewTokens(*pgxpool.Pool) *repository.Tokens` (implements `usecase.TokenRepository`)

- [ ] **Step 1: Write the failing tests**

Create `internal/adapter/repository/main_test.go`:
```go
package repository_test

import (
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var t0 = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func newUserInput(email string) usecase.NewUser {
	return usecase.NewUser{Email: entity.Email(email), PasswordHash: "old-hash", DisplayName: "Asha", PreferredLang: "hi", AcceptedAt: t0}
}

func mustUser(t *testing.T, users *repository.Users) entity.User {
	t.Helper()
	u, err := users.Create(t.Context(), newUserInput(uuid.NewString()+"@example.com"))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}
```

Create `internal/adapter/repository/users_test.go`:
```go
package repository_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func TestUsersCreateAndGet(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	grade := 7
	in := newUserInput("asha@example.com")
	in.Grade, in.GuardianConsent = &grade, true

	created, err := users.Create(ctx, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil || created.Email != "asha@example.com" || created.Grade == nil || *created.Grade != 7 {
		t.Fatalf("created = %+v", created)
	}
	if !created.TermsAcceptedAt.Equal(t0) || !created.CreatedAt.Equal(t0) || created.GuardianConsentAt == nil || !created.GuardianConsentAt.Equal(t0) {
		t.Fatalf("timestamps not taken from AcceptedAt: %+v", created)
	}
	byEmail, err := users.GetByEmail(ctx, "asha@example.com")
	if err != nil || byEmail.ID != created.ID {
		t.Fatalf("GetByEmail = %+v, %v", byEmail, err)
	}
	byID, err := users.GetByID(ctx, created.ID)
	if err != nil || byID.Email != "asha@example.com" {
		t.Fatalf("GetByID = %+v, %v", byID, err)
	}

	noConsent, err := users.Create(ctx, newUserInput("ravi@example.com"))
	if err != nil || noConsent.GuardianConsentAt != nil {
		t.Fatalf("consent should be nil when not given: %+v, %v", noConsent, err)
	}
}

func TestUsersEmailUniqueIgnoringCase(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	if _, err := users.Create(ctx, newUserInput("dup@example.com")); err != nil {
		t.Fatal(err)
	}
	// The database index is the last line of defence even if a caller skips normalization.
	if _, err := users.Create(ctx, newUserInput("DUP@example.com")); !errors.Is(err, entity.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestUsersMissingReturnsNotFound(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	if _, err := users.GetByID(ctx, uuid.New()); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("GetByID err = %v", err)
	}
	if _, err := users.GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("GetByEmail err = %v", err)
	}
	if err := users.UpdatePassword(ctx, uuid.New(), "h", t0); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("UpdatePassword err = %v", err)
	}
	if err := users.Delete(ctx, uuid.New()); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("Delete err = %v", err)
	}
}

func TestUsersUpdateProfileChangesOnlyGivenFields(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	u := mustUser(t, users)
	later := t0.Add(time.Hour)

	name, grade := "Asha K", 9
	updated, err := users.UpdateProfile(ctx, u.ID, usecase.ProfilePatch{DisplayName: &name, Grade: &grade}, later)
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.DisplayName != "Asha K" || *updated.Grade != 9 || updated.PreferredLang != "hi" || !updated.UpdatedAt.Equal(later) {
		t.Fatalf("updated = %+v", updated)
	}
	same, err := users.UpdateProfile(ctx, u.ID, usecase.ProfilePatch{}, later)
	if err != nil || same.DisplayName != "Asha K" || *same.Grade != 9 {
		t.Fatalf("empty patch changed fields: %+v, %v", same, err)
	}
	if _, err := users.UpdateProfile(ctx, uuid.New(), usecase.ProfilePatch{}, later); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("unknown user err = %v", err)
	}
}

func TestUsersUpdatePassword(t *testing.T) {
	users := repository.NewUsers(dbtest.NewPool(t))
	ctx := t.Context()
	u := mustUser(t, users)
	if err := users.UpdatePassword(ctx, u.ID, "new-hash", t0.Add(time.Minute)); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	got, _ := users.GetByID(ctx, u.ID)
	if got.PasswordHash != "new-hash" {
		t.Fatalf("PasswordHash = %q", got.PasswordHash)
	}
}

func TestUsersDeleteCascadesToTokens(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	if err := tokens.CreateRefresh(ctx, u.ID, uuid.New(), hashOf("A"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tokens.FindRefresh(ctx, hashOf("A")); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("token survived user deletion: %v", err)
	}
}
```

Create `internal/adapter/repository/tokens_test.go`:
```go
package repository_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestConsumeRefreshIsSingleUse(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	family := uuid.New()
	if err := tokens.CreateRefresh(ctx, u.ID, family, hashOf("A"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	tok, err := tokens.ConsumeRefresh(ctx, hashOf("A"), t0)
	if err != nil {
		t.Fatalf("ConsumeRefresh: %v", err)
	}
	if tok.UserID != u.ID || tok.FamilyID != family || tok.UsedAt == nil || !tok.UsedAt.Equal(t0) {
		t.Fatalf("token = %+v", tok)
	}
	if _, err := tokens.ConsumeRefresh(ctx, hashOf("A"), t0.Add(time.Second)); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("second consume err = %v", err)
	}
	found, err := tokens.FindRefresh(ctx, hashOf("A"))
	if err != nil || found.UsedAt == nil || !found.UsedAt.Equal(t0) {
		t.Fatalf("FindRefresh = %+v, %v", found, err)
	}
}

func TestConsumeRefreshRejectsExpiredRevokedAndUnknown(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)

	if err := tokens.CreateRefresh(ctx, u.ID, uuid.New(), hashOf("EXP"), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ConsumeRefresh(ctx, hashOf("EXP"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("expired err = %v", err)
	}

	revokedFamily := uuid.New()
	if err := tokens.CreateRefresh(ctx, u.ID, revokedFamily, hashOf("REV"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tokens.RevokeFamily(ctx, revokedFamily, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ConsumeRefresh(ctx, hashOf("REV"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("revoked err = %v", err)
	}

	if _, err := tokens.ConsumeRefresh(ctx, hashOf("unknown"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("unknown consume err = %v", err)
	}
	if _, err := tokens.FindRefresh(ctx, hashOf("unknown")); !errors.Is(err, entity.ErrNotFound) {
		t.Errorf("unknown find err = %v", err)
	}
}

func TestConsumeRefreshConcurrentHasOneWinner(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	u := mustUser(t, users)
	if err := tokens.CreateRefresh(t.Context(), u.ID, uuid.New(), hashOf("A"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = tokens.ConsumeRefresh(context.Background(), hashOf("A"), t0)
		}()
	}
	wg.Wait()

	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, entity.ErrTokenInvalid):
			t.Fatalf("consumer %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent consumers succeeded, want exactly 1", wins)
	}
}

func TestRevokeFamilyOfAndAllForUser(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	family, other := uuid.New(), uuid.New()
	for token, fam := range map[string]uuid.UUID{"A": family, "B": family, "C": other} {
		if err := tokens.CreateRefresh(ctx, u.ID, fam, hashOf(token), t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tokens.RevokeFamilyOf(ctx, hashOf("A"), t0); err != nil {
		t.Fatalf("RevokeFamilyOf: %v", err)
	}
	if b, _ := tokens.FindRefresh(ctx, hashOf("B")); b.RevokedAt == nil {
		t.Fatal("sibling B not revoked with its family")
	}
	if c, _ := tokens.FindRefresh(ctx, hashOf("C")); c.RevokedAt != nil {
		t.Fatal("token from another family was revoked")
	}
	if err := tokens.RevokeFamilyOf(ctx, hashOf("unknown"), t0); err != nil {
		t.Fatalf("RevokeFamilyOf(unknown) = %v, want nil", err)
	}
	if err := tokens.RevokeAllForUser(ctx, u.ID, t0); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if c, _ := tokens.FindRefresh(ctx, hashOf("C")); c.RevokedAt == nil {
		t.Fatal("RevokeAllForUser missed a family")
	}
}

func TestPasswordResetTokens(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	for token, exp := range map[string]time.Time{"R1": t0.Add(30 * time.Minute), "R2": t0.Add(30 * time.Minute), "R3": t0} {
		if err := tokens.CreatePasswordReset(ctx, u.ID, hashOf(token), exp); err != nil {
			t.Fatal(err)
		}
	}

	got, err := tokens.ConsumePasswordReset(ctx, hashOf("R1"), t0)
	if err != nil || got != u.ID {
		t.Fatalf("ConsumePasswordReset = %v, %v", got, err)
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R1"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("reuse err = %v", err)
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R3"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("expired err = %v", err)
	}
	if err := tokens.InvalidatePasswordResets(ctx, u.ID, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R2"), t0); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Errorf("invalidated token err = %v", err)
	}
}

func TestRepositoriesJoinTheUseCaseTransaction(t *testing.T) {
	pool := dbtest.NewPool(t)
	users, tokens := repository.NewUsers(pool), repository.NewTokens(pool)
	tm := database.NewTxManager(pool)
	ctx := t.Context()
	u := mustUser(t, users)
	if err := tokens.CreatePasswordReset(ctx, u.ID, hashOf("R"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	failure := errors.New("later step failed")
	err := tm.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R"), t0); err != nil {
			return err
		}
		if err := users.UpdatePassword(ctx, u.ID, "new-hash", t0); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("WithinTx err = %v", err)
	}
	got, _ := users.GetByID(ctx, u.ID)
	if got.PasswordHash != "old-hash" {
		t.Fatal("password change survived the rollback")
	}
	if _, err := tokens.ConsumePasswordReset(ctx, hashOf("R"), t0); err != nil {
		t.Fatalf("reset token consumption survived the rollback: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/adapter/repository/`
Expected: FAIL, the build fails because `repository.NewUsers` and `repository.NewTokens` are undefined.

- [ ] **Step 3: Implement the repositories**

Create `internal/adapter/repository/errors.go`:
```go
package repository

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

Create `internal/adapter/repository/users.go`:
```go
// Package repository implements the use-case repository ports on Postgres. Every query goes
// through database.Conn, so repositories join a use case's transaction automatically.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const userColumns = `id, email, password_hash, display_name, preferred_lang, grade,
	terms_accepted_at, guardian_consent_at, created_at, updated_at`

// Users implements usecase.UserRepository.
type Users struct{ pool *pgxpool.Pool }

var _ usecase.UserRepository = (*Users)(nil)

func NewUsers(pool *pgxpool.Pool) *Users { return &Users{pool: pool} }

func (r *Users) Create(ctx context.Context, u usecase.NewUser) (entity.User, error) {
	user, err := scanUser(database.Conn(ctx, r.pool).QueryRow(ctx, `
		INSERT INTO users (email, password_hash, display_name, preferred_lang, grade,
		                   terms_accepted_at, guardian_consent_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::timestamptz, CASE WHEN $7::boolean THEN $6::timestamptz END, $6::timestamptz, $6::timestamptz)
		RETURNING `+userColumns,
		string(u.Email), u.PasswordHash, u.DisplayName, u.PreferredLang, u.Grade, u.AcceptedAt, u.GuardianConsent))
	if isUniqueViolation(err) {
		return entity.User{}, entity.ErrEmailTaken
	}
	return user, err
}

func (r *Users) GetByEmail(ctx context.Context, email entity.Email) (entity.User, error) {
	return scanUser(database.Conn(ctx, r.pool).QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, string(email)))
}

func (r *Users) GetByID(ctx context.Context, id uuid.UUID) (entity.User, error) {
	return scanUser(database.Conn(ctx, r.pool).QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func (r *Users) UpdateProfile(ctx context.Context, id uuid.UUID, p usecase.ProfilePatch, now time.Time) (entity.User, error) {
	return scanUser(database.Conn(ctx, r.pool).QueryRow(ctx, `
		UPDATE users SET
			display_name   = coalesce($2, display_name),
			preferred_lang = coalesce($3, preferred_lang),
			grade          = coalesce($4, grade),
			updated_at     = $5
		WHERE id = $1
		RETURNING `+userColumns,
		id, p.DisplayName, p.PreferredLang, p.Grade, now))
}

func (r *Users) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string, now time.Time) error {
	tag, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1`, id, passwordHash, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return entity.ErrNotFound
	}
	return nil
}

func (r *Users) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := database.Conn(ctx, r.pool).Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return entity.ErrNotFound
	}
	return nil
}

func scanUser(row pgx.Row) (entity.User, error) {
	var (
		u     entity.User
		email string
	)
	err := row.Scan(&u.ID, &email, &u.PasswordHash, &u.DisplayName, &u.PreferredLang, &u.Grade,
		&u.TermsAcceptedAt, &u.GuardianConsentAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.User{}, entity.ErrNotFound
	}
	if err != nil {
		return entity.User{}, err
	}
	u.Email = entity.Email(email)
	return u, nil
}
```

Create `internal/adapter/repository/tokens.go`:
```go
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const refreshColumns = `user_id, family_id, expires_at, used_at, revoked_at`

// Tokens implements usecase.TokenRepository. Only SHA-256 hashes of tokens are stored.
type Tokens struct{ pool *pgxpool.Pool }

var _ usecase.TokenRepository = (*Tokens)(nil)

func NewTokens(pool *pgxpool.Pool) *Tokens { return &Tokens{pool: pool} }

func (r *Tokens) CreateRefresh(ctx context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, familyID, hash, expiresAt)
	return err
}

// ConsumeRefresh is a single conditional UPDATE, so of several concurrent callers exactly one wins.
func (r *Tokens) ConsumeRefresh(ctx context.Context, hash []byte, now time.Time) (entity.RefreshToken, error) {
	tok, err := scanRefresh(database.Conn(ctx, r.pool).QueryRow(ctx, `
		UPDATE refresh_tokens SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at > $2
		RETURNING `+refreshColumns, hash, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.RefreshToken{}, entity.ErrTokenInvalid
	}
	return tok, err
}

func (r *Tokens) FindRefresh(ctx context.Context, hash []byte) (entity.RefreshToken, error) {
	tok, err := scanRefresh(database.Conn(ctx, r.pool).QueryRow(ctx,
		`SELECT `+refreshColumns+` FROM refresh_tokens WHERE token_hash = $1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.RefreshToken{}, entity.ErrNotFound
	}
	return tok, err
}

func (r *Tokens) RevokeFamily(ctx context.Context, familyID uuid.UUID, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`, familyID, now)
	return err
}

func (r *Tokens) RevokeFamilyOf(ctx context.Context, hash []byte, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = $2
		WHERE revoked_at IS NULL
		  AND family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1)`, hash, now)
	return err
}

func (r *Tokens) RevokeAllForUser(ctx context.Context, userID uuid.UUID, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, now)
	return err
}

func (r *Tokens) CreatePasswordReset(ctx context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx, `
		INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hash, userID, expiresAt)
	return err
}

func (r *Tokens) ConsumePasswordReset(ctx context.Context, hash []byte, now time.Time) (uuid.UUID, error) {
	var userID uuid.UUID
	err := database.Conn(ctx, r.pool).QueryRow(ctx, `
		UPDATE password_reset_tokens SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING user_id`, hash, now).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	return userID, err
}

func (r *Tokens) InvalidatePasswordResets(ctx context.Context, userID uuid.UUID, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE password_reset_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, userID, now)
	return err
}

func scanRefresh(row pgx.Row) (entity.RefreshToken, error) {
	var t entity.RefreshToken
	err := row.Scan(&t.UserID, &t.FamilyID, &t.ExpiresAt, &t.UsedAt, &t.RevokedAt)
	return t, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/adapter/repository/ ./internal/archtest/`
Expected: both packages print `ok`.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/adapter/repository
git commit -m "feat(repository): Postgres user and token repositories joining use-case transactions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Crypto infrastructure: argon2id hasher, JWT issuer, opaque tokens

**Files:**
- Create: `internal/infrastructure/crypto/password.go`, `internal/infrastructure/crypto/jwt.go`, `internal/infrastructure/crypto/opaque.go`
- Test: `internal/infrastructure/crypto/password_test.go`, `internal/infrastructure/crypto/jwt_test.go`, `internal/infrastructure/crypto/opaque_test.go`

**Interfaces:**
- Consumes: `usecase.PasswordHasher`, `usecase.AccessTokens` and `usecase.OpaqueTokens` (Task 4); `entity.ErrTokenInvalid` (Task 2)
- Produces:
  - `crypto.NewArgon2Hasher(maxConcurrent int) *crypto.Argon2Hasher`
  - `crypto.NewJWTIssuer(secret string, ttl time.Duration) *crypto.JWTIssuer`
  - `crypto.Opaque{}`
  - Each implements its port, which a compile-time assertion checks.

- [ ] **Step 1: Write the failing tests**

Run: `go get golang.org/x/crypto@latest github.com/golang-jwt/jwt/v5@latest`

Create `internal/infrastructure/crypto/password_test.go`:
```go
package crypto

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHashThenVerify(t *testing.T) {
	h := NewArgon2Hasher(2)
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
	h := NewArgon2Hasher(2)
	a, _ := h.Hash(context.Background(), "same password")
	b, _ := h.Hash(context.Background(), "same password")
	if a == b {
		t.Fatal("two hashes of the same password are identical; salt is not random")
	}
}

func TestVerifyHandlesUnicodePasswords(t *testing.T) {
	h := NewArgon2Hasher(2)
	enc, err := h.Hash(context.Background(), "पासवर्ड१२३")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.Verify(context.Background(), "पासवर्ड१२३", enc); !ok {
		t.Fatal("Devanagari password did not verify")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	h := NewArgon2Hasher(1)
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

// The dummy hash backs timing equalization for unknown accounts. VerifyDummy discards errors, so
// this test is what proves the dummy hash is well formed and never matches.
func TestDummyHashIsWellFormed(t *testing.T) {
	h := NewArgon2Hasher(1)
	ok, err := h.Verify(context.Background(), "anything", h.dummy)
	if err != nil || ok {
		t.Fatalf("Verify(dummy) = %v, %v; want false, nil", ok, err)
	}
}

func TestHashRespectsContextWhenSaturated(t *testing.T) {
	h := NewArgon2Hasher(1)
	h.sem <- struct{}{} // occupy the only slot
	defer func() { <-h.sem }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash err = %v, want context.Canceled", err)
	}
}
```

Create `internal/infrastructure/crypto/jwt_test.go`:
```go
package crypto

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

var testSecret = strings.Repeat("s", 32)

func TestIssueAndVerify(t *testing.T) {
	ti := NewJWTIssuer(testSecret, 15*time.Minute)
	id := uuid.New()
	tok, ttl, err := ti.Issue(id)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if ttl != 15*time.Minute {
		t.Errorf("ttl = %v, want 15m", ttl)
	}
	got, err := ti.Verify(tok)
	if err != nil || got != id {
		t.Fatalf("Verify = %v, %v; want %v", got, err, id)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	ti := NewJWTIssuer(testSecret, time.Minute)
	start := time.Now()
	ti.now = func() time.Time { return start }
	tok, _, err := ti.Issue(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	ti.now = func() time.Time { return start.Add(2 * time.Minute) }
	if _, err := ti.Verify(tok); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyRejectsForeignSignature(t *testing.T) {
	tok, _, _ := NewJWTIssuer(strings.Repeat("x", 32), time.Minute).Issue(uuid.New())
	if _, err := NewJWTIssuer(testSecret, time.Minute).Verify(tok); !errors.Is(err, entity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyRejectsWrongAlgorithmAudienceAndMissingExpiry(t *testing.T) {
	ti := NewJWTIssuer(testSecret, time.Minute)
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
		if _, err := ti.Verify(tok); !errors.Is(err, entity.ErrTokenInvalid) {
			t.Errorf("%s: err = %v, want ErrTokenInvalid", name, err)
		}
	}
}
```

Create `internal/infrastructure/crypto/opaque_test.go`:
```go
package crypto

import (
	"bytes"
	"testing"
)

func TestOpaqueTokens(t *testing.T) {
	var o Opaque
	a, ha, err := o.New()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := o.New()
	if a == b {
		t.Fatal("two opaque tokens are equal")
	}
	if len(a) != 43 {
		t.Errorf("len(token) = %d, want 43 (32 bytes base64url)", len(a))
	}
	if len(ha) != 32 || !bytes.Equal(ha, o.Hash(a)) {
		t.Errorf("hash mismatch: %x vs %x", ha, o.Hash(a))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infrastructure/crypto/`
Expected: FAIL, the build fails because `NewArgon2Hasher`, `NewJWTIssuer` and `Opaque` are undefined.

- [ ] **Step 3: Implement the crypto package**

Create `internal/infrastructure/crypto/password.go`:
```go
// Package crypto implements the password, access-token and opaque-token ports.
package crypto

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// argon2id parameters: the OWASP minimum recommendation (19 MiB, 2 iterations, 1 lane).
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

var errMalformedHash = errors.New("crypto: malformed password hash")

// Argon2Hasher hashes and verifies passwords with argon2id. Every hash allocates about 19 MiB,
// so a semaphore caps concurrent hashes and bounds memory during a burst of logins.
type Argon2Hasher struct {
	sem   chan struct{}
	dummy string
}

var _ usecase.PasswordHasher = (*Argon2Hasher)(nil)

// NewArgon2Hasher allows at most maxConcurrent hashes at a time (use 2×NumCPU in production).
func NewArgon2Hasher(maxConcurrent int) *Argon2Hasher {
	salt := make([]byte, argonSaltLen) // a fixed salt is fine: the dummy hash never matches a real password
	key := argon2.IDKey([]byte("dummy password"), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return &Argon2Hasher{
		sem:   make(chan struct{}, max(1, maxConcurrent)),
		dummy: encodeHash(salt, key, argonMemoryKiB, argonTime, argonThreads),
	}
}

// Hash returns a PHC-formatted hash: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>.
func (h *Argon2Hasher) Hash(ctx context.Context, password string) (string, error) {
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
func (h *Argon2Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
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
func (h *Argon2Hasher) VerifyDummy(ctx context.Context, password string) {
	_, _ = h.Verify(ctx, password, h.dummy)
}

func (h *Argon2Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Argon2Hasher) release() { <-h.sem }

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

Create `internal/infrastructure/crypto/jwt.go`:
```go
package crypto

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const (
	tokenIssuer   = "shiksha-ai"
	tokenAudience = "api"
)

// JWTIssuer signs and verifies short-lived HS256 access tokens.
type JWTIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

var _ usecase.AccessTokens = (*JWTIssuer)(nil)

// NewJWTIssuer creates an issuer. The secret must be at least 32 bytes (config enforces this).
func NewJWTIssuer(secret string, ttl time.Duration) *JWTIssuer {
	return &JWTIssuer{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// Issue returns a signed access token for userID and its lifetime.
func (ti *JWTIssuer) Issue(userID uuid.UUID) (string, time.Duration, error) {
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

// Verify checks signature, algorithm, issuer, audience and expiry and returns the user id.
// Every failure is reported as entity.ErrTokenInvalid.
func (ti *JWTIssuer) Verify(token string) (uuid.UUID, error) {
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
		return uuid.Nil, entity.ErrTokenInvalid
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	return id, nil
}
```

Create `internal/infrastructure/crypto/opaque.go`:
```go
package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// Opaque creates random URL-safe secrets (refresh and reset tokens) and their SHA-256 storage hashes.
type Opaque struct{}

var _ usecase.OpaqueTokens = Opaque{}

// New returns a random token (32 bytes, base64url) and the hash to store in its place.
func (Opaque) New() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plain := base64.RawURLEncoding.EncodeToString(b)
	return plain, Opaque{}.Hash(plain), nil
}

// Hash returns the SHA-256 of a token, which is the form stored in the database.
func (Opaque) Hash(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/infrastructure/crypto/ ./internal/archtest/`
Expected: both packages print `ok`.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/infrastructure/crypto
git commit -m "feat(crypto): argon2id hasher, HS256 JWT issuer and opaque tokens" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Mail infrastructure

**Files:**
- Create: `internal/infrastructure/mail/mail.go`
- Test: `internal/infrastructure/mail/mail_test.go`

**Interfaces:**
- Consumes: `usecase.Mailer` and `usecase.Message` (Task 4)
- Produces:
  - `mail.LogMailer{Log *slog.Logger}`
  - `mail.SMTPMailer{Host string; Port int; User, Pass, From string}`
  - Both implement `usecase.Mailer`.

- [ ] **Step 1: Write the failing tests**

Create `internal/infrastructure/mail/mail_test.go`:
```go
package mail

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

var testNow = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func TestBuildMessage(t *testing.T) {
	msg, err := buildMessage("Shiksha AI <no-reply@example.com>", usecase.Message{
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
	for name, m := range map[string]usecase.Message{
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
	if err := m.Send(context.Background(), usecase.Message{To: "a@example.com", Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(buf.String(), "a@example.com") {
		t.Fatalf("log output %q does not mention the recipient", buf.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infrastructure/mail/`
Expected: FAIL, the build fails because `buildMessage` and `LogMailer` are undefined.

- [ ] **Step 3: Implement the mailers**

Create `internal/infrastructure/mail/mail.go`:
```go
// Package mail implements the usecase.Mailer port.
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

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// LogMailer writes messages to the log instead of sending them. Use it only in development.
type LogMailer struct{ Log *slog.Logger }

var _ usecase.Mailer = LogMailer{}

func (l LogMailer) Send(_ context.Context, m usecase.Message) error {
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

var _ usecase.Mailer = SMTPMailer{}

func (s SMTPMailer) Send(ctx context.Context, m usecase.Message) error {
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

func buildMessage(from string, m usecase.Message, now time.Time) ([]byte, error) {
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

Run: `go test -race ./internal/infrastructure/mail/ ./internal/archtest/`
Expected: both packages print `ok`.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/infrastructure/mail
git commit -m "feat(mail): SMTP and log mailers implementing the Mailer port" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: HTTP foundation, the RateLimiter port and the in-memory limiter

**Files:**
- Create: `internal/adapter/httpapi/respond.go`, `internal/adapter/httpapi/middleware.go`, `internal/adapter/httpapi/ratelimit.go`, `internal/infrastructure/ratelimit/memory.go`
- Test: `internal/adapter/httpapi/foundation_test.go`, `internal/infrastructure/ratelimit/memory_test.go`

**Interfaces:**
- Consumes: entity errors (Task 2)
- Produces:
  - Package-internal helpers used by Task 10:
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
  - Exported:
    - `httpapi.RateLimiter` interface `{Allow(key string) bool}`
    - `httpapi.Limits{Register, Login, LoginIP, Refresh, Forgot, ForgotIP RateLimiter}`
    - `httpapi.DefaultLimits(newLimiter func(interval time.Duration, burst int) RateLimiter) Limits`
    - `ratelimit.NewMemory(interval time.Duration, burst int) *ratelimit.Memory`, `(*Memory).Allow(key string) bool`

- [ ] **Step 1: Write the failing limiter test and implement the limiter**

Run: `go get golang.org/x/time@latest`

Create `internal/infrastructure/ratelimit/memory_test.go`:
```go
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
```

Run: `go test ./internal/infrastructure/ratelimit/`
Expected: FAIL, the build fails because `NewMemory` is undefined.

Create `internal/infrastructure/ratelimit/memory.go`:
```go
// Package ratelimit provides an in-memory keyed token-bucket limiter.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	idleTTL   = time.Hour // must exceed the slowest full refill (forgot-password: 3/hour)
	sweepSize = 10_000    // sweep idle buckets once the map grows this large
)

// Memory is a keyed token bucket kept in process memory, so limits apply per instance.
// A shared (Redis) limiter can replace it behind the same interface.
type Memory struct {
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

// NewMemory allows `burst` events at once, refilling one event every `interval`.
func NewMemory(interval time.Duration, burst int) *Memory {
	return &Memory{every: rate.Every(interval), burst: burst, buckets: make(map[string]*bucket), now: time.Now}
}

// Allow reports whether one more event for key is allowed now.
func (l *Memory) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= sweepSize {
			l.sweep(now)
		}
		b = &bucket{lim: rate.NewLimiter(l.every, l.burst)}
		l.buckets[key] = b
	}
	b.seen = now
	return b.lim.AllowN(now, 1)
}

func (l *Memory) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.seen) > idleTTL {
			delete(l.buckets, k)
		}
	}
}
```

Run: `go mod tidy && go test -race ./internal/infrastructure/ratelimit/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/infrastructure/ratelimit`

- [ ] **Step 2: Write the failing HTTP foundation tests**

Create `internal/adapter/httpapi/foundation_test.go`:
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
	for name, lim := range map[string]RateLimiter{"Register": l.Register, "Login": l.Login, "LoginIP": l.LoginIP, "Refresh": l.Refresh, "Forgot": l.Forgot, "ForgotIP": l.ForgotIP} {
		if lim == nil {
			t.Errorf("%s limiter is nil", name)
		}
	}
	want := []spec{{"6s", 10}, {"12s", 5}, {"1s", 60}, {"2s", 30}, {"20m0s", 3}, {"6s", 10}}
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/adapter/httpapi/`
Expected: FAIL, the build fails because `decodeJSON`, `DefaultLimits` and the other helpers are undefined.

- [ ] **Step 4: Implement the foundation**

Create `internal/adapter/httpapi/ratelimit.go`:
```go
package httpapi

import "time"

// RateLimiter decides whether one more event for key is allowed now. The in-memory
// implementation is per instance; a shared (e.g. Redis) one can replace it without touching handlers.
type RateLimiter interface {
	Allow(key string) bool
}

// Limits groups the limiters the API applies. Every field must be set.
type Limits struct {
	Register RateLimiter // per IP
	Login    RateLimiter // per IP + email
	LoginIP  RateLimiter // per IP, looser because a school NAT shares one address
	Refresh  RateLimiter // per IP
	Forgot   RateLimiter // per email (forgot and reset)
	ForgotIP RateLimiter // per IP (forgot and reset)
}

// DefaultLimits returns the production budgets, building each limiter with newLimiter
// (burst events at once, refilling one per interval).
func DefaultLimits(newLimiter func(interval time.Duration, burst int) RateLimiter) Limits {
	return Limits{
		Register: newLimiter(time.Minute/10, 10),
		Login:    newLimiter(time.Minute/5, 5),
		LoginIP:  newLimiter(time.Second, 60),
		Refresh:  newLimiter(time.Minute/30, 30),
		Forgot:   newLimiter(time.Hour/3, 3),
		ForgotIP: newLimiter(time.Minute/10, 10),
	}
}
```

Create `internal/adapter/httpapi/respond.go`:
```go
// Package httpapi is the HTTP interface adapter: REST controllers, DTOs, middleware and admin endpoints.
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

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

const maxBodyBytes = 1 << 20

var errNotSingleObject = errors.New("body must contain a single JSON object")

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
			err = errNotSingleObject
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
	case errors.Is(err, errNotSingleObject):
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		writeError(w, r, http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), "json: "))
	default:
		writeError(w, r, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
	}
	return false
}

// serviceError maps entity errors to HTTP responses and logs anything unexpected.
func serviceError(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	var ve *entity.ValidationError
	switch {
	case errors.As(err, &ve):
		writeErrorDetail(w, r, http.StatusBadRequest, errorDetail{Code: "validation_failed", Message: ve.Error(), Field: ve.Field})
	case errors.Is(err, entity.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, "email_taken", "an account with this email already exists")
	case errors.Is(err, entity.ErrInvalidCredentials):
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
	case errors.Is(err, entity.ErrTokenInvalid):
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "token is invalid or expired")
	case errors.Is(err, entity.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, context.Canceled):
		// The client went away; there is nobody to answer.
	default:
		log.ErrorContext(r.Context(), "request failed", "err", err, "request_id", requestIDFrom(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal", "something went wrong")
	}
}
```

Create `internal/adapter/httpapi/middleware.go`:
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

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/adapter/httpapi/ ./internal/archtest/`
Expected: both packages print `ok`.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/adapter/httpapi internal/infrastructure/ratelimit
git commit -m "feat(httpapi): JSON I/O, error mapping, middleware and rate-limiter port with in-memory adapter" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: REST controllers and router

**Files:**
- Create: `internal/adapter/httpapi/router.go`, `internal/adapter/httpapi/auth_handlers.go`, `internal/adapter/httpapi/account_handlers.go`
- Test: `internal/adapter/httpapi/main_test.go`, `internal/adapter/httpapi/api_test.go`

**Interfaces:**
- Consumes:
  - From Tasks 4–5: `usecase.Auth`, `usecase.Accounts`, `usecase.Languages`, `usecase.RegisterInput`, `usecase.ProfileInput`, `usecase.TokenPair`
  - From Task 9: `Limits` and the helpers
  - For the tests: `repository.NewUsers` and `repository.NewTokens` (Task 6), `database.NewTxManager` and `dbtest` (Task 3), `crypto.*` (Task 7), `ratelimit.NewMemory` (Task 9)
- Produces:
  - `httpapi.Options{Auth *usecase.Auth; Accounts *usecase.Accounts; Limits Limits; Log *slog.Logger; AllowedOrigins []string; TrustProxy bool}`
  - `httpapi.New(Options) http.Handler`

- [ ] **Step 1: Write the failing tests**

Create `internal/adapter/httpapi/main_test.go`:
```go
package httpapi

import (
	"os"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
```

Create `internal/adapter/httpapi/api_test.go`:
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/adapter/httpapi/`
Expected: FAIL, the build fails because `New`, `Options`, `authResponse` and the other API types are undefined.

- [ ] **Step 3: Implement the router and controllers**

Create `internal/adapter/httpapi/router.go`:
```go
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// Options configures the public API handler.
type Options struct {
	Auth           *usecase.Auth
	Accounts       *usecase.Accounts
	Limits         Limits
	Log            *slog.Logger
	AllowedOrigins []string
	TrustProxy     bool
}

// API holds controller dependencies. Controllers call use cases only, never repositories.
type API struct {
	auth       *usecase.Auth
	accounts   *usecase.Accounts
	limits     Limits
	log        *slog.Logger
	trustProxy bool
}

// New returns the public API handler with middleware applied.
func New(opts Options) http.Handler {
	a := &API{auth: opts.Auth, accounts: opts.Accounts, limits: opts.Limits, log: opts.Log, trustProxy: opts.TrustProxy}

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
func (a *API) allow(w http.ResponseWriter, r *http.Request, l RateLimiter, key string) bool {
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

Create `internal/adapter/httpapi/auth_handlers.go`:
```go
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
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

func toUserResponse(u entity.User) userResponse {
	return userResponse{
		ID: u.ID, Email: u.Email.String(), DisplayName: u.DisplayName, PreferredLang: u.PreferredLang,
		Grade: u.Grade, GuardianConsent: u.GuardianConsentAt != nil, CreatedAt: u.CreatedAt,
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
}

func toTokenResponse(p usecase.TokenPair) tokenResponse {
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
	if !a.allow(w, r, a.limits.Register, clientIP(r, a.trustProxy)) {
		return
	}
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, pair, err := a.auth.Register(r.Context(), usecase.RegisterInput{
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
	if !a.allow(w, r, a.limits.LoginIP, ip) {
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.allow(w, r, a.limits.Login, ip+"|"+strings.ToLower(strings.TrimSpace(req.Email))) {
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
	if !a.allow(w, r, a.limits.Refresh, clientIP(r, a.trustProxy)) {
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
	if !a.allow(w, r, a.limits.ForgotIP, clientIP(r, a.trustProxy)) {
		return
	}
	var req forgotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.allow(w, r, a.limits.Forgot, strings.ToLower(strings.TrimSpace(req.Email))) {
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
	if !a.allow(w, r, a.limits.ForgotIP, clientIP(r, a.trustProxy)) {
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

Create `internal/adapter/httpapi/account_handlers.go`:
```go
package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func (a *API) handleGetMe(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	user, err := a.accounts.Me(r.Context(), userID)
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
	user, err := a.accounts.UpdateProfile(r.Context(), userID, usecase.ProfileInput{
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
	if err := a.accounts.DeleteAccount(r.Context(), userID, req.Password); err != nil {
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
	langs := usecase.Languages()
	out := make([]languageResponse, 0, len(langs))
	for _, l := range langs {
		out = append(out, languageResponse{Code: l.Code, Name: l.Name, NativeName: l.NativeName})
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/adapter/httpapi/ ./internal/archtest/`
Expected: both packages print `ok`.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/adapter/httpapi
git commit -m "feat(httpapi): account REST controllers, auth guard and rate-limited router" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Admin endpoints, composition root with roles, the `shiksha` binary, packaging and README

**Files:**
- Create:
  - `internal/adapter/httpapi/admin.go`
  - `internal/bootstrap/roles.go`, `internal/bootstrap/bootstrap.go`
  - `cmd/shiksha/main.go`, `cmd/devdb/main.go`
  - `.env.example`, `.dockerignore`, `Dockerfile`, `docker-compose.yml`
- Modify: `README.md` (replace its contents)
- Test: `internal/adapter/httpapi/admin_test.go`, `internal/bootstrap/roles_test.go`, `internal/bootstrap/bootstrap_test.go`, `cmd/shiksha/main_test.go`

**Interfaces:**
- Consumes: everything above
- Produces:
  - `httpapi.Pinger` interface `{Ping(ctx) error}`, `httpapi.NewAdminHandler(Pinger, *atomic.Bool) http.Handler`
  - Roles: `bootstrap.Role`, `bootstrap.RoleAPI`, `bootstrap.KnownRoles`, `bootstrap.ParseRoles(string) ([]Role, error)`
  - App:
    - `bootstrap.New(ctx, config.Config, *slog.Logger) (*bootstrap.App, error)`
    - Methods: `(*App).Migrate(ctx) error`, `(*App).Run(ctx, []Role) error`, `(*App).Close()`
  - CLI: `shiksha serve [--roles=api] [--migrate=true]`, `shiksha migrate`

- [ ] **Step 1: Write the failing admin test and implement the admin handler**

Run: `go get github.com/prometheus/client_golang@latest`

Create `internal/adapter/httpapi/admin_test.go`:
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

Run: `go test ./internal/adapter/httpapi/ -run TestAdminEndpoints`
Expected: FAIL, the build fails because `NewAdminHandler` is undefined.

Create `internal/adapter/httpapi/admin.go`:
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

// NewAdminHandler serves liveness, readiness and Prometheus metrics on the admin port of every role.
// Readiness checks only the database, because a provider outage must not pull every instance out of rotation.
func NewAdminHandler(db Pinger, ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
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

Run: `go mod tidy && go test -race ./internal/adapter/httpapi/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/internal/adapter/httpapi`

- [ ] **Step 2: Write the failing bootstrap tests**

Create `internal/bootstrap/roles_test.go`:
```go
package bootstrap

import (
	"slices"
	"strings"
	"testing"
)

func TestParseRoles(t *testing.T) {
	cases := map[string][]Role{
		"":           {RoleAPI},
		"api":        {RoleAPI},
		" api , api": {RoleAPI},
	}
	for in, want := range cases {
		got, err := ParseRoles(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("ParseRoles(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseRoles("api,worker"); err == nil || !strings.Contains(err.Error(), `"worker"`) || !strings.Contains(err.Error(), "api") {
		t.Errorf("unknown role err = %v, want it to name the role and list known roles", err)
	}
	if _, err := ParseRoles(" , "); err == nil {
		t.Error("blank role list accepted")
	}
}
```

Create `internal/bootstrap/bootstrap_test.go`:
```go
package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func testApp(t *testing.T) *App {
	t.Helper()
	cfg, err := config.LoadFrom(map[string]string{
		"DATABASE_URL":     "postgres://unused", // the pool below is injected directly
		"JWT_SECRET":       strings.Repeat("k", 32),
		"HTTP_ADDR":        "127.0.0.1:0",
		"ADMIN_ADDR":       "127.0.0.1:0",
		"SHUTDOWN_TIMEOUT": "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), pool: dbtest.NewPool(t)}
}

func TestAPIHandlerIsFullyWired(t *testing.T) {
	srv := httptest.NewServer(testApp(t).apiHandler())
	defer srv.Close()
	body := `{"email":"asha@example.com","password":"correct horse","display_name":"Asha","terms_accepted":true}`
	resp, err := http.Post(srv.URL+"/v1/auth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register through the wired stack = %d, want 201", resp.StatusCode)
	}
}

func TestRunShutsDownWhenContextIsCancelled(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, []Role{RoleAPI}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil after graceful shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunRejectsUnimplementedRole(t *testing.T) {
	if err := testApp(t).Run(context.Background(), []Role{"worker"}); err == nil {
		t.Fatal("Run accepted a role it cannot start")
	}
}
```

Run: `go test ./internal/bootstrap/`
Expected: FAIL, the build fails because `ParseRoles`, `App` and `RoleAPI` are undefined.

- [ ] **Step 3: Implement the composition root**

Create `internal/bootstrap/roles.go`:
```go
package bootstrap

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Role is a runnable part of the system. Roles run together in one process for local
// development, or as separate deployments scaled independently (spec §19.4).
type Role string

// RoleAPI serves the stateless REST API.
const RoleAPI Role = "api"

// KnownRoles lists the roles this build can run, in start order. Plan 3 adds "worker"; Plan 4 adds "realtime".
var KnownRoles = []Role{RoleAPI}

// ParseRoles parses a comma-separated role list. An empty string means every known role.
func ParseRoles(s string) ([]Role, error) {
	if strings.TrimSpace(s) == "" {
		return slices.Clone(KnownRoles), nil
	}
	var roles []Role
	for _, part := range strings.Split(s, ",") {
		r := Role(strings.TrimSpace(part))
		if r == "" {
			continue
		}
		if !slices.Contains(KnownRoles, r) {
			known := make([]string, len(KnownRoles))
			for i, k := range KnownRoles {
				known[i] = string(k)
			}
			return nil, fmt.Errorf("unknown role %q (known: %s)", r, strings.Join(known, ", "))
		}
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return nil, errors.New("no roles given")
	}
	return roles, nil
}
```

Create `internal/bootstrap/bootstrap.go`:
```go
// Package bootstrap is the composition root: it builds each role's object graph from the outer
// layers and runs it. It is the only package that knows every layer.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/httpapi"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/crypto"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/mail"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/ratelimit"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const (
	refreshReuseGrace = 20 * time.Second
	passwordResetTTL  = 30 * time.Minute
)

// App owns the process-wide infrastructure shared by every role.
type App struct {
	cfg  config.Config
	log  *slog.Logger
	pool *pgxpool.Pool
}

// New connects to the database. Call Close when done.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &App{cfg: cfg, log: log, pool: pool}, nil
}

// Close releases the database pool.
func (a *App) Close() { a.pool.Close() }

// Migrate applies pending database migrations.
func (a *App) Migrate(ctx context.Context) error { return database.Migrate(ctx, a.pool) }

// Run starts the admin listener plus the given roles. It blocks until ctx is cancelled or a
// server fails, then shuts every server down gracefully within cfg.ShutdownTimeout.
func (a *App) Run(ctx context.Context, roles []Role) error {
	var ready atomic.Bool
	servers := []*http.Server{{
		Addr:              a.cfg.AdminAddr,
		Handler:           httpapi.NewAdminHandler(a.pool, &ready),
		ReadHeaderTimeout: 5 * time.Second,
	}}
	for _, role := range roles {
		switch role {
		case RoleAPI:
			servers = append(servers, &http.Server{
				Addr:              a.cfg.HTTPAddr,
				Handler:           a.apiHandler(),
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       120 * time.Second,
				// No WriteTimeout: the realtime role (Plan 4) serves long-lived WebSockets.
			})
		default:
			return fmt.Errorf("role %q is not implemented in this build", role)
		}
	}

	errCh := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			a.log.Info("listening", "addr", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("serve %s: %w", s.Addr, err)
			}
		}()
	}
	ready.Store(true)
	a.log.Info("started", "roles", roles)

	var runErr error
	select {
	case <-ctx.Done():
		a.log.Info("shutdown signal received")
	case runErr = <-errCh:
	}

	ready.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()
	errs := []error{runErr}
	for _, s := range servers {
		errs = append(errs, s.Shutdown(shutdownCtx))
	}
	return errors.Join(errs...)
}

// apiHandler wires the api role: repositories and infrastructure into use cases into controllers.
func (a *App) apiHandler() http.Handler {
	users, tokens := repository.NewUsers(a.pool), repository.NewTokens(a.pool)
	hasher := crypto.NewArgon2Hasher(2 * runtime.NumCPU())
	auth := usecase.NewAuth(usecase.AuthDeps{
		Users:  users,
		Tokens: tokens,
		Tx:     database.NewTxManager(a.pool),
		Hasher: hasher,
		Access: crypto.NewJWTIssuer(a.cfg.JWTSecret, a.cfg.AccessTokenTTL),
		Opaque: crypto.Opaque{},
		Mailer: a.mailer(),
	}, usecase.AuthConfig{
		RefreshTTL:        a.cfg.RefreshTokenTTL,
		RefreshReuseGrace: refreshReuseGrace,
		ResetTTL:          passwordResetTTL,
		AppBaseURL:        a.cfg.AppBaseURL,
	})
	return httpapi.New(httpapi.Options{
		Auth:     auth,
		Accounts: usecase.NewAccounts(users, hasher, nil),
		Limits: httpapi.DefaultLimits(func(interval time.Duration, burst int) httpapi.RateLimiter {
			return ratelimit.NewMemory(interval, burst)
		}),
		Log:            a.log,
		AllowedOrigins: a.cfg.AllowedOrigins,
		TrustProxy:     a.cfg.TrustProxy,
	})
}

func (a *App) mailer() usecase.Mailer {
	if a.cfg.SMTP.Host == "" {
		a.log.Warn("SMTP_HOST is not set; emails (including password reset links) are written to the log")
		return mail.LogMailer{Log: a.log}
	}
	s := a.cfg.SMTP
	return mail.SMTPMailer{Host: s.Host, Port: s.Port, User: s.User, Pass: s.Pass, From: s.From}
}
```

Run: `go mod tidy && go test -race ./internal/bootstrap/ ./internal/archtest/`
Expected: both packages print `ok`.

- [ ] **Step 4: Write the CLI with its tests**

Create `cmd/shiksha/main_test.go`:
```go
package main

import (
	"strings"
	"testing"
)

func TestRunRejectsBadInvocations(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"no command":      {nil, "missing command"},
		"unknown command": {[]string{"launch"}, `unknown command "launch"`},
		"unknown role":    {[]string{"serve", "--roles=api,teleport"}, `unknown role "teleport"`},
		"bad flag":        {[]string{"serve", "--nope"}, "flag provided but not defined"},
	}
	for name, tc := range cases {
		err := run(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: run(%q) = %v, want error containing %q", name, tc.args, err, tc.want)
		}
	}
}

func TestRunHelp(t *testing.T) {
	if err := run([]string{"help"}); err != nil {
		t.Fatalf("run(help) = %v", err)
	}
}
```

Create `cmd/shiksha/main.go`:
```go
// Command shiksha runs the Shiksha AI backend.
//
//	shiksha serve [--roles=api] [--migrate=true]   run roles (default: every role in this build)
//	shiksha migrate                                 apply database migrations and exit
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bitwizard25/Shiksh_AI/internal/bootstrap"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
)

const usage = `usage:
  shiksha serve [--roles=api] [--migrate=true]   run roles (default: every role in this build)
  shiksha migrate                                 apply database migrations and exit`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "shiksha:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		rolesFlag := fs.String("roles", "", "comma-separated roles to run (default: all)")
		migrate := fs.Bool("migrate", true, "apply database migrations before serving")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("%w\n%s", err, usage)
		}
		roles, err := bootstrap.ParseRoles(*rolesFlag)
		if err != nil {
			return err
		}
		return withApp(func(ctx context.Context, app *bootstrap.App) error {
			if *migrate {
				if err := app.Migrate(ctx); err != nil {
					return err
				}
			}
			return app.Run(ctx, roles)
		})
	case "migrate":
		return withApp(func(ctx context.Context, app *bootstrap.App) error { return app.Migrate(ctx) })
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

// withApp loads configuration, sets up logging and signal handling, opens the app and runs fn.
func withApp(fn func(ctx context.Context, app *bootstrap.App) error) error {
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

	app, err := bootstrap.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer app.Close()
	return fn(ctx, app)
}
```

Run: `go test -race ./cmd/shiksha/`
Expected: `ok  github.com/bitwizard25/Shiksh_AI/cmd/shiksha`

- [ ] **Step 5: Write the dev database command and the packaging files**

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

Create `.env.example`:
```dotenv
# Copy to .env (git-ignored) and adjust. `shiksha` reads .env from its working directory.
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
.superpowers
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
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/shiksha ./cmd/shiksha

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shiksha /shiksha
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/shiksha"]
CMD ["serve"]
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
  api:
    build: .
    command: ["serve", "--roles=api"]
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

- Design (HLD + LLD + diagrams): [docs/design.md](docs/design.md). §19 covers architecture and scaling.
- Implementation plans: [docs/superpowers/plans/](docs/superpowers/plans/)

## Architecture

The code follows classic **Clean Architecture** layers. Source dependencies point inward only, and `internal/archtest` fails the build if they don't.

| Layer | Package | Holds |
|---|---|---|
| Entities | `internal/entity` | Enterprise rules: users, emails, languages, refresh-token replay rule |
| Use cases | `internal/usecase` | Interactors (auth, accounts) and the ports they need |
| Interface adapters | `internal/adapter` | REST controllers (`httpapi`) and Postgres repositories (`repository`) |
| Frameworks & drivers | `internal/infrastructure` | Config, database (pool, TxManager, migrations), crypto, mail, rate limiter |
| Composition root | `internal/bootstrap`, `cmd/shiksha` | Wiring and roles |

The same binary runs as different **roles**, so each part scales on its own. Coordination goes only through Postgres; no Redis is needed.

```bash
shiksha serve --roles=api      # stateless REST API (this plan)
shiksha serve                  # every role in this build (local dev)
shiksha migrate                # apply migrations and exit (release step)
```

## Status

| Plan | Scope | State |
|---|---|---|
| 1 | Clean skeleton, Postgres, accounts API, admin endpoints, `api` role | ✅ this branch |
| 2 | Bhashini ASR/TTS + Gemini gateways, latency benchmark gate | next |
| 3 | Tutoring sessions, prompts, summaries; `worker` role (River jobs), LISTEN/NOTIFY bus | planned |
| 4 | `realtime` role: WebSocket voice core (turn pipeline, barge-in) | planned |
| 5 | Latency polish, hardening, protocol docs | planned |

## Run locally (Windows, no Docker)

You need Go 1.27. In the first terminal, start a local Postgres (the first run downloads it):

```powershell
go run ./cmd/devdb
```

In a second terminal:

```powershell
Copy-Item .env.example .env   # then set JWT_SECRET to a random 32+ character string
go run ./cmd/shiksha serve
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

- **Use-case tests** run on in-memory fakes, with no database.
- **Adapter and infrastructure tests** start a real embedded Postgres, with no Docker. The first run downloads the binaries; run `go test ./internal/infrastructure/database/...` once on its own before the full suite, so the download doesn't race across packages.
- **Existing server:** set `TEST_DATABASE_URL` to a role with `CREATEDB` to test against it instead.

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
- **Redis adapters** for globally exact rate limits, plus **OpenTelemetry tracing**.
- **Web and mobile clients.**
````

- [ ] **Step 7: Full verification and manual smoke test**

Run:
```bash
gofmt -l .
go vet ./...
go test -race ./...
CGO_ENABLED=0 go build -o bin/shiksha.exe ./cmd/shiksha
```
Expected: `gofmt` prints nothing, vet passes, every package prints `ok`, and the build produces `bin/shiksha.exe`.

Then, manually:
1. Start `go run ./cmd/devdb`.
2. Create `.env` from `.env.example` with a 32+ character `JWT_SECRET`.
3. Run `go run ./cmd/shiksha serve`. The log should show `started` with `roles=[api]`.
4. Run the three PowerShell commands from the README. Expected results:
   - register returns tokens;
   - `/v1/me` returns the user;
   - `/readyz` returns `ready`.
5. Press Ctrl+C. It should log `shutdown signal received` and exit 0.
6. Run `go run ./cmd/shiksha migrate`. It should exit 0 with no pending migrations.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/adapter/httpapi internal/bootstrap cmd .env.example .dockerignore Dockerfile docker-compose.yml README.md
git commit -m "feat: composition root with roles, shiksha serve/migrate, admin endpoints, packaging and README" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
