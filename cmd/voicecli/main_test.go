package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/fake"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
)

func fakeEnv() map[string]string {
	return map[string]string{"PROVIDERS": "fake", "ENABLED_LANGUAGES": "hi,en"}
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	var out strings.Builder
	if err := run(context.Background(), args, fakeEnv(), &out); err != nil {
		t.Fatalf("voicecli %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTTSThenASR(t *testing.T) {
	dir := t.TempDir()
	native := filepath.Join(dir, "hello.wav")
	runCLI(t, "tts", "--lang", "hi", "--text", "नमस्ते", "--out", native)
	data, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	if _, f, err := audio.DecodeWAV(data); err != nil || f.SampleRate != fake.SampleRate {
		t.Fatalf("tts output = %+v, %v", f, err)
	}

	q := filepath.Join(dir, "q16k.wav")
	runCLI(t, "tts", "--lang", "hi", "--text", "भिन्न क्या है", "--out", q, "--rate", "16000")
	out := runCLI(t, "asr", "--lang", "hi", "--wav", q)
	if !strings.Contains(out, "fake transcript") || !strings.Contains(out, "asr_ms:") {
		t.Fatalf("asr output = %q", out)
	}
}

func TestASRExplainsWrongFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cd.wav")
	writeFile(t, path, audio.EncodeWAV(make([]byte, 4410*2), 44100, 1))
	err := run(context.Background(), []string{"asr", "--lang", "hi", "--wav", path}, fakeEnv(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("err = %v, want a conversion hint", err)
	}
}

func TestLLMStreamsReply(t *testing.T) {
	out := runCLI(t, "llm", "--lang", "hi", "--text", "भिन्न क्या है?")
	if !strings.Contains(out, fake.Reply("भिन्न क्या है?")) || !strings.Contains(out, "llm_ttft_ms:") {
		t.Fatalf("llm output = %q", out)
	}
}

func TestAssetsWritesEveryClip(t *testing.T) {
	dir := t.TempDir()
	runCLI(t, "assets", "--langs", "hi", "--out", dir)
	hi, _ := entity.LookupLanguage("hi")
	files, err := filepath.Glob(filepath.Join(dir, "hi", "*.wav"))
	if err != nil || len(files) != len(hi.Phrases.Fillers)+3 {
		t.Fatalf("clips = %v, %v", files, err)
	}
	for _, name := range []string{"filler_1.wav", "repeat.wav", "error.wav", "redirect.wav"} {
		data, err := os.ReadFile(filepath.Join(dir, "hi", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := audio.DecodeWAV(data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLatencyReportsPercentiles(t *testing.T) {
	q := filepath.Join(t.TempDir(), "q.wav")
	writeFile(t, q, audio.EncodeWAV(make([]byte, 16000*2), 16000, 1))
	out := runCLI(t, "latency", "--lang", "hi", "--wav", q, "--runs", "1")
	for _, want := range []string{"asr_ms", "llm_ttft_ms", "first_clause_ms", "tts_first_ms", "server_total_ms", "SLO"} {
		if !strings.Contains(out, want) {
			t.Errorf("latency output lacks %q:\n%s", want, out)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	ctx := context.Background()
	for name, args := range map[string][]string{
		"no command":       nil,
		"unknown command":  {"dance"},
		"missing flags":    {"tts", "--lang", "hi"},
		"unknown language": {"llm", "--lang", "xx", "--text", "q"},
		"bad flag":         {"asr", "--nope"},
	} {
		if err := run(ctx, args, fakeEnv(), io.Discard); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	var out strings.Builder
	if err := run(ctx, []string{"help"}, fakeEnv(), &out); err != nil || !strings.Contains(out.String(), "voicecli latency") {
		t.Fatalf("help = %q, %v", out.String(), err)
	}
}

func TestPercentile(t *testing.T) {
	var twenty []int
	for i := 20; i >= 1; i-- {
		twenty = append(twenty, i)
	}
	cases := []struct {
		in      []int
		p, want int
	}{{twenty, 50, 10}, {twenty, 95, 19}, {twenty, 100, 20}, {[]int{5}, 95, 5}, {[]int{3, 1, 2}, 50, 2}, {nil, 50, 0}}
	for _, tc := range cases {
		if got := percentile(tc.in, tc.p); got != tc.want {
			t.Errorf("percentile(%v, %d) = %d, want %d", tc.in, tc.p, got, tc.want)
		}
	}
}

func TestFirstClause(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"नमस्ते। आज हम", "नमस्ते।", true},
		{"नमस्ते।", "", false}, // the next rune decides (it could be a decimal point elsewhere)
		{"यह 3.5 है", "", false},
		{"Hello, world", "", false}, // soft stop before 20 runes
		{"This is a longer opening clause, then more", "This is a longer opening clause,", true},
		{"Line one\nLine two", "Line one", true},
		{"Why? Because", "Why?", true},
	}
	for _, tc := range cases {
		got, ok := firstClause(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("firstClause(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
