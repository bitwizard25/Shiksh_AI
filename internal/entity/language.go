package entity

import "slices"

// DefaultLanguage is used when a learner does not choose one.
const DefaultLanguage = "hi"

// Language is a tutoring language. Codes are ISO 639-1, which is also what Bhashini uses.
type Language struct {
	Code       string
	Name       string
	NativeName string
}

var languages = []Language{
	{Code: "hi", Name: "Hindi", NativeName: "हिन्दी"},
	{Code: "mr", Name: "Marathi", NativeName: "मराठी"},
	{Code: "bn", Name: "Bengali", NativeName: "বাংলা"},
	{Code: "ta", Name: "Tamil", NativeName: "தமிழ்"},
	{Code: "te", Name: "Telugu", NativeName: "తెలుగు"},
	{Code: "gu", Name: "Gujarati", NativeName: "ગુજરાતી"},
	{Code: "kn", Name: "Kannada", NativeName: "ಕನ್ನಡ"},
	{Code: "ml", Name: "Malayalam", NativeName: "മലയാളം"},
	{Code: "en", Name: "English", NativeName: "English"},
}

// Languages returns the supported languages in display order. The caller may modify the result.
func Languages() []Language { return slices.Clone(languages) }

// LookupLanguage returns the language with the given code.
func LookupLanguage(code string) (Language, bool) {
	for _, l := range languages {
		if l.Code == code {
			return l, true
		}
	}
	return Language{}, false
}

// ValidateLanguage checks code is a supported language.
func ValidateLanguage(code string) error {
	if _, ok := LookupLanguage(code); !ok {
		return &ValidationError{Field: "preferred_lang", Message: "is not a supported language"}
	}
	return nil
}
