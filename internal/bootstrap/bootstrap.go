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
