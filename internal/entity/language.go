package entity

import "slices"

// DefaultLanguage is used when a learner does not choose one.
const DefaultLanguage = "hi"

// Phrases are the short fixed lines the tutor speaks outside model answers. They are synthesized
// once into audio clips (voicecli assets) and played without calling any provider.
type Phrases struct {
	Fillers  []string // thinking sounds played while an answer is slow; each well under a second
	Repeat   string   // asks the learner to say it again (nothing usable was heard)
	Error    string   // apologizes after a failure
	Redirect string   // steers back to the lesson after a blocked or off-topic answer
}

// Language is a tutoring language. Codes are ISO 639-1, which is also what Bhashini uses.
type Language struct {
	Code       string
	Name       string
	NativeName string
	TTSGender  string // Bhashini TTS voice: "female" or "male"
	Phrases    Phrases
}

func (l Language) clone() Language {
	l.Phrases.Fillers = slices.Clone(l.Phrases.Fillers)
	return l
}

var languages = []Language{
	{Code: "hi", Name: "Hindi", NativeName: "हिन्दी", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"हम्म…", "अच्छा…", "ठीक है…"},
		Repeat:   "माफ़ कीजिए, क्या आप फिर से बोल सकते हैं?",
		Error:    "कुछ गड़बड़ हो गई, चलिए फिर से कोशिश करते हैं।",
		Redirect: "चलिए, अपने विषय पर वापस आते हैं।",
	}},
	{Code: "mr", Name: "Marathi", NativeName: "मराठी", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"हम्म…", "बरं…", "ठीक आहे…"},
		Repeat:   "माफ करा, पुन्हा एकदा सांगाल का?",
		Error:    "काहीतरी चूक झाली, चला पुन्हा प्रयत्न करूया.",
		Redirect: "चला, आपल्या विषयाकडे परत येऊया.",
	}},
	{Code: "bn", Name: "Bengali", NativeName: "বাংলা", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"হুম…", "আচ্ছা…", "ঠিক আছে…"},
		Repeat:   "দুঃখিত, আবার একবার বলবেন?",
		Error:    "কিছু একটা সমস্যা হয়েছে, চলুন আবার চেষ্টা করি।",
		Redirect: "চলুন, আমাদের বিষয়ে ফিরে যাই।",
	}},
	{Code: "ta", Name: "Tamil", NativeName: "தமிழ்", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"ம்ம்…", "சரி…", "அப்படியா…"},
		Repeat:   "மன்னிக்கவும், மீண்டும் ஒருமுறை சொல்ல முடியுமா?",
		Error:    "ஏதோ தவறு நடந்துவிட்டது, மீண்டும் முயற்சி செய்வோம்.",
		Redirect: "சரி, நம் பாடத்திற்குத் திரும்புவோம்.",
	}},
	{Code: "te", Name: "Telugu", NativeName: "తెలుగు", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"హ్మ్…", "సరే…", "అలాగా…"},
		Repeat:   "క్షమించండి, మళ్ళీ ఒకసారి చెప్పగలరా?",
		Error:    "ఏదో పొరపాటు జరిగింది, మళ్ళీ ప్రయత్నిద్దాం.",
		Redirect: "సరే, మన విషయానికి తిరిగి వద్దాం.",
	}},
	{Code: "gu", Name: "Gujarati", NativeName: "ગુજરાતી", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"હમ્મ…", "અચ્છા…", "બરાબર…"},
		Repeat:   "માફ કરશો, ફરીથી કહેશો?",
		Error:    "કંઈક ખોટું થયું, ચાલો ફરી પ્રયાસ કરીએ.",
		Redirect: "ચાલો, આપણા વિષય પર પાછા આવીએ.",
	}},
	{Code: "kn", Name: "Kannada", NativeName: "ಕನ್ನಡ", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"ಹ್ಮ್…", "ಸರಿ…", "ಹೌದಾ…"},
		Repeat:   "ಕ್ಷಮಿಸಿ, ಮತ್ತೊಮ್ಮೆ ಹೇಳುತ್ತೀರಾ?",
		Error:    "ಏನೋ ತಪ್ಪಾಯಿತು, ಮತ್ತೆ ಪ್ರಯತ್ನಿಸೋಣ.",
		Redirect: "ಸರಿ, ನಮ್ಮ ವಿಷಯಕ್ಕೆ ಹಿಂತಿರುಗೋಣ.",
	}},
	{Code: "ml", Name: "Malayalam", NativeName: "മലയാളം", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"ഹ്മ്…", "ശരി…", "അങ്ങനെയാണോ…"},
		Repeat:   "ക്ഷമിക്കണം, ഒന്നുകൂടി പറയാമോ?",
		Error:    "എന്തോ പിശക് സംഭവിച്ചു, നമുക്ക് വീണ്ടും ശ്രമിക്കാം.",
		Redirect: "ശരി, നമുക്ക് വിഷയത്തിലേക്ക് തിരിച്ചുവരാം.",
	}},
	{Code: "en", Name: "English", NativeName: "English", TTSGender: "female", Phrases: Phrases{
		Fillers:  []string{"Hmm…", "Okay…", "Let me think…"},
		Repeat:   "Sorry, could you say that again?",
		Error:    "Something went wrong. Let's try that again.",
		Redirect: "Let's get back to our topic.",
	}},
}

// Languages returns the supported languages in display order. The caller may modify the result.
func Languages() []Language {
	out := make([]Language, len(languages))
	for i, l := range languages {
		out[i] = l.clone()
	}
	return out
}

// LookupLanguage returns the language with the given code.
func LookupLanguage(code string) (Language, bool) {
	for _, l := range languages {
		if l.Code == code {
			return l.clone(), true
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
