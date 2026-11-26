package gateway

import (
	"context"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// SpeechProvider is the platform-independent contract implemented by every speech backend
// (such as Bhashini or Sarvam AI). Switching speech platforms only changes the config and
// bootstrap plane; the guard, metrics, warmer, HTTP API, WebSocket core, and CLI remain untouched.
type SpeechProvider interface {
	conversation.ASR
	conversation.TTS
	usecase.LanguageAvailability
	Name() string
	Warm(ctx context.Context, langs []string)
	Endpoints() []string
}
