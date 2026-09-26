package usecase_test

import (
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

type availability map[string]bool

func (a availability) Available(code string) bool { return a[code] }

func TestCatalogLanguages(t *testing.T) {
	got := usecase.NewCatalog([]string{"en", "hi", "xx"}, availability{"hi": true}).Languages()
	if len(got) != 2 || got[0].Code != "hi" || !got[0].Available || got[1].Code != "en" || got[1].Available {
		t.Fatalf("Languages = %+v; want hi (available) then en (not), registry order, unknown code ignored", got)
	}

	all := usecase.NewCatalog(nil, nil).Languages()
	if len(all) != 9 || all[0].Code != "hi" || all[0].NativeName != "हिन्दी" || !all[0].Available {
		t.Fatalf("default catalog = %+v", all)
	}

	enabled := []string{"hi"}
	c := usecase.NewCatalog(enabled, nil)
	enabled[0] = "en"
	if got := c.Languages(); len(got) != 1 || got[0].Code != "hi" {
		t.Fatalf("catalog aliased the caller's slice: %+v", got)
	}

	if got := usecase.NewCatalog([]string{"xx"}, nil).Languages(); got == nil || len(got) != 0 {
		t.Fatalf("no matching languages = %#v, want an empty non-nil slice (JSON [])", got)
	}
}
