package usecase_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

var update = flag.Bool("update", false, "rewrite golden files")

func promptInput(lang, subject string, grade *int, memory ...string) usecase.PromptInput {
	l, _ := entity.LookupLanguage(lang)
	s, _ := entity.LookupSubject(subject)
	return usecase.PromptInput{Language: l, Subject: s, Grade: grade, LearnerName: "Asha", Memory: memory}
}

func TestBuildSystemPromptGolden(t *testing.T) {
	g8 := 8
	cases := map[string]usecase.PromptInput{
		"hindi_math_grade8_memory": promptInput("hi", "math", &g8, "Covered fractions; confused numerator and denominator.", "Practiced adding halves."),
		"english_general_no_grade": promptInput("en", "general", nil),
		"tamil_science_grade8":     promptInput("ta", "science", &g8),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := usecase.BuildSystemPrompt(in)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -update to create it)", err)
			}
			if got+"\n" != strings.ReplaceAll(string(want), "\r\n", "\n") {
				t.Fatalf("prompt differs from %s; run go test -update if the change is intended.\n--- got ---\n%s", path, got)
			}
		})
	}
}

func TestBuildSystemPromptContent(t *testing.T) {
	g := 5
	got, _ := usecase.BuildSystemPrompt(promptInput("hi", "math", &g, "memo one"))
	for _, want := range []string{"हिन्दी", "Mathematics", "class 5", `"Asha"`, "- memo one"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	none, _ := usecase.BuildSystemPrompt(promptInput("hi", "math", nil))
	if strings.Contains(none, "remember") || strings.Contains(none, "class ") {
		t.Errorf("prompt without memory or grade mentions them:\n%s", none)
	}
}
