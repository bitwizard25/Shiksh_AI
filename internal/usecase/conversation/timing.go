package conversation

import (
	"sync"
	"time"
)

// timing records when each stage of a turn happened. Only the first call to a mark counts.
type timing struct {
	mu    sync.Mutex
	start time.Time
	marks map[string]time.Time
	end   time.Time
	// playback is reported by the client after the turn, hence stored separately.
	playback time.Duration
	filler   bool
}

const (
	markASR           = "asr"
	markLLMFirstToken = "llm_first_token"
	markFirstSegment  = "first_segment"
	markFirstTTS      = "first_tts"
	markFirstAudio    = "first_audio"
)

func newTiming(start time.Time) *timing { return &timing{start: start, marks: map[string]time.Time{}} }

func (t *timing) mark(name string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.marks[name]; !ok {
		t.marks[name] = now
	}
}

func (t *timing) finish(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.end.IsZero() {
		t.end = now
	}
}

func (t *timing) setFiller() {
	t.mu.Lock()
	t.filler = true
	t.mu.Unlock()
}

func (t *timing) setPlayback(d time.Duration) {
	t.mu.Lock()
	t.playback = d
	t.mu.Unlock()
}

// since returns the mark's offset from the start of the turn, and whether it was set.
func (t *timing) since(name string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.marks[name]
	return m.Sub(t.start), ok
}

// latency builds the report. Stages are measured from the start of the turn (speech.end), so each
// number is "time until this stage finished", except asr_ms which is the same by construction.
func (t *timing) latency() Latency {
	t.mu.Lock()
	defer t.mu.Unlock()
	ms := func(name string) int64 {
		if m, ok := t.marks[name]; ok {
			return m.Sub(t.start).Milliseconds()
		}
		return 0
	}
	l := Latency{
		ASRMs: ms(markASR), LLMTTFTMs: ms(markLLMFirstToken), FirstSegmentMs: ms(markFirstSegment),
		FirstTTSMs: ms(markFirstTTS), FirstAudioMs: ms(markFirstAudio),
		ClientPlaybackMs: t.playback.Milliseconds(), Filler: t.filler,
	}
	if !t.end.IsZero() {
		l.TotalMs = t.end.Sub(t.start).Milliseconds()
	}
	return l
}
