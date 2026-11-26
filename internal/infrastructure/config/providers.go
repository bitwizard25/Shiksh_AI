package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v11"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

const (
	ProvidersFake = "fake"
	ProvidersReal = "real"
)

const (
	SpeechProviderFake     = "fake"
	SpeechProviderBhashini = "bhashini"
	SpeechProviderSarvam   = "sarvam"
)

type ProviderConfig struct {
	Mode                   string   `env:"PROVIDERS" envDefault:"fake"`
	SpeechProvider         string   `env:"SPEECH_PROVIDER"`
	BhashiniUserID         string   `env:"BHASHINI_USER_ID"`
	BhashiniULCAKey        string   `env:"BHASHINI_ULCA_API_KEY"`
	BhashiniPipelineID     string   `env:"BHASHINI_PIPELINE_ID" envDefault:"64392f96daac500b55c543cd"`
	BhashiniConfigURL      string   `env:"BHASHINI_CONFIG_URL" envDefault:"https://meity-auth.ulcacontrib.org/ulca/apis/v0/model/getModelsPipeline"`
	SarvamAPIKey           string   `env:"SARVAM_API_KEY"`
	SarvamBaseURL          string   `env:"SARVAM_BASE_URL" envDefault:"https://api.sarvam.ai"`
	SarvamSTTModel         string   `env:"SARVAM_STT_MODEL" envDefault:"saaras:v4"`
	SarvamTTSModel         string   `env:"SARVAM_TTS_MODEL" envDefault:"bulbul:v4-flash"`
	SarvamTTSSpeaker       string   `env:"SARVAM_TTS_SPEAKER" envDefault:"shubh_enhi_ads"`
	GeminiAPIKey           string   `env:"GEMINI_API_KEY"`
	GeminiModel            string   `env:"GEMINI_MODEL" envDefault:"gemini-3.8-flash"`
	GeminiFallbackModel    string   `env:"GEMINI_FALLBACK_MODEL" envDefault:"gemini-3.5-flash-lite"`
	GeminiSummaryModel     string   `env:"GEMINI_SUMMARY_MODEL" envDefault:"gemini-3.5-flash-lite"`
	GeminiTemperature      string   `env:"GEMINI_TEMPERATURE"`
	GeminiThinkingLevel    string   `env:"GEMINI_THINKING_LEVEL" envDefault:"minimal"`
	EnabledLanguages       []string `env:"ENABLED_LANGUAGES" envSeparator:"," envDefault:"hi,mr,bn,ta,te,gu,kn,ml,en"`
	ProviderMaxConcurrency int      `env:"PROVIDER_MAX_CONCURRENCY" envDefault:"64"`
}

func (p ProviderConfig) EffectiveSpeechProvider() string {
	if sp := strings.ToLower(strings.TrimSpace(p.SpeechProvider)); sp != "" {
		return sp
	}
	if p.Mode == ProvidersReal {
		return SpeechProviderBhashini
	}
	return SpeechProviderFake
}

func LoadProviderConfig() (ProviderConfig, error) {
	return LoadProviderConfigFrom(envMap(os.Environ()))
}

func LoadProviderConfigFrom(environ map[string]string) (ProviderConfig, error) {
	var p ProviderConfig
	if err := env.ParseWithOptions(&p, env.Options{Environment: environ}); err != nil {
		return ProviderConfig{}, fmt.Errorf("config: %w", err)
	}
	p.normalize()
	if err := p.Validate(); err != nil {
		return ProviderConfig{}, fmt.Errorf("config: %w", err)
	}
	return p, nil
}

func (p *ProviderConfig) normalize() {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	p.SpeechProvider = strings.ToLower(strings.TrimSpace(p.SpeechProvider))
	p.GeminiThinkingLevel = strings.ToLower(strings.TrimSpace(p.GeminiThinkingLevel))
	p.GeminiTemperature = strings.TrimSpace(p.GeminiTemperature)
	langs := cleanList(p.EnabledLanguages)
	for i, code := range langs {
		langs[i] = strings.ToLower(code)
	}
	p.EnabledLanguages = langs
}

func (p ProviderConfig) Validate() error {
	var errs []error
	switch p.Mode {
	case ProvidersFake:
		if p.SpeechProvider != "" && p.SpeechProvider != SpeechProviderFake {
			errs = append(errs, fmt.Errorf("PROVIDERS=fake cannot be combined with SPEECH_PROVIDER=%q", p.SpeechProvider))
		}
	case ProvidersReal:
		for name, v := range map[string]string{
			"GEMINI_API_KEY": p.GeminiAPIKey, "GEMINI_MODEL": p.GeminiModel,
		} {
			if strings.TrimSpace(v) == "" {
				errs = append(errs, fmt.Errorf("PROVIDERS=real requires %s", name))
			}
		}
		switch p.EffectiveSpeechProvider() {
		case SpeechProviderBhashini:
			for name, v := range map[string]string{
				"BHASHINI_USER_ID": p.BhashiniUserID, "BHASHINI_ULCA_API_KEY": p.BhashiniULCAKey,
				"BHASHINI_PIPELINE_ID": p.BhashiniPipelineID, "BHASHINI_CONFIG_URL": p.BhashiniConfigURL,
			} {
				if strings.TrimSpace(v) == "" {
					errs = append(errs, fmt.Errorf("SPEECH_PROVIDER=bhashini requires %s", name))
				}
			}
		case SpeechProviderSarvam:
			for name, v := range map[string]string{
				"SARVAM_API_KEY": p.SarvamAPIKey, "SARVAM_BASE_URL": p.SarvamBaseURL,
			} {
				if strings.TrimSpace(v) == "" {
					errs = append(errs, fmt.Errorf("SPEECH_PROVIDER=sarvam requires %s", name))
				}
			}
		default:
			errs = append(errs, fmt.Errorf("SPEECH_PROVIDER %q must be %q or %q", p.EffectiveSpeechProvider(), SpeechProviderBhashini, SpeechProviderSarvam))
		}
	default:
		errs = append(errs, fmt.Errorf("PROVIDERS %q must be %q or %q", p.Mode, ProvidersReal, ProvidersFake))
	}
	if len(p.EnabledLanguages) == 0 {
		errs = append(errs, errors.New("ENABLED_LANGUAGES must list at least one language"))
	}
	for _, code := range p.EnabledLanguages {
		if _, ok := entity.LookupLanguage(code); !ok {
			errs = append(errs, fmt.Errorf("ENABLED_LANGUAGES: %q is not a supported language", code))
		}
	}
	if p.GeminiTemperature != "" {
		if t, err := strconv.ParseFloat(p.GeminiTemperature, 32); err != nil || t < 0 || t > 2 {
			errs = append(errs, errors.New("GEMINI_TEMPERATURE must be a number from 0 to 2"))
		}
	}
	switch p.GeminiThinkingLevel {
	case "", "minimal", "low", "medium", "high":
	default:
		errs = append(errs, errors.New("GEMINI_THINKING_LEVEL must be minimal, low, medium or high"))
	}
	if p.ProviderMaxConcurrency < 1 {
		errs = append(errs, errors.New("PROVIDER_MAX_CONCURRENCY must be at least 1"))
	}
	return errors.Join(errs...)
}

func (p ProviderConfig) Temperature() *float32 {
	if p.GeminiTemperature == "" {
		return nil
	}
	t, err := strconv.ParseFloat(p.GeminiTemperature, 32)
	if err != nil {
		return nil
	}
	v := float32(t)
	return &v
}
