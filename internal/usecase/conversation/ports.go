// Package conversation holds the realtime voice use case. In Plan 2 it declares the ports the
// speech and language providers implement; the session actor and turn pipeline arrive in Plan 4.
package conversation

import (
	"context"
	"errors"
	"fmt"
	"iter"
)

// ASRRequest is one utterance to transcribe: 16-bit little-endian mono PCM.
type ASRRequest struct {
	Lang       string
	PCM        []byte
	SampleRate int
}

// ASRResult is a transcript. Text is empty when nothing usable was said.
type ASRResult struct {
	Text string
}

// ASR converts speech to text.
type ASR interface {
	Transcribe(ctx context.Context, req ASRRequest) (ASRResult, error)
}

// Role is who spoke a chat message.
type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// ChatMessage is one message of the conversation history.
type ChatMessage struct {
	Role Role
	Text string
}

// LLMRequest asks the model for the tutor's next reply.
type LLMRequest struct {
	Model           string // optional override of the provider's default model
	System          string
	Messages        []ChatMessage
	Temperature     *float32 // nil keeps the provider/model default
	MaxOutputTokens int32
}

// Delta is one piece of a streamed reply. Blocked is true when safety filters stopped the reply;
// FinishReason is set on the chunk that ends the stream (e.g. "STOP", "MAX_TOKENS", "SAFETY").
type Delta struct {
	Text         string
	Blocked      bool
	FinishReason string
}

// LLM generates the tutor's replies.
type LLM interface {
	// Stream yields the reply as it is generated. A yielded error ends the stream.
	Stream(ctx context.Context, req LLMRequest) iter.Seq2[Delta, error]
	// Complete returns a whole reply (used for session summaries).
	Complete(ctx context.Context, req LLMRequest) (string, error)
}

// TTSRequest is text to speak.
type TTSRequest struct {
	Lang   string
	Text   string
	Gender string // "female" or "male"; empty lets the gateway choose
}

// TTSResult is 16-bit little-endian mono PCM at the provider's sample rate.
type TTSResult struct {
	PCM        []byte
	SampleRate int
	DurationMs int
}

// TTS converts text to speech.
type TTS interface {
	Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error)
}

var (
	// ErrProviderUnavailable means a provider is shedding load: its circuit breaker is open.
	ErrProviderUnavailable = errors.New("provider unavailable")
	// ErrLanguageUnsupported means the provider has no model for the requested language.
	ErrLanguageUnsupported = errors.New("language not supported by provider")
	// ErrResponseBlocked means the model's safety filters refused the request.
	ErrResponseBlocked = errors.New("response blocked by safety filters")
)

// ProviderError describes a failed call to an external speech or language provider.
type ProviderError struct {
	Provider  string // e.g. "bhashini", "gemini"
	Op        string // e.g. "asr", "tts", "llm", "config"
	Status    int    // HTTP status; 0 when no response was received
	Retryable bool   // true for timeouts, network errors, 429 and 5xx
	Err       error
}

func (e *ProviderError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s %s: status %d: %v", e.Provider, e.Op, e.Status, e.Err)
	}
	return fmt.Sprintf("%s %s: %v", e.Provider, e.Op, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// IsRetryable reports whether err is a provider failure worth one more attempt.
func IsRetryable(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && pe.Retryable
}
