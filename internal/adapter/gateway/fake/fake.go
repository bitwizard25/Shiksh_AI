// Package fake provides providers that need no network or API keys: speech recognition that returns
// fixed text, a language model that echoes, and speech synthesis that plays a tone. It backs
// PROVIDERS=fake for local development and the end-to-end tests.
package fake

import (
	"context"
	"fmt"
	"iter"
	"math"
	"strings"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// SampleRate is the rate of every tone the fakes produce.
const SampleRate = 22050

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
		return context.Cause(ctx)
	}
}

// ASR returns Text for any audio of at least 100 ms, and "" for shorter audio.
type ASR struct {
	Text  string
	Delay time.Duration
	Err   error
}

var _ conversation.ASR = (*ASR)(nil)

func (a *ASR) Transcribe(ctx context.Context, r conversation.ASRRequest) (string, error) {
	if err := wait(ctx, a.Delay); err != nil {
		return "", err
	}
	if a.Err != nil {
		return "", a.Err
	}
	if len(r.PCM) < r.SampleRate*2/10 {
		return "", nil
	}
	if a.Text != "" {
		return a.Text, nil
	}
	return "can you explain this to me", nil
}

// LLM streams an echoing reply of three sentences, word by word.
type LLM struct {
	TTFT       time.Duration // before the first token
	TokenDelay time.Duration // between tokens
}

var _ conversation.LLM = (*LLM)(nil)

func (l *LLM) Stream(ctx context.Context, r conversation.LLMRequest) iter.Seq2[conversation.Delta, error] {
	said := ""
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == usecase.ChatUser {
			said = r.Messages[i].Text
			break
		}
	}
	reply := fmt.Sprintf("You asked: %s. That is a good question, so let us think about it together. What do you already know about it?", strings.TrimRight(said, ".?! "))
	return func(yield func(conversation.Delta, error) bool) {
		if err := wait(ctx, l.TTFT); err != nil {
			yield(conversation.Delta{}, err)
			return
		}
		words := strings.SplitAfter(reply, " ")
		for i, w := range words {
			if i > 0 {
				if err := wait(ctx, l.TokenDelay); err != nil {
					yield(conversation.Delta{}, err)
					return
				}
			}
			if !yield(conversation.Delta{Text: w}, nil) {
				return
			}
		}
	}
}

// TTS synthesizes a sine tone as long as the text would take to say.
type TTS struct {
	Delay time.Duration
	Err   error
}

var _ conversation.TTS = (*TTS)(nil)

func (t *TTS) Synthesize(ctx context.Context, r conversation.TTSRequest) (conversation.TTSResult, error) {
	if err := wait(ctx, t.Delay); err != nil {
		return conversation.TTSResult{}, err
	}
	if t.Err != nil {
		return conversation.TTSResult{}, t.Err
	}
	d := min(max(time.Duration(len([]rune(r.Text)))*60*time.Millisecond, 300*time.Millisecond), 8*time.Second)
	return conversation.TTSResult{PCM: Tone(440, d), SampleRate: SampleRate}, nil
}

// Tone returns a sine wave of freq Hz lasting d, as signed 16-bit little-endian mono PCM at SampleRate.
func Tone(freq float64, d time.Duration) []byte {
	n := int(float64(SampleRate) * d.Seconds())
	pcm := make([]byte, n*2)
	for i := range n {
		v := int16(0.2 * math.MaxInt16 * math.Sin(2*math.Pi*freq*float64(i)/SampleRate))
		pcm[2*i], pcm[2*i+1] = byte(v), byte(v>>8)
	}
	return pcm
}

// Clips plays a distinct short tone for every clip kind in every language.
type Clips struct{}

var _ conversation.Clips = Clips{}

func (Clips) Get(kind conversation.ClipKind, _ string) (conversation.Clip, bool) {
	tones := map[conversation.ClipKind]struct {
		freq float64
		d    time.Duration
	}{
		conversation.ClipFiller:   {330, 300 * time.Millisecond},
		conversation.ClipRepeat:   {523, 500 * time.Millisecond},
		conversation.ClipError:    {196, 500 * time.Millisecond},
		conversation.ClipRedirect: {392, 500 * time.Millisecond},
	}
	t, ok := tones[kind]
	if !ok {
		return conversation.Clip{}, false
	}
	return conversation.Clip{PCM: Tone(t.freq, t.d), SampleRate: SampleRate}, true
}
