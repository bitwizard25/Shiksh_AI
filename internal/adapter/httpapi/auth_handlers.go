package httpapi

import (
	"net/http"
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
	if !a.allow(w, r, a.limits.Register, limiterIP(clientIP(r, a.trustProxy))) {
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
	ip := limiterIP(clientIP(r, a.trustProxy))
	if !a.allow(w, r, a.limits.LoginIP, ip) {
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.allow(w, r, a.limits.Login, ip+"|"+emailKey(req.Email)) {
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
	if !a.allow(w, r, a.limits.Refresh, limiterIP(clientIP(r, a.trustProxy))) {
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
	if !a.allow(w, r, a.limits.ForgotIP, limiterIP(clientIP(r, a.trustProxy))) {
		return
	}
	var req forgotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.allow(w, r, a.limits.Forgot, emailKey(req.Email)) {
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
	if !a.allow(w, r, a.limits.ForgotIP, limiterIP(clientIP(r, a.trustProxy))) {
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
