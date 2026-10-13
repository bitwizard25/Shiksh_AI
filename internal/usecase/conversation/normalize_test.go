package conversation

import "testing"

func TestNormalizeForSpeech(t *testing.T) {
	cases := map[string]string{
		"**Great** job!":         "Great job!",
		"# Heading\nSome text":   "Heading Some text",
		"- first\n- second":      "first second",
		"1. one\n2. two":         "one two",
		"use `code` here":        "use code here",
		"well done 🎉👍":           "well done",
		"tab\tand\u0007bell":     "tab and bell",
		"  lots   of    space  ": "lots of space",
		"नमस्ते **दोस्त**":       "नमस्ते दोस्त",
		"क्‍या":                  "क्‍या", // zero-width joiner inside a word is kept
		"":                       "",
		"3. 5 is not a list":     "5 is not a list",
		"a 12.5 percent rise":    "a 12.5 percent rise",
	}
	for in, want := range cases {
		if got := NormalizeForSpeech(in); got != want {
			t.Errorf("NormalizeForSpeech(%q) = %q, want %q", in, got, want)
		}
	}
}
