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
