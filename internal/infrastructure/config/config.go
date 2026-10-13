// Package config loads process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds runtime settings.
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

	// WSURL is the public WebSocket URL handed to clients along with a ticket.
	WSURL string `env:"WS_URL" envDefault:"ws://localhost:8080/v1/ws"`
	// WSTicketTTL is how long a WebSocket ticket can be redeemed.
	WSTicketTTL time.Duration `env:"WS_TICKET_TTL" envDefault:"60s"`
	// SessionResumeGrace is how long a session may sit idle before the sweeper ends it.
	SessionResumeGrace time.Duration `env:"SESSION_RESUME_GRACE" envDefault:"5m"`
	// SweepInterval is how often the worker role sweeps stale sessions and expired tokens.
	SweepInterval time.Duration `env:"SWEEP_INTERVAL" envDefault:"60s"`
	SMTP          SMTPConfig

	// Providers selects and configures the speech and language gateways (PROVIDERS=fake or real).
	Providers ProviderConfig
	Voice     VoiceConfig
}

// VoiceConfig holds the realtime tunables (spec §14).
type VoiceConfig struct {
	ASRTimeout         time.Duration `env:"ASR_TIMEOUT" envDefault:"4s"`
	TTSTimeout         time.Duration `env:"TTS_TIMEOUT" envDefault:"4s"`
	LLMFirstToken      time.Duration `env:"LLM_FIRST_TOKEN_TIMEOUT" envDefault:"4s"`
	TTSParallelism     int           `env:"TTS_PARALLELISM" envDefault:"3"`
	FillerAfter        time.Duration `env:"FILLER_AFTER" envDefault:"1s"` // 0 turns fillers off
	FirstMinRunes      int           `env:"FIRST_MIN_RUNES" envDefault:"20"`
	MinUtterance       time.Duration `env:"MIN_UTTERANCE" envDefault:"250ms"`
	MaxUtterance       time.Duration `env:"MAX_UTTERANCE" envDefault:"30s"`
	ContinuationWindow time.Duration `env:"CONTINUATION_WINDOW" envDefault:"2s"`
	IdleTimeout        time.Duration `env:"IDLE_TIMEOUT" envDefault:"3m"`
	MaxSessionDuration time.Duration `env:"MAX_SESSION_DURATION" envDefault:"45m"`
	TurnRatePerMin     int           `env:"TURN_RATE_PER_MIN" envDefault:"30"`
	TurnBurst          int           `env:"TURN_BURST" envDefault:"3"`
	HistoryMaxMessages int           `env:"HISTORY_MAX_MESSAGES" envDefault:"20"`
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
	cfg.Providers.normalize()
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
	if c.WSTicketTTL <= 0 || c.SessionResumeGrace <= 0 || c.SweepInterval <= 0 {
		errs = append(errs, errors.New("WS_TICKET_TTL, SESSION_RESUME_GRACE and SWEEP_INTERVAL must be positive"))
	}
	if u, err := url.Parse(c.WSURL); err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		errs = append(errs, fmt.Errorf("WS_URL %q must be an absolute ws:// or wss:// URL", c.WSURL))
	}
	if err := c.Voice.validate(); err != nil {
		errs = append(errs, err)
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.LogLevel)); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL %q must be one of debug, info, warn, error", c.LogLevel))
	}
	if err := c.Providers.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

func (v VoiceConfig) validate() error {
	var errs []error
	for name, d := range map[string]time.Duration{
		"ASR_TIMEOUT": v.ASRTimeout, "TTS_TIMEOUT": v.TTSTimeout, "LLM_FIRST_TOKEN_TIMEOUT": v.LLMFirstToken,
		"MIN_UTTERANCE": v.MinUtterance, "MAX_UTTERANCE": v.MaxUtterance, "CONTINUATION_WINDOW": v.ContinuationWindow,
		"IDLE_TIMEOUT": v.IdleTimeout, "MAX_SESSION_DURATION": v.MaxSessionDuration,
	} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if v.FillerAfter < 0 {
		errs = append(errs, errors.New("FILLER_AFTER must not be negative"))
	}
	if v.MinUtterance >= v.MaxUtterance {
		errs = append(errs, errors.New("MIN_UTTERANCE must be shorter than MAX_UTTERANCE"))
	}
	for name, n := range map[string]int{
		"TTS_PARALLELISM": v.TTSParallelism, "FIRST_MIN_RUNES": v.FirstMinRunes, "TURN_RATE_PER_MIN": v.TurnRatePerMin,
		"TURN_BURST": v.TurnBurst, "HISTORY_MAX_MESSAGES": v.HistoryMaxMessages,
	} {
		if n <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	return errors.Join(errs...)
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
