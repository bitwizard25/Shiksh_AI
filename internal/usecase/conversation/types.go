package conversation

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// ---- Providers -------------------------------------------------------------------------------

// ASRRequest is one utterance of 16 kHz mono signed 16-bit little-endian PCM.
type ASRRequest struct {
	Lang       string
	PCM        []byte
	SampleRate int
}

// ASR turns speech into text.
type ASR interface {
	Transcribe(ctx context.Context, r ASRRequest) (string, error)
}

// LLMRequest is a stateless chat request: Postgres owns the history, not the provider.
type LLMRequest struct {
	System   string
	Messages []usecase.ChatMessage
	Fallback bool // use the fallback model; set on the retry after a failure before the first token
}

// Delta is one piece of a streamed reply.
type Delta struct {
	Text    string
	Blocked bool // the provider's safety filter stopped the reply
	Length  bool // the reply hit the output token limit
}

// LLM streams a tutor reply.
type LLM interface {
	Stream(ctx context.Context, r LLMRequest) iter.Seq2[Delta, error]
}

// TTSRequest asks for one sentence of speech.
type TTSRequest struct {
	Lang string
	Text string
}

// TTSResult is mono signed 16-bit little-endian PCM at SampleRate.
type TTSResult struct {
	PCM        []byte
	SampleRate int
}

// TTS turns text into speech.
type TTS interface {
	Synthesize(ctx context.Context, r TTSRequest) (TTSResult, error)
}

// ProviderError is what gateways return so the turn knows whether a retry is worth it.
type ProviderError struct {
	Provider  string
	Op        string
	Retryable bool
	Err       error
}

func (e *ProviderError) Error() string { return e.Provider + " " + e.Op + ": " + e.Err.Error() }
func (e *ProviderError) Unwrap() error { return e.Err }

func retryable(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && pe.Retryable
}

// ClipKind selects a pre-recorded audio clip.
type ClipKind string

const (
	ClipFiller   ClipKind = "filler"   // "hmm, let me think", played when the answer is slow
	ClipRepeat   ClipKind = "repeat"   // "please say that again"
	ClipError    ClipKind = "error"    // "something went wrong"
	ClipRedirect ClipKind = "redirect" // steer back to the lesson
)

// Clip is ready-to-play PCM.
type Clip struct {
	PCM        []byte
	SampleRate int
}

// Clips holds the pre-recorded audio. A missing clip is simply not played.
type Clips interface {
	Get(kind ClipKind, lang string) (Clip, bool)
}

// ---- Output ----------------------------------------------------------------------------------

// Output is the session's way to talk to the client. The WebSocket adapter implements it with a
// bounded outbox and the wire encoding.
type Output interface {
	// SetTurn declares which turn is current. Turn-scoped events of any other turn that are still
	// queued are dropped instead of being written. SetTurn(0) means no turn.
	SetTurn(id uint32)
	// Send queues a turn-scoped event from a turn goroutine, blocking while the queue is full or until ctx ends.
	Send(ctx context.Context, e Event) error
	// TrySend queues a control event without ever blocking. It reports false when the queue is full.
	// Control events are never dropped for being stale.
	TrySend(e Event) bool
	// HeardSegments is how many tutor segments of the turn were completely written to the client.
	HeardSegments(turnID uint32) int
	// Close flushes what is queued (bounded) and closes the connection with a WebSocket close code.
	Close(code int, reason string)
}

// WebSocket close codes the session uses.
const (
	CloseNormal       = 1000
	CloseGoingAway    = 1001
	ClosePolicy       = 1008
	CloseInternal     = 1011
	CloseIdleTimeout  = 4008
	CloseReplaced     = 4009
	CloseSessionEnded = 4010 // the session ended or does not exist; do not reconnect
)

// Metrics records what the session does. Implementations must be safe for concurrent use.
type Metrics interface {
	TurnFirstAudio(lang, inputMode string, d time.Duration)
	TurnStage(stage, lang string, d time.Duration)
	TurnDone(status string)
	ClientFirstPlayback(lang string, d time.Duration)
	BargeIn(kind string) // "live" or "late"
	Continuation()
	FillerPlayed()
	PersistFailure()
	Connections(delta int)
}

// NopMetrics discards everything.
type NopMetrics struct{}

func (NopMetrics) TurnFirstAudio(string, string, time.Duration) {}
func (NopMetrics) TurnStage(string, string, time.Duration)      {}
func (NopMetrics) TurnDone(string)                              {}
func (NopMetrics) ClientFirstPlayback(string, time.Duration)    {}
func (NopMetrics) BargeIn(string)                               {}
func (NopMetrics) Continuation()                                {}
func (NopMetrics) FillerPlayed()                                {}
func (NopMetrics) PersistFailure()                              {}
func (NopMetrics) Connections(int)                              {}

// ---- Config and dependencies ----------------------------------------------------------------

// Config holds the realtime tunables (spec §14).
type Config struct {
	ASRTimeout         time.Duration
	TTSTimeout         time.Duration
	LLMFirstToken      time.Duration
	TurnDeadline       time.Duration // overall limit for one turn
	TTSParallelism     int
	FillerAfter        time.Duration // 0 turns fillers off
	FirstMinRunes      int
	MinUtterance       time.Duration
	MaxUtterance       time.Duration
	ContinuationWindow time.Duration
	IdleTimeout        time.Duration
	MaxSessionDuration time.Duration
	HistoryMaxMessages int
	TurnRatePerMin     int
	TurnBurst          int
	TouchEvery         time.Duration
	FrameMs            int // size of outgoing audio frames
}

// DefaultConfig returns the production values.
func DefaultConfig() Config {
	return Config{
		ASRTimeout: 4 * time.Second, TTSTimeout: 4 * time.Second, LLMFirstToken: 4 * time.Second,
		TurnDeadline: 30 * time.Second, TTSParallelism: 3, FillerAfter: time.Second, FirstMinRunes: 20,
		MinUtterance: 250 * time.Millisecond, MaxUtterance: 30 * time.Second, ContinuationWindow: 2 * time.Second,
		IdleTimeout: 3 * time.Minute, MaxSessionDuration: 45 * time.Minute, HistoryMaxMessages: 20,
		TurnRatePerMin: 30, TurnBurst: 3, TouchEvery: time.Minute, FrameMs: 200,
	}
}

// Audio constants of the uplink.
const (
	InputSampleRate = 16000
	bytesPerSample  = 2
)

// Deps are everything a session needs from the outside.
type Deps struct {
	ASR      ASR
	LLM      LLM
	TTS      TTS
	Clips    Clips
	Store    usecase.TurnStore
	Users    usecase.UserRepository
	Sessions usecase.SessionRepository
	Messages usecase.MessageRepository
	Cfg      Config
	Log      *slog.Logger
	Metrics  Metrics
	Now      func() time.Time
	// OnEnded runs after a session was ended in the database, so it can be summarized.
	OnEnded func(sessionID uuid.UUID)
}

func (d *Deps) fill() {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Metrics == nil {
		d.Metrics = NopMetrics{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Clips == nil {
		d.Clips = noClips{}
	}
	if d.OnEnded == nil {
		d.OnEnded = func(uuid.UUID) {}
	}
}

type noClips struct{}

func (noClips) Get(ClipKind, string) (Clip, bool) { return Clip{}, false }
