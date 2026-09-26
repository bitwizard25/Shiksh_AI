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
	Catalog        *usecase.Catalog // nil offers every language as available
	Limits         Limits
	Log            *slog.Logger
	AllowedOrigins []string
	TrustProxy     bool
}

// API holds controller dependencies. Controllers call use cases only, never repositories.
type API struct {
	auth       *usecase.Auth
	accounts   *usecase.Accounts
	catalog    *usecase.Catalog
	limits     Limits
	log        *slog.Logger
	trustProxy bool
}

// New returns the public API handler with middleware applied.
func New(opts Options) http.Handler {
	catalog := opts.Catalog
	if catalog == nil {
		catalog = usecase.NewCatalog(nil, nil)
	}
	a := &API{auth: opts.Auth, accounts: opts.Accounts, catalog: catalog, limits: opts.Limits, log: opts.Log, trustProxy: opts.TrustProxy}

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
