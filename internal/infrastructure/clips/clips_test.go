package clips

import (
	"io"
	"log/slog"
	"testing"
	"testing/fstest"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

func wav(rate int, n int) []byte { return audio.EncodeWAV(make([]byte, n*2), rate, 1) }

func TestNewLoadsVariantsAndSkipsBadFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"assets/hi/filler_1.wav": {Data: wav(22050, 100)},
		"assets/hi/filler_2.wav": {Data: wav(22050, 200)},
		"assets/hi/repeat.wav":   {Data: wav(16000, 50)},
		"assets/hi/error.wav":    {Data: []byte("not a wav")},
		"assets/hi/notes.txt":    {Data: []byte("ignored")},
		"assets/hi/redirect.wav": {Data: audio.EncodeWAV(make([]byte, 40), 16000, 2)}, // stereo: not playable as a clip
		"assets/hi/unknown.wav":  {Data: wav(16000, 5)},
		"assets/en/redirect.wav": {Data: wav(22050, 10)},
		"assets/xx/filler.wav":   {Data: wav(22050, 10)}, // not a supported language
	}
	s, n := New(fsys, "assets", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if n != 4 {
		t.Fatalf("loaded %d clips, want 4", n)
	}
	seen := map[int]bool{}
	for range 100 {
		c, ok := s.Get(conversation.ClipFiller, "hi")
		if !ok || c.SampleRate != 22050 {
			t.Fatalf("filler = %+v, %v", c, ok)
		}
		seen[len(c.PCM)] = true
	}
	if !seen[200] || !seen[400] {
		t.Fatalf("variants seen = %v, want both", seen)
	}
	if c, ok := s.Get(conversation.ClipRepeat, "hi"); !ok || c.SampleRate != 16000 || len(c.PCM) != 100 {
		t.Fatalf("repeat = %+v, %v", c, ok)
	}
	if _, ok := s.Get(conversation.ClipError, "hi"); ok {
		t.Fatal("a corrupt clip was loaded")
	}
	if _, ok := s.Get(conversation.ClipRedirect, "hi"); ok {
		t.Fatal("a stereo clip was loaded")
	}
	if _, ok := s.Get(conversation.ClipFiller, "ta"); ok {
		t.Fatal("clip for a language with no files")
	}
}

func TestEmbeddedWithNoAssetsIsEmptyButSafe(t *testing.T) {
	s := Embedded(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, ok := s.Get(conversation.ClipFiller, "hi"); ok {
		t.Fatal("unexpected embedded clip")
	}
}
