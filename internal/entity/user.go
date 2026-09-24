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
