package usecase

import "github.com/bitwizard25/Shiksh_AI/internal/entity"

// Languages lists the languages the tutor speaks, in display order.
func Languages() []entity.Language { return entity.Languages() }
