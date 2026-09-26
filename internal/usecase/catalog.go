package usecase

import (
	"slices"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// LanguageAvailability reports whether the speech providers can currently serve a language.
type LanguageAvailability interface {
	Available(code string) bool
}

// LanguageStatus is a tutoring language and whether the tutor can speak it right now.
type LanguageStatus struct {
	entity.Language
	Available bool
}

// Catalog lists the languages this deployment offers.
type Catalog struct {
	enabled []string
	avail   LanguageAvailability
}

// NewCatalog offers the enabled language codes (every registry language when enabled is empty;
// unknown codes are ignored). A nil avail reports every offered language as available.
func NewCatalog(enabled []string, avail LanguageAvailability) *Catalog {
	return &Catalog{enabled: slices.Clone(enabled), avail: avail}
}

// Languages returns the offered languages in registry display order.
func (c *Catalog) Languages() []LanguageStatus {
	out := []LanguageStatus{}
	for _, l := range entity.Languages() {
		if len(c.enabled) > 0 && !slices.Contains(c.enabled, l.Code) {
			continue
		}
		out = append(out, LanguageStatus{Language: l, Available: c.avail == nil || c.avail.Available(l.Code)})
	}
	return out
}
