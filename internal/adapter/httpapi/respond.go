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
