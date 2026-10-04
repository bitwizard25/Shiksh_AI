// Package clips serves the pre-recorded audio (fillers and system phrases) embedded in the binary,
// so playing one never depends on a speech provider being reachable.
package clips

import (
	"embed"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"path"
	"strings"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

//go:embed assets
var embedded embed.FS

var kinds = []conversation.ClipKind{conversation.ClipFiller, conversation.ClipRepeat, conversation.ClipError, conversation.ClipRedirect}

// Set is the decoded clip library. It is read-only after construction.
type Set struct {
	clips map[key][]conversation.Clip
}

type key struct {
	kind conversation.ClipKind
	lang string
}

var _ conversation.Clips = (*Set)(nil)

// Embedded loads the clips compiled into the binary.
func Embedded(log *slog.Logger) *Set {
	s, _ := New(embedded, "assets", log)
	return s
}

// New decodes every <root>/<lang>/<kind>[_N].wav it finds. Files that cannot be decoded are
// logged and skipped; the second result is how many clips were loaded.
func New(fsys fs.FS, root string, log *slog.Logger) (*Set, int) {
	if log == nil {
		log = slog.Default()
	}
	s := &Set{clips: map[key][]conversation.Clip{}}
	total := 0
	for _, lang := range entity.Languages() {
		dir := path.Join(root, lang.Code)
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			kind, ok := kindOf(e.Name())
			if e.IsDir() || !ok {
				continue
			}
			raw, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
			if err == nil {
				var pcm []byte
				var rate int
				if pcm, rate, err = audio.DecodeWAV(raw); err == nil {
					k := key{kind, lang.Code}
					s.clips[k] = append(s.clips[k], conversation.Clip{PCM: pcm, SampleRate: rate})
					total++
					continue
				}
			}
			log.Warn("skipping clip", "file", path.Join(dir, e.Name()), "err", err)
		}
	}
	return s, total
}

// kindOf maps "filler_2.wav" to the filler kind.
func kindOf(name string) (conversation.ClipKind, bool) {
	base, ok := strings.CutSuffix(name, ".wav")
	if !ok {
		return "", false
	}
	base, _, _ = strings.Cut(base, "_")
	for _, k := range kinds {
		if string(k) == base {
			return k, true
		}
	}
	return "", false
}

// Get returns a clip of the kind in the language, choosing among variants at random.
func (s *Set) Get(kind conversation.ClipKind, lang string) (conversation.Clip, bool) {
	list := s.clips[key{kind, lang}]
	if len(list) == 0 {
		return conversation.Clip{}, false
	}
	return list[rand.IntN(len(list))], true
}
