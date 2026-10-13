package entity

import (
	"slices"
	"time"

	"github.com/google/uuid"
)

// Session lifecycle states.
const (
	SessionCreated = "created"
	SessionActive  = "active"
	SessionEnded   = "ended"
)

// Reasons a session ended.
const (
	EndLearner     = "learner"
	EndIdle        = "idle"
	EndMaxDuration = "max_duration"
	EndSwept       = "swept"
)

// MinTurnsForSummary is the fewest turns a session needs before it is worth summarizing.
const MinTurnsForSummary = 2

// Subject is something the tutor can teach.
type Subject struct {
	Code string
	Name string
}

var subjects = []Subject{
	{"math", "Mathematics"},
	{"science", "Science"},
	{"physics", "Physics"},
	{"chemistry", "Chemistry"},
	{"biology", "Biology"},
	{"english", "English"},
	{"hindi", "Hindi"},
	{"social_science", "Social Science"},
	{"computer_science", "Computer Science"},
	{"general", "General"},
}

// Subjects returns the subject catalogue in display order. The caller may modify the result.
func Subjects() []Subject { return slices.Clone(subjects) }

// LookupSubject returns the subject with the given code.
func LookupSubject(code string) (Subject, bool) {
	for _, s := range subjects {
		if s.Code == code {
			return s, true
		}
	}
	return Subject{}, false
}

// ValidateSubject checks code is in the catalogue.
func ValidateSubject(code string) error {
	if _, ok := LookupSubject(code); !ok {
		return &ValidationError{Field: "subject", Message: "is not a supported subject"}
	}
	return nil
}

// TutoringSession is one tutoring conversation.
type TutoringSession struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Subject      string
	Language     string
	Grade        *int
	Status       string
	ConnEpoch    int
	EndReason    string
	Summary      string
	TurnCount    int
	CreatedAt    time.Time
	StartedAt    *time.Time
	LastActiveAt *time.Time
	EndedAt      *time.Time
}

// Ended reports whether the session is over.
func (s TutoringSession) Ended() bool { return s.Status == SessionEnded }

// NeedsSummary reports whether the session has enough turns to be worth summarizing.
func (s TutoringSession) NeedsSummary() bool { return s.TurnCount >= MinTurnsForSummary }
