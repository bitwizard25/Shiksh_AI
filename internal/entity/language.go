package entity

import "slices"

// DefaultLanguage is used when a learner does not choose one.
const DefaultLanguage = "hi"

// Language is a tutoring language. Codes are ISO 639-1, which is also what Bhashini uses.
type Language struct {
	Code       string
	Name       string
	NativeName string
	// Redirect is a short spoken sentence that steers the learner back to the lesson.
	Redirect string
}

var languages = []Language{
	{Code: "hi", Name: "Hindi", NativeName: "हिन्दी", Redirect: "चलिए, हम अपनी पढ़ाई पर वापस आते हैं।"},
	{Code: "mr", Name: "Marathi", NativeName: "मराठी", Redirect: "चला, आपण अभ्यासाकडे परत येऊया."},
	{Code: "bn", Name: "Bengali", NativeName: "বাংলা", Redirect: "চলো, আমরা পড়াশোনায় ফিরে যাই।"},
	{Code: "ta", Name: "Tamil", NativeName: "தமிழ்", Redirect: "வாங்க, நாம் படிப்புக்குத் திரும்புவோம்."},
	{Code: "te", Name: "Telugu", NativeName: "తెలుగు", Redirect: "రండి, మన చదువుకు తిరిగి వద్దాం."},
	{Code: "gu", Name: "Gujarati", NativeName: "ગુજરાતી", Redirect: "ચાલો, આપણે અભ્યાસ પર પાછા ફરીએ."},
	{Code: "kn", Name: "Kannada", NativeName: "ಕನ್ನಡ", Redirect: "ಬನ್ನಿ, ನಾವು ಓದಿನ ಕಡೆಗೆ ಮರಳೋಣ."},
	{Code: "ml", Name: "Malayalam", NativeName: "മലയാളം", Redirect: "വരൂ, നമുക്ക് പഠനത്തിലേക്ക് മടങ്ങാം."},
	{Code: "en", Name: "English", NativeName: "English", Redirect: "Let's get back to our lesson."},
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
