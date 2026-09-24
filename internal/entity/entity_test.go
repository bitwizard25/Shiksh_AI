package entity_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

func wantField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *entity.ValidationError
	if !errors.As(err, &ve) || ve.Field != field {
		t.Fatalf("err = %v, want validation error on %q", err, field)
	}
}

func TestParseEmail(t *testing.T) {
	got, err := entity.ParseEmail("  Asha@Example.COM ")
	if err != nil || got != "asha@example.com" {
		t.Fatalf("ParseEmail = %q, %v; want normalized asha@example.com", got, err)
	}
	for _, bad := range []string{
		"not-an-email",
		"Asha <asha@example.com>",
		"asha@localhost",
		strings.Repeat("a", 250) + "@example.com",
		"",
	} {
		_, err := entity.ParseEmail(bad)
		wantField(t, err, "email")
	}
}

func TestValidatePassword(t *testing.T) {
	for _, ok := range []string{"correct horse", "पासवर्ड१२" /* 9 runes, 27 bytes */, strings.Repeat("p", 128)} {
		if err := entity.ValidatePassword("password", ok); err != nil {
			t.Errorf("ValidatePassword(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"short", "पासवर्ड" /* 7 runes, 21 bytes */, strings.Repeat("p", 129)} {
		wantField(t, entity.ValidatePassword("new_password", bad), "new_password")
	}
}

func TestParseDisplayName(t *testing.T) {
	if got, err := entity.ParseDisplayName("  आशा "); err != nil || got != "आशा" {
		t.Fatalf("ParseDisplayName = %q, %v", got, err)
	}
	_, err := entity.ParseDisplayName("   ")
	wantField(t, err, "display_name")
	_, err = entity.ParseDisplayName(strings.Repeat("n", 81))
	wantField(t, err, "display_name")
}

func TestParseDisplayNameRejectsControlAndBidiCharacters(t *testing.T) {
	rlo := string(rune(0x202E)) // RIGHT-TO-LEFT OVERRIDE
	nul := string(rune(0x0000)) // NUL, a C0 control
	for _, bad := range []string{
		"Asha" + rlo + "evil",
		"line\nbreak", // C0 control (newline)
		"a" + nul + "b",
	} {
		_, err := entity.ParseDisplayName(bad)
		wantField(t, err, "display_name")
	}
	// ZWJ (U+200D) is required to render conjunct consonants in Indic scripts and must stay allowed.
	zwj := string(rune(0x200D))
	name := "क्" + zwj + "ष"
	if got, err := entity.ParseDisplayName(name); err != nil || got != name {
		t.Fatalf("ParseDisplayName(ZWJ) = %q, %v; want it accepted unchanged", got, err)
	}
}

func TestValidateGrade(t *testing.T) {
	for _, g := range []int{1, 12} {
		if err := entity.ValidateGrade(&g); err != nil {
			t.Errorf("grade %d rejected: %v", g, err)
		}
	}
	if err := entity.ValidateGrade(nil); err != nil {
		t.Errorf("nil grade rejected: %v", err)
	}
	for _, g := range []int{0, 13} {
		wantField(t, entity.ValidateGrade(&g), "grade")
	}
}

func TestLanguages(t *testing.T) {
	hi, ok := entity.LookupLanguage("hi")
	if !ok || hi.Name != "Hindi" || hi.NativeName != "हिन्दी" {
		t.Fatalf("LookupLanguage(hi) = %+v, %v", hi, ok)
	}
	if _, ok := entity.LookupLanguage("xx"); ok {
		t.Fatal("LookupLanguage(xx) found a language")
	}
	wantField(t, entity.ValidateLanguage("xx"), "preferred_lang")
	if err := entity.ValidateLanguage(entity.DefaultLanguage); err != nil {
		t.Fatalf("default language invalid: %v", err)
	}
	langs := entity.Languages()
	if len(langs) != 9 || langs[0].Code != "hi" || langs[8].Code != "en" {
		t.Fatalf("Languages() = %+v", langs)
	}
	langs[0].Code = "zz"
	if entity.Languages()[0].Code != "hi" {
		t.Fatal("modifying Languages() result changed the registry")
	}
}

func TestRefreshTokenIsReplay(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	used := func(ago time.Duration) *time.Time { t := now.Add(-ago); return &t }
	grace := 20 * time.Second
	cases := []struct {
		name  string
		token entity.RefreshToken
		want  bool
	}{
		{"never used", entity.RefreshToken{}, false},
		{"used inside grace", entity.RefreshToken{UsedAt: used(5 * time.Second)}, false},
		{"used exactly at grace", entity.RefreshToken{UsedAt: used(grace)}, false},
		{"used after grace", entity.RefreshToken{UsedAt: used(21 * time.Second)}, true},
	}
	for _, tc := range cases {
		if got := tc.token.IsReplay(now, grace); got != tc.want {
			t.Errorf("%s: IsReplay = %v, want %v", tc.name, got, tc.want)
		}
	}
}
