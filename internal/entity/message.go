package entity

import "time"

// Message roles, input modes and statuses.
const (
	RoleLearner = "learner"
	RoleTutor   = "tutor"

	InputVoice = "voice"
	InputText  = "text"

	MessageComplete    = "complete"
	MessageInterrupted = "interrupted"
	MessageFailed      = "failed"
)

// Message is one side of one turn. For a tutor message, Content is the text the learner actually
// heard: when they interrupted the tutor, it stops where the audio stopped.
type Message struct {
	ID        int64
	SessionID string
	TurnNo    int
	Role      string
	InputMode string // learner messages only
	Content   string
	Status    string
	AudioMs   *int
	Latency   []byte // raw JSON timings, opaque to the core
	CreatedAt time.Time
}

// Heard reports whether the message belongs in the conversation the tutor sees: failed tutor
// messages never reached the learner.
func (m Message) Heard() bool { return !(m.Role == RoleTutor && m.Status == MessageFailed) }
