package entity

import (
	"time"

	"github.com/google/uuid"
)

// RefreshToken is one link in a rotation family. Only its hash is ever stored.
type RefreshToken struct {
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	ExpiresAt time.Time
	UsedAt    *time.Time // set when the token was rotated
	RevokedAt *time.Time
}

// IsReplay reports whether presenting this token at `now` signals theft: it was already rotated
// more than `grace` ago. Reuse inside the grace window is a benign race, such as two parallel
// refreshes when an app resumes or a client retry, and must not log the learner out.
func (t RefreshToken) IsReplay(now time.Time, grace time.Duration) bool {
	return t.UsedAt != nil && now.Sub(*t.UsedAt) > grace
}
