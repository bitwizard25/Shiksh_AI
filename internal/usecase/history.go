package usecase

import "github.com/bitwizard25/Shiksh_AI/internal/entity"

// Roles in a ChatMessage, as the LLM sees them.
const (
	ChatUser  = "user"
	ChatModel = "model"
)

// InterruptedSuffix marks a tutor message the learner cut off.
const InterruptedSuffix = " (learner interrupted)"

// ChatMessage is one message of the conversation sent to the LLM.
type ChatMessage struct {
	Role string
	Text string
}

// BuildHistory turns stored messages (oldest first) into the LLM conversation. It keeps the last
// max messages that reached the learner: failed tutor messages are skipped, and an interrupted
// tutor message is the text the learner heard followed by InterruptedSuffix.
func BuildHistory(msgs []entity.Message, max int) []ChatMessage {
	out := make([]ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if !m.Heard() || m.Content == "" {
			continue
		}
		cm := ChatMessage{Role: ChatUser, Text: m.Content}
		if m.Role == entity.RoleTutor {
			cm.Role = ChatModel
			if m.Status == entity.MessageInterrupted {
				cm.Text += InterruptedSuffix
			}
		}
		out = append(out, cm)
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}
