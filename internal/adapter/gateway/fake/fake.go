// Package fake provides deterministic speech and language providers for local development
// (PROVIDERS=fake) and tests. They need no network or API keys.
package fake

import (
	"context"
	"encoding/binary"
	"fmt"
	"iter"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// SampleRate is the rate of the fake TTS audio (Bhashini's documented TTS rate).
const SampleRate = 22050

// Options control the fakes' latency and failures.
type Options struct {
	ASRDelay      time.Duration
	ASRText       string // fixed transcript; empty means "fake transcript (<ms> ms)"
	ASRErr        error
	LLMFirstToken time.Duration
	LLMPerToken   time.Duration
	LLMErr        error
	TTSDelay      time.Duration
	TTSErr        error
}

// Defaults returns latencies close to real providers, for realistic local runs.
func Defaults() Options {
	return Options{ASRDelay: 300 * time.Millisecond, LLMFirstToken: 300 * time.Millisecond, LLMPerToken: 20 * time.Millisecond, TTSDelay: 250 * time.Millisecond}
}

// Reply is the canned bilingual answer the fake tutor gives to question.
func Reply(question string) string {
	return "अच्छा सवाल है। You asked: " + question + ". पहले बताइए, आप इसके बारे में क्या जानते हैं?"
}

// ASR is a fake speech-to-text provider.
type ASR struct{ opts Options }

var _ conversation.ASR = (*ASR)(nil)

func NewASR(o Options) *ASR { return &ASR{opts: o} }

func (a *ASR) Transcribe(ctx context.Context, req conversation.ASRRequest) (conversation.ASRResult, error) {
	if err := wait(ctx, a.opts.ASRDelay); err != nil {
		return conversation.ASRResult{}, err
	}
	if a.opts.ASRErr != nil {
		return conversation.ASRResult{}, a.opts.ASRErr
	}
	if a.opts.ASRText != "" {
		return conversation.ASRResult{Text: a.opts.ASRText}, nil
	}
	return conversation.ASRResult{Text: fmt.Sprintf("fake transcript (%d ms)", audio.DurationMs(len(req.PCM), req.SampleRate, 1))}, nil
}

// LLM is a fake language model that streams Reply word by word.
type LLM struct{ opts Options }

var _ conversation.LLM = (*LLM)(nil)

func NewLLM(o Options) *LLM { return &LLM{opts: o} }

func (l *LLM) Stream(ctx context.Context, req conversation.LLMRequest) iter.Seq2[conversation.Delta, error] {
	return func(yield func(conversation.Delta, error) bool) {
		if err := wait(ctx, l.opts.LLMFirstToken); err != nil {
			yield(conversation.Delta{}, err)
			return
		}
		if l.opts.LLMErr != nil {
			yield(conversation.Delta{}, l.opts.LLMErr)
			return
		}
		words := strings.Fields(Reply(lastUserText(req)))
		for i, w := range words {
			if i > 0 {
				if err := wait(ctx, l.opts.LLMPerToken); err != nil {
					yield(conversation.Delta{}, err)
					return
				}
			}
			d := conversation.Delta{Text: w + " "}
			if i == len(words)-1 {
				d = conversation.Delta{Text: w, FinishReason: "STOP"}
			}
			if !yield(d, nil) {
				return
			}
		}
	}
}

func (l *LLM) Complete(ctx context.Context, req conversation.LLMRequest) (string, error) {
	if err := wait(ctx, l.opts.LLMFirstToken); err != nil {
		return "", err
	}
	if l.opts.LLMErr != nil {
		return "", l.opts.LLMErr
	}
	return "Summary: " + lastUserText(req), nil
}

// TTS is a fake text-to-speech provider producing a quiet 440 Hz tone, 60 ms per character.
type TTS struct{ opts Options }

var _ conversation.TTS = (*TTS)(nil)

func NewTTS(o Options) *TTS { return &TTS{opts: o} }

func (t *TTS) Synthesize(ctx context.Context, req conversation.TTSRequest) (conversation.TTSResult, error) {
	if err := wait(ctx, t.opts.TTSDelay); err != nil {
		return conversation.TTSResult{}, err
	}
	if t.opts.TTSErr != nil {
		return conversation.TTSResult{}, t.opts.TTSErr
	}
	ms := min(60*utf8.RuneCountInString(req.Text), 10_000)
	pcm := tone(SampleRate, ms)
	return conversation.TTSResult{PCM: pcm, SampleRate: SampleRate, DurationMs: audio.DurationMs(len(pcm), SampleRate, 1)}, nil
}

// Availability reports every enabled language as available.
type Availability struct{ enabled map[string]bool }

func NewAvailability(codes []string) *Availability {
	a := &Availability{enabled: make(map[string]bool, len(codes))}
	for _, c := range codes {
		a.enabled[c] = true
	}
	return a
}

func (a *Availability) Available(code string) bool { return a.enabled[code] }

func lastUserText(req conversation.LLMRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == conversation.RoleUser {
			return req.Messages[i].Text
		}
	}
	return ""
}

func tone(rate, ms int) []byte {
	n := rate * ms / 1000
	buf := make([]byte, 2*n)
	for i := range n {
		v := int16(3000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(v))
	}
	return buf
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
