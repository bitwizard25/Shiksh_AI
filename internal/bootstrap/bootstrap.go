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
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/httpapi"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/ws"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/crypto"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/mail"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/metrics"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/ratelimit"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
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
	prov Providers // the guarded speech and language stack; set by Run before any role is wired

	summariesOnce sync.Once
	summaries     *usecase.Summaries

	reg   prometheus.Registerer // where the provider and realtime metrics register; nil means unregistered/private
	voice *metrics.Voice
	hub   *conversation.Hub // set by Run when the realtime role is on
}

// New connects to the database. Call Close when done.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &App{cfg: cfg, log: log, pool: pool, reg: prometheus.DefaultRegisterer}, nil
}

// Close releases the database pool.
func (a *App) Close() { a.pool.Close() }

// Migrate applies pending database migrations.
func (a *App) Migrate(ctx context.Context) error { return database.Migrate(ctx, a.pool) }

// Run starts the admin listener plus the given roles. It blocks until ctx is cancelled or a
// server fails, then shuts every server down gracefully within cfg.ShutdownTimeout.
func (a *App) Run(ctx context.Context, roles []Role) error {
	prov, err := BuildProviders(ctx, a.cfg.Providers, a.reg, a.log)
	if err != nil {
		return fmt.Errorf("build providers: %w", err)
	}
	if a.cfg.Providers.Mode == config.ProvidersFake {
		a.log.Warn("PROVIDERS=fake: speech and language are simulated; set PROVIDERS=real with the Bhashini and Gemini keys for real providers")
	}
	a.prov = prov

	var ready atomic.Bool
	servers := []*http.Server{{
		Addr:              a.cfg.AdminAddr,
		Handler:           httpapi.NewAdminHandler(a.pool, &ready),
		ReadHeaderTimeout: 5 * time.Second,
	}}
	if slices.Contains(roles, RoleRealtime) {
		a.hub = a.newHub()
	}
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()
	var workers sync.WaitGroup
	for _, role := range roles {
		switch role {
		case RoleAPI, RoleRealtime:
			// Both roles share one listener when they run together; the load balancer splits them in production.
		case RoleWorker:
			sweeper := a.sweeper()
			workers.Add(1)
			go func() {
				defer workers.Done()
				sweeper.Run(workerCtx, a.cfg.SweepInterval)
			}()
		default:
			return fmt.Errorf("role %q is not implemented in this build", role)
		}
	}
	if slices.Contains(roles, RoleAPI) || slices.Contains(roles, RoleRealtime) {
		servers = append(servers, &http.Server{
			Addr:              a.cfg.HTTPAddr,
			Handler:           a.httpHandler(roles),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			// No WriteTimeout: WebSockets are long-lived.
		})
	}

	errCh := make(chan error, len(servers))
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
	if a.hub != nil {
		// http.Server.Shutdown does not wait for hijacked WebSockets, so live sessions are drained first.
		errs = append(errs, a.hub.Drain(shutdownCtx))
	}
	for _, s := range servers {
		errs = append(errs, s.Shutdown(shutdownCtx))
	}
	wg.Wait()
	stopWorkers()
	workers.Wait()
	// Summaries queued by in-flight requests and the sweeper finish before the pool closes.
	errs = append(errs, a.summariesService().Wait(shutdownCtx))
	close(errCh)
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// apiHandler wires the api role: repositories and infrastructure into use cases into controllers.
func (a *App) apiHandler(prov Providers) http.Handler {
	users, tokens := repository.NewUsers(a.pool), repository.NewTokens(a.pool)
	// GOMAXPROCS honors a container CPU quota (e.g. Kubernetes cpu limits via GOMAXPROCS or an
	// automaxprocs-style setter), unlike NumCPU, which always reports the host's core count.
	hasher := crypto.NewArgon2Hasher(2 * runtime.GOMAXPROCS(0))
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
	sessions := a.sessionsService()
	return httpapi.New(httpapi.Options{
		Auth:     auth,
		Accounts: usecase.NewAccounts(users, hasher, nil),
		Catalog:  usecase.NewCatalog(a.cfg.Providers.EnabledLanguages, prov.Availability),
		Sessions: sessions,
		WSURL:    a.cfg.WSURL,
		Limits: httpapi.DefaultLimits(func(interval time.Duration, burst int) httpapi.RateLimiter {
			return ratelimit.NewMemory(interval, burst)
		}),
		Log:            a.log,
		AllowedOrigins: a.cfg.AllowedOrigins,
		TrustProxy:     a.cfg.TrustProxy,
	})
}

// summariesService returns the process-wide session summarizer, built on first use. It writes
// summaries with the providers' LLM on GEMINI_SUMMARY_MODEL; without an LLM, summaries are off.
func (a *App) summariesService() *usecase.Summaries {
	a.summariesOnce.Do(func() {
		var gen usecase.TextGenerator
		if a.prov.LLM != nil {
			gen = summaryGenerator{llm: a.prov.LLM, model: a.cfg.Providers.GeminiSummaryModel}
		} else {
			a.log.Warn("no LLM provider is configured; session summaries are disabled")
		}
		a.summaries = usecase.NewSummaries(repository.NewSessions(a.pool), repository.NewMessages(a.pool), gen, a.log)
	})
	return a.summaries
}

// summaryGenerator adapts the LLM port to usecase.TextGenerator: one system prompt, one user message.
type summaryGenerator struct {
	llm   conversation.LLM
	model string
}

func (g summaryGenerator) Complete(ctx context.Context, system, user string) (string, error) {
	return g.llm.Complete(ctx, conversation.LLMRequest{
		Model:    g.model,
		System:   system,
		Messages: []conversation.ChatMessage{{Role: conversation.RoleUser, Text: user}},
	})
}

// sweeper wires the worker role's periodic cleanup.
func (a *App) sweeper() *usecase.Sweeper {
	return usecase.NewSweeper(usecase.SweeperDeps{
		Sessions:  repository.NewSessions(a.pool),
		Tickets:   repository.NewTickets(a.pool),
		Tokens:    repository.NewTokens(a.pool),
		Summaries: a.summariesService(),
		Log:       a.log,
	}, a.cfg.SessionResumeGrace, nil)
}

// sessionsService wires the session use case. With the realtime role in this process, ending a
// session over REST reaches the live connection through the hub.
func (a *App) sessionsService() *usecase.Sessions {
	d := usecase.SessionsDeps{
		Users:     repository.NewUsers(a.pool),
		Sessions:  repository.NewSessions(a.pool),
		Messages:  repository.NewMessages(a.pool),
		Tickets:   repository.NewTickets(a.pool),
		Tx:        database.NewTxManager(a.pool),
		Opaque:    crypto.Opaque{},
		Summaries: a.summariesService(),
	}
	if a.hub != nil {
		d.Live = a.hub
	}
	return usecase.NewSessions(d, usecase.SessionsConfig{TicketTTL: a.cfg.WSTicketTTL}, nil)
}

// httpHandler serves the public listener: REST for the api role, GET /v1/ws for the realtime role.
func (a *App) httpHandler(roles []Role) http.Handler {
	mux := http.NewServeMux()
	if slices.Contains(roles, RoleAPI) {
		mux.Handle("/", a.apiHandler(a.prov))
	}
	if a.hub != nil {
		mux.Handle("GET /v1/ws", ws.NewHandler(ws.Options{
			Hub: a.hub, Tickets: a.sessionsService(), Log: a.log, AllowedOrigins: a.cfg.AllowedOrigins,
			Dropped: a.voiceMetrics().OutboxDropped,
		}))
	}
	return mux
}

func (a *App) voiceMetrics() *metrics.Voice {
	if a.voice == nil {
		reg := a.reg
		if reg == nil {
			reg = prometheus.NewRegistry()
		}
		a.voice = metrics.NewVoice(reg)
	}
	return a.voice
}

// newHub builds the realtime hub on the guarded providers (fake or real, per PROVIDERS), their
// clips, the stores and the realtime settings.
func (a *App) newHub() *conversation.Hub {
	v := a.cfg.Voice
	cfg := conversation.DefaultConfig()
	cfg.ASRTimeout, cfg.TTSTimeout, cfg.LLMFirstToken = v.ASRTimeout, v.TTSTimeout, v.LLMFirstToken
	cfg.TTSParallelism, cfg.FillerAfter, cfg.FirstMinRunes = v.TTSParallelism, v.FillerAfter, v.FirstMinRunes
	cfg.MinUtterance, cfg.MaxUtterance, cfg.ContinuationWindow = v.MinUtterance, v.MaxUtterance, v.ContinuationWindow
	cfg.IdleTimeout, cfg.MaxSessionDuration = v.IdleTimeout, v.MaxSessionDuration
	cfg.TurnRatePerMin, cfg.TurnBurst, cfg.HistoryMaxMessages = v.TurnRatePerMin, v.TurnBurst, v.HistoryMaxMessages
	cfg.FallbackModel = a.cfg.Providers.GeminiFallbackModel
	summaries := a.summariesService()
	return conversation.NewHub(conversation.Deps{
		ASR: a.prov.ASR, LLM: a.prov.LLM, TTS: a.prov.TTS, Clips: a.prov.Clips,
		Store:    repository.NewTurns(a.pool),
		Users:    repository.NewUsers(a.pool),
		Sessions: repository.NewSessions(a.pool),
		Messages: repository.NewMessages(a.pool),
		Cfg:      cfg, Log: a.log, Metrics: a.voiceMetrics(),
		OnEnded: summaries.Submit,
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
