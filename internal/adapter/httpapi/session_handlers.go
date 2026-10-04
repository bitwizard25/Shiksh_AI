package httpapi

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

type subjectResponse struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (a *API) handleSubjects(w http.ResponseWriter, _ *http.Request) {
	subjects := usecase.Subjects()
	out := make([]subjectResponse, 0, len(subjects))
	for _, s := range subjects {
		out = append(out, subjectResponse{Code: s.Code, Name: s.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

type sessionResponse struct {
	ID           string     `json:"id"`
	Subject      string     `json:"subject"`
	Language     string     `json:"language"`
	Grade        *int       `json:"grade"`
	Status       string     `json:"status"`
	EndReason    string     `json:"end_reason,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	TurnCount    int        `json:"turn_count"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
	EndedAt      *time.Time `json:"ended_at"`
}

func toSessionResponse(s entity.TutoringSession) sessionResponse {
	return sessionResponse{
		ID: s.ID.String(), Subject: s.Subject, Language: s.Language, Grade: s.Grade, Status: s.Status,
		EndReason: s.EndReason, Summary: s.Summary, TurnCount: s.TurnCount, CreatedAt: s.CreatedAt,
		StartedAt: s.StartedAt, LastActiveAt: s.LastActiveAt, EndedAt: s.EndedAt,
	}
}

type messageResponse struct {
	TurnNo    int       `json:"turn_no"`
	Role      string    `json:"role"`
	InputMode string    `json:"input_mode,omitempty"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	AudioMs   *int      `json:"audio_ms,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type wsResponse struct {
	URL       string    `json:"url"`
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *API) toWS(t usecase.Ticket) wsResponse {
	return wsResponse{URL: a.wsURL, Ticket: t.Token, ExpiresAt: t.ExpiresAt}
}

type createSessionRequest struct {
	Subject  string `json:"subject"`
	Language string `json:"language"`
	Grade    *int   `json:"grade"`
}

func (a *API) handleCreateSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var req createSessionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.allow(w, r, a.limits.CreateSession, userID.String()) {
		return
	}
	started, err := a.sessions.Create(r.Context(), userID, usecase.SessionInput{
		Subject: req.Subject, Language: req.Language, Grade: req.Grade,
	})
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session": toSessionResponse(started.Session),
		"ws":      a.toWS(started.Ticket),
	})
}

func (a *API) handleSessionTicket(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	id, ok := pathSessionID(w, r)
	if !ok || !a.allow(w, r, a.limits.CreateSession, userID.String()) {
		return
	}
	ticket, err := a.sessions.NewTicket(r.Context(), userID, id)
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ws": a.toWS(ticket)})
}

func (a *API) handleEndSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	id, ok := pathSessionID(w, r)
	if !ok {
		return
	}
	if err := a.sessions.End(r.Context(), userID, id); err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErrorDetail(w, r, http.StatusBadRequest, errorDetail{Code: "validation_failed", Message: "limit must be a positive integer", Field: "limit"})
			return
		}
		limit = n
	}
	var after *usecase.SessionCursor
	if v := r.URL.Query().Get("cursor"); v != "" {
		c, err := decodeCursor(v)
		if err != nil {
			writeErrorDetail(w, r, http.StatusBadRequest, errorDetail{Code: "validation_failed", Message: "cursor is invalid", Field: "cursor"})
			return
		}
		after = &c
	}
	page, err := a.sessions.List(r.Context(), userID, limit, after)
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	items := make([]sessionResponse, 0, len(page.Items))
	for _, s := range page.Items {
		items = append(items, toSessionResponse(s))
	}
	var next *string
	if page.Next != nil {
		c := encodeCursor(*page.Next)
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (a *API) handleGetSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	id, ok := pathSessionID(w, r)
	if !ok {
		return
	}
	detail, err := a.sessions.Get(r.Context(), userID, id)
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	msgs := make([]messageResponse, 0, len(detail.Messages))
	for _, m := range detail.Messages {
		msgs = append(msgs, messageResponse{
			TurnNo: m.TurnNo, Role: m.Role, InputMode: m.InputMode, Content: m.Content,
			Status: m.Status, AudioMs: m.AudioMs, CreatedAt: m.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": toSessionResponse(detail.Session), "messages": msgs})
}

// pathSessionID parses {id}. A malformed id is a 404, the same as an unknown one.
func pathSessionID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "resource not found")
		return uuid.Nil, false
	}
	return id, true
}

func encodeCursor(c usecase.SessionCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", c.CreatedAt.UnixMicro(), c.ID)))
}

func decodeCursor(s string) (usecase.SessionCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return usecase.SessionCursor{}, err
	}
	micros, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return usecase.SessionCursor{}, fmt.Errorf("malformed cursor")
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return usecase.SessionCursor{}, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return usecase.SessionCursor{}, err
	}
	return usecase.SessionCursor{CreatedAt: time.UnixMicro(n).UTC(), ID: u}, nil
}
