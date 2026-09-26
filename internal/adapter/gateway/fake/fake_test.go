package fake_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/fake"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

func TestASR(t *testing.T) {
	ctx := context.Background()
	got, err := fake.NewASR(fake.Options{ASRText: "नमस्ते"}).Transcribe(ctx, conversation.ASRRequest{Lang: "hi"})
	if err != nil || got.Text != "नमस्ते" {
		t.Fatalf("fixed text = %+v, %v", got, err)
	}
	got, err = fake.NewASR(fake.Options{}).Transcribe(ctx, conversation.ASRRequest{PCM: make([]byte, 32000), SampleRate: 16000})
	if err != nil || got.Text != "fake transcript (1000 ms)" {
		t.Fatalf("derived text = %+v, %v", got, err)
	}
	boom := errors.New("boom")
	if _, err := fake.NewASR(fake.Options{ASRErr: boom}).Transcribe(ctx, conversation.ASRRequest{}); !errors.Is(err, boom) {
		t.Fatalf("injected error = %v", err)
	}
}

func TestDelaysHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := fake.NewTTS(fake.Options{TTSDelay: 5 * time.Second}).Synthesize(ctx, conversation.TTSRequest{Text: "hi"})
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v, want prompt context.Canceled", err, time.Since(start))
	}
}

func TestLLMStreamsTheCannedReply(t *testing.T) {
	req := conversation.LLMRequest{Messages: []conversation.ChatMessage{
		{Role: conversation.RoleUser, Text: "old question"},
		{Role: conversation.RoleModel, Text: "old answer"},
		{Role: conversation.RoleUser, Text: "भिन्न क्या है?"},
	}}
	var text strings.Builder
	var last conversation.Delta
	for d, err := range fake.NewLLM(fake.Options{}).Stream(context.Background(), req) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		text.WriteString(d.Text)
		last = d
	}
	if text.String() != fake.Reply("भिन्न क्या है?") || last.FinishReason != "STOP" {
		t.Fatalf("text=%q last=%+v", text.String(), last)
	}

	n := 0
	for range fake.NewLLM(fake.Options{}).Stream(context.Background(), req) {
		n++
		break // consumer stops early; the stream must end without panicking
	}
	if n != 1 {
		t.Fatalf("consumed %d deltas after break", n)
	}

	boom := errors.New("boom")
	for _, err := range fake.NewLLM(fake.Options{LLMErr: boom}).Stream(context.Background(), req) {
		if !errors.Is(err, boom) {
			t.Fatalf("injected error = %v", err)
		}
	}
	if s, err := fake.NewLLM(fake.Options{}).Complete(context.Background(), req); err != nil || !strings.Contains(s, "भिन्न क्या है?") {
		t.Fatalf("Complete = %q, %v", s, err)
	}
}

func TestTTSDurationFollowsText(t *testing.T) {
	res, err := fake.NewTTS(fake.Options{}).Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "नमस्ते"})
	if err != nil {
		t.Fatal(err)
	}
	wantMs := 60 * 6 // 6 runes × 60 ms
	if res.SampleRate != fake.SampleRate || res.DurationMs < wantMs-1 || res.DurationMs > wantMs {
		t.Fatalf("res = rate %d, %d ms; want %d Hz, ~%d ms", res.SampleRate, res.DurationMs, fake.SampleRate, wantMs)
	}
	if len(res.PCM) != 2*(fake.SampleRate*wantMs/1000) {
		t.Fatalf("PCM bytes = %d", len(res.PCM))
	}
}

func TestAvailability(t *testing.T) {
	a := fake.NewAvailability([]string{"hi", "en"})
	if !a.Available("hi") || a.Available("ta") {
		t.Fatal("availability does not follow the enabled list")
	}
}
