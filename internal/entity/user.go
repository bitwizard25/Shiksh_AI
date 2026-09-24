package entity

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
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

// ParseDisplayName trims raw, checks it is 1-80 runes, and rejects control and bidi-override
// characters that could be used to spoof how the name renders.
func ParseDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(name)
	if n == 0 {
		return "", &ValidationError{Field: "display_name", Message: "is required"}
	}
	if n > MaxDisplayNameRunes {
		return "", &ValidationError{Field: "display_name", Message: fmt.Sprintf("must be at most %d characters", MaxDisplayNameRunes)}
	}
	for _, r := range name {
		if isUnsupportedDisplayNameRune(r) {
			return "", &ValidationError{Field: "display_name", Message: "contains unsupported characters"}
		}
	}
	return name, nil
}

// isUnsupportedDisplayNameRune reports whether r is a C0/C1 control character, a bidi control
// (which can be used to make a name render misleadingly, e.g. right-to-left override), or
// U+FEFF (byte order mark). ZWJ (U+200D) and ZWNJ (U+200C) are intentionally not rejected:
// Indic scripts need them to render conjunct consonants correctly.
func isUnsupportedDisplayNameRune(r rune) bool {
	const (
		bom              = rune(0xFEFF) // BYTE ORDER MARK / ZERO WIDTH NO-BREAK SPACE
		lrm              = rune(0x200E) // LEFT-TO-RIGHT MARK
		rlm              = rune(0x200F) // RIGHT-TO-LEFT MARK
		alm              = rune(0x061C) // ARABIC LETTER MARK
		bidiEmbedFirst   = rune(0x202A) // LEFT-TO-RIGHT EMBEDDING
		bidiEmbedLast    = rune(0x202E) // RIGHT-TO-LEFT OVERRIDE
		bidiIsolateFirst = rune(0x2066) // LEFT-TO-RIGHT ISOLATE
		bidiIsolateLast  = rune(0x2069) // POP DIRECTIONAL ISOLATE
	)
	if unicode.Is(unicode.Cc, r) {
		return true
	}
	switch r {
	case bom, lrm, rlm, alm:
		return true
	}
	if r >= bidiEmbedFirst && r <= bidiEmbedLast {
		return true
	}
	if r >= bidiIsolateFirst && r <= bidiIsolateLast {
		return true
	}
	return false
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
