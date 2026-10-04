package conversation

import (
	"strings"
	"unicode"
)

const (
	laterSoftMinRunes = 120 // later segments split at a soft boundary only past this length
	forceSplitRunes   = 250 // a segment this long is cut at the last whitespace
	minLaterRunes     = 8   // shorter later segments merge into the next one
	minFirstRunes     = 2
)

var abbreviations = map[string]bool{"dr": true, "mr": true, "mrs": true, "ms": true, "e.g": true, "i.e": true, "etc": true, "vs": true}

// Segmenter cuts a streamed reply into speakable segments. It works on runes, not bytes, and
// understands Indic sentence ends. The first segment is cut early (at a clause) so speech starts fast.
type Segmenter struct {
	buf      []rune
	firstMin int
	emitted  int
}

// NewSegmenter returns a segmenter whose first segment may end at a comma once it has firstMinRunes runes.
func NewSegmenter(firstMinRunes int) *Segmenter { return &Segmenter{firstMin: firstMinRunes} }

// Push adds streamed text and returns the segments that are now complete.
func (s *Segmenter) Push(delta string) []string {
	s.buf = append(s.buf, []rune(delta)...)
	var out []string
	for {
		seg, ok := s.next()
		if !ok {
			return out
		}
		out = append(out, seg)
	}
}

// Flush returns whatever is left, trimmed, or "" if nothing is.
func (s *Segmenter) Flush() string {
	rest := strings.TrimSpace(string(s.buf))
	s.buf = nil
	if rest != "" {
		s.emitted++
	}
	return rest
}

func (s *Segmenter) next() (string, bool) {
	first := s.emitted == 0
	for i := 0; i < len(s.buf); i++ {
		r := s.buf[i]
		cut := -1
		switch {
		case r == '\n':
			cut = i
		case isHard(r):
			if i+1 >= len(s.buf) {
				return s.forced() // the next rune decides whether this is a boundary
			}
			if unicode.IsSpace(s.buf[i+1]) && !(r == '.' && s.afterAbbreviation(i)) {
				cut = i + 1
			}
		case isSoft(r):
			if i+1 >= len(s.buf) {
				return s.forced()
			}
			if unicode.IsSpace(s.buf[i+1]) {
				need := laterSoftMinRunes
				if first {
					need = s.firstMin
				}
				if runeLen(s.buf[:i+1]) >= need {
					cut = i + 1
				}
			}
		}
		if cut < 0 {
			continue
		}
		seg := strings.TrimSpace(string(s.buf[:cut]))
		min := minLaterRunes
		if first {
			min = minFirstRunes
		}
		if seg == "" || (isHard(r) || r == '\n') && len([]rune(seg)) < min {
			continue // too short to speak alone: keep it and merge forward
		}
		s.buf = trimLeft(s.buf[cut:])
		s.emitted++
		return seg, true
	}
	return s.forced()
}

// forced cuts a run of text longer than forceSplitRunes at the last whitespace within that limit.
func (s *Segmenter) forced() (string, bool) {
	if len(s.buf) <= forceSplitRunes {
		return "", false
	}
	at := -1
	for i := forceSplitRunes; i > 0; i-- { // the last whitespace within the limit
		if unicode.IsSpace(s.buf[i]) {
			at = i
			break
		}
	}
	if at <= 0 {
		return "", false
	}
	seg := strings.TrimSpace(string(s.buf[:at]))
	s.buf = trimLeft(s.buf[at:])
	s.emitted++
	return seg, true
}

// afterAbbreviation reports whether the word ending at the '.' at index i is a known abbreviation.
func (s *Segmenter) afterAbbreviation(i int) bool {
	j := i
	for j > 0 && !unicode.IsSpace(s.buf[j-1]) {
		j--
	}
	return abbreviations[strings.ToLower(string(s.buf[j:i]))]
}

func isHard(r rune) bool { return r == '.' || r == '?' || r == '!' || r == '।' || r == '॥' }
func isSoft(r rune) bool { return r == ',' || r == ';' || r == ':' || r == '—' }

func runeLen(r []rune) int { return len([]rune(strings.TrimSpace(string(r)))) }

func trimLeft(r []rune) []rune {
	i := 0
	for i < len(r) && unicode.IsSpace(r[i]) {
		i++
	}
	return r[i:]
}
