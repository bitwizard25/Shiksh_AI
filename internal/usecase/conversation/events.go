package conversation

import "encoding/json"

// Event is something the server sends to the client. The WebSocket adapter chooses the wire form.
type Event interface {
	// Turn returns the turn the event belongs to. scoped is false for events of the connection.
	Turn() (id uint32, scoped bool)
}

// Wire values.
const (
	StatusComplete    = "complete"
	StatusInterrupted = "interrupted"
	StatusFailed      = "failed"

	ErrASRFailed          = "asr_failed"
	ErrLLMFailed          = "llm_failed"
	ErrTTSFailed          = "tts_failed"
	ErrUtteranceTooLong   = "utterance_too_long"
	ErrRateLimited        = "rate_limited"
	ErrBadMessage         = "bad_message"
	ErrStorageUnavailable = "storage_unavailable"
	ErrServerShutdown     = "server_shutdown"
	ErrSessionReplaced    = "session_replaced"
	ErrIdleTimeout        = "idle_timeout"
)

// Audio frame kinds.
const (
	KindSpeech byte = 1
	KindFiller byte = 2
	KindClip   byte = 3
)

// AudioIn describes the uplink format.
type AudioIn struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
}

// AudioOut describes the downlink format. The sample rate is carried in every frame header.
type AudioOut struct {
	Encoding string `json:"encoding"`
	Channels int    `json:"channels"`
}

// Limits tells the client the session's timing limits.
type Limits struct {
	MaxUtteranceMs int64 `json:"max_utterance_ms"`
	IdleTimeoutMs  int64 `json:"idle_timeout_ms"`
}

// HistoryItem is one earlier message, sent when a session resumes.
type HistoryItem struct {
	TurnNo  int    `json:"turn_no"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// SessionReady is the first message of a connection.
type SessionReady struct {
	SessionID  string        `json:"session_id"`
	Language   string        `json:"language"`
	Subject    string        `json:"subject"`
	Resumed    bool          `json:"resumed"`
	NextTurnID uint32        `json:"next_turn_id"`
	AudioIn    AudioIn       `json:"audio_in"`
	AudioOut   AudioOut      `json:"audio_out"`
	Limits     Limits        `json:"limits"`
	History    []HistoryItem `json:"history"`
}

func (SessionReady) Turn() (uint32, bool) { return 0, false }

// TurnStarted is sent at speech.end (or text.input), before the transcript is known.
type TurnStarted struct {
	TurnID    uint32 `json:"turn_id"`
	InputMode string `json:"input_mode"`
}

func (e TurnStarted) Turn() (uint32, bool) { return e.TurnID, true }

// Transcript is what the learner said.
type Transcript struct {
	TurnID uint32 `json:"turn_id"`
	Text   string `json:"text"`
}

func (e Transcript) Turn() (uint32, bool) { return e.TurnID, true }

// TranscriptEmpty means speech recognition heard nothing usable.
type TranscriptEmpty struct {
	TurnID uint32 `json:"turn_id"`
}

func (e TranscriptEmpty) Turn() (uint32, bool) { return e.TurnID, true }

// TutorSegment is the caption of one spoken segment, sent just before that segment's audio.
type TutorSegment struct {
	TurnID     uint32 `json:"turn_id"`
	ChunkSeq   int    `json:"chunk_seq"`
	Text       string `json:"text"`
	SampleRate int    `json:"sample_rate"`
	NoAudio    bool   `json:"-"` // speech synthesis failed: the caption is all the learner gets
}

func (e TutorSegment) Turn() (uint32, bool) { return e.TurnID, true }

// TutorDone ends a turn. For an interruption the actor sends it as a control event.
type TutorDone struct {
	TurnID  uint32  `json:"turn_id"`
	Status  string  `json:"status"`
	Latency Latency `json:"latency"`
	// Control marks events the actor sends itself, which must not be dropped as stale.
	Control bool `json:"-"`
}

func (e TutorDone) Turn() (uint32, bool) { return e.TurnID, !e.Control }

// ErrorEvent reports a problem. TurnID is set for turn errors.
type ErrorEvent struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Retryable bool    `json:"retryable"`
	TurnID    *uint32 `json:"turn_id,omitempty"`
}

func (e ErrorEvent) Turn() (uint32, bool) {
	if e.TurnID == nil {
		return 0, false
	}
	return *e.TurnID, true
}

// SessionEnded says why the session is over.
type SessionEnded struct {
	Reason string `json:"reason"`
}

func (SessionEnded) Turn() (uint32, bool) { return 0, false }

// Pong answers a ping.
type Pong struct {
	T json.RawMessage `json:"t,omitempty"`
}

func (Pong) Turn() (uint32, bool) { return 0, false }

// AudioFrame is one binary frame of PCM s16le mono.
type AudioFrame struct {
	Kind       byte
	LastOfSeg  bool
	LastOfTurn bool
	TurnID     uint32
	ChunkSeq   uint16
	Part       uint16
	SampleRate int
	PCM        []byte
}

func (e AudioFrame) Turn() (uint32, bool) { return e.TurnID, true }

// Client message types.
const (
	MsgSpeechStart     = "speech.start"
	MsgSpeechEnd       = "speech.end"
	MsgSpeechCancel    = "speech.cancel"
	MsgTextInput       = "text.input"
	MsgPlaybackStarted = "playback.started"
	MsgPlaybackDone    = "playback.done"
	MsgSessionEnd      = "session.end"
	MsgPing            = "ping"
)

// Played says which tutor segment the learner had fully heard when they started speaking.
type Played struct {
	TurnID   uint32 `json:"turn_id"`
	ChunkSeq int    `json:"chunk_seq"`
}

// ClientMsg is a decoded JSON message from the client.
type ClientMsg struct {
	Type   string
	Played *Played
	Text   string
	TurnID uint32
	T      json.RawMessage
}

// Latency is the per-turn timing report, in milliseconds. Unset stages are zero.
type Latency struct {
	ASRMs            int64 `json:"asr_ms"`
	LLMTTFTMs        int64 `json:"llm_ttft_ms"`
	FirstSegmentMs   int64 `json:"first_segment_ms"`
	FirstTTSMs       int64 `json:"first_tts_ms"`
	FirstAudioMs     int64 `json:"first_audio_ms"`
	ClientPlaybackMs int64 `json:"client_playback_ms"`
	TotalMs          int64 `json:"total_ms"`
	Filler           bool  `json:"filler"`
}
