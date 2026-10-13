package conversation

import (
	"slices"
	"strings"
	"testing"
)

// segmentsOf feeds text in chunks of chunk runes and returns every segment, including the flushed rest.
func segmentsOf(text string, chunk int) []string {
	s := NewSegmenter(20)
	var out []string
	r := []rune(text)
	for len(r) > 0 {
		n := min(chunk, len(r))
		out = append(out, s.Push(string(r[:n]))...)
		r = r[n:]
	}
	if rest := s.Flush(); rest != "" {
		out = append(out, rest)
	}
	return out
}

func TestSegmenter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"english sentences", "Fractions are parts of a whole. Do you know what a numerator is? Good!", []string{"Fractions are parts of a whole.", "Do you know what a numerator is?", "Good!"}},
		{"hindi danda", "भिन्न किसी पूरी चीज़ का हिस्सा होती है। क्या तुम अंश जानते हो? बहुत अच्छा।", []string{"भिन्न किसी पूरी चीज़ का हिस्सा होती है।", "क्या तुम अंश जानते हो?", "बहुत अच्छा।"}},
		{"tamil", "பின்னம் என்பது ஒரு பகுதி. உனக்குத் தெரியுமா?", []string{"பின்னம் என்பது ஒரு பகுதி.", "உனக்குத் தெரியுமா?"}},
		{"decimal point is not a boundary", "Pi is about 3.14 and that is a useful number to remember. Next.", []string{"Pi is about 3.14 and that is a useful number to remember.", "Next."}},
		{"abbreviation is not a boundary", "Ask Dr. Rao about this topic today. Then rest.", []string{"Ask Dr. Rao about this topic today.", "Then rest."}},
		{"e.g. is not a boundary", "Try a shape, e.g. a square or a circle for now. Done.", []string{"Try a shape, e.g. a square or a circle for now.", "Done."}},
		{"newline is a boundary", "First line here\nSecond line here", []string{"First line here", "Second line here"}},
		{"first segment splits at a comma once long enough", "Let us start with something simple, then we move on to harder ideas.", []string{"Let us start with something simple,", "then we move on to harder ideas."}},
		{"short first clause does not split", "Yes, that is right and you did well today.", []string{"Yes, that is right and you did well today."}},
		{"later segments ignore short commas", "Okay. Think about a pizza, cut into eight equal slices, and eat three of them now.", []string{"Okay.", "Think about a pizza, cut into eight equal slices, and eat three of them now."}},
		{"tiny later segments merge forward", "First sentence is long enough. Ok. Then a longer sentence follows here.", []string{"First sentence is long enough.", "Ok. Then a longer sentence follows here."}},
		{"remainder without punctuation is flushed", "Tell me what you think", []string{"Tell me what you think"}},
		{"empty", "", nil},
		{"whitespace only", "   \n  ", nil},
	}
	for _, tc := range cases {
		for _, chunk := range []int{1, 3, 1000} { // the result must not depend on how the stream is chunked
			if got := segmentsOf(tc.in, chunk); !slices.Equal(got, tc.want) {
				t.Errorf("%s (chunk %d):\n got %q\nwant %q", tc.name, chunk, got, tc.want)
			}
		}
	}
}

func TestSegmenterForcesSplitOfRunOnText(t *testing.T) {
	long := strings.Repeat("word ", 80) // 400 runes, no punctuation
	got := segmentsOf(long, 1000)
	if len(got) < 2 {
		t.Fatalf("got %d segments, want a forced split", len(got))
	}
	for _, seg := range got[:len(got)-1] {
		if n := len([]rune(seg)); n > 300 {
			t.Errorf("segment of %d runes is too long", n)
		}
	}
	if strings.Join(got, " ") != strings.TrimSpace(long) {
		t.Error("text was lost or changed by the split")
	}
}

func TestSegmenterHoldsBoundaryUntilNextRuneIsKnown(t *testing.T) {
	s := NewSegmenter(20)
	if got := s.Push("This sentence is finished."); len(got) != 0 {
		t.Fatalf("emitted %q before knowing what follows the full stop", got)
	}
	if got := s.Push(" And"); !slices.Equal(got, []string{"This sentence is finished."}) {
		t.Fatalf("got %q", got)
	}
	if got := s.Flush(); got != "And" {
		t.Fatalf("Flush = %q", got)
	}
}

func TestSegmenterFlushAtEndOfInputEmitsFinalSentence(t *testing.T) {
	s := NewSegmenter(20)
	s.Push("The end is here.")
	if got := s.Flush(); got != "The end is here." {
		t.Fatalf("Flush = %q", got)
	}
}
