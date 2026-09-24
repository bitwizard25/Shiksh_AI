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
