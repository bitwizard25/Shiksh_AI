package conversation

import (
	"strings"
	"unicode"
)

// NormalizeForSpeech makes a segment safe to send to speech synthesis: it strips markdown marks,
// list prefixes, emoji and control characters, and collapses whitespace. The prompt already asks
// for plain text, so this only catches slips.
func NormalizeForSpeech(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = stripListPrefix(strings.TrimSpace(line))
		var b strings.Builder
		for _, r := range line {
			switch {
			case r == '*' || r == '_' || r == '#' || r == '`':
			case isEmoji(r):
			case unicode.IsControl(r):
				b.WriteRune(' ')
			default:
				b.WriteRune(r)
			}
		}
		if t := strings.Join(strings.Fields(b.String()), " "); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}

// stripListPrefix removes a leading "- ", "• " or "12. " bullet.
func stripListPrefix(line string) string {
	for _, p := range []string{"- ", "• ", "* "} {
		if rest, ok := strings.CutPrefix(line, p); ok {
			return rest
		}
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i <= 2 && strings.HasPrefix(line[i:], ". ") {
		return line[i+2:]
	}
	return line
}

func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF, // pictographs, emoticons, transport, supplemental symbols
		r >= 0x2600 && r <= 0x27BF,   // misc symbols and dingbats
		r >= 0x1F1E6 && r <= 0x1F1FF, // regional indicators
		r == 0xFE0F, r == 0x20E3:     // variation selector-16, keycap
		return true
	}
	return false
}
