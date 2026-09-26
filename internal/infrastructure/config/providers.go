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

// Provider modes.
const (
	ProvidersFake = "fake" // simulated speech and language: no keys, no network (development, tests)
	ProvidersReal = "real" // Bhashini + Gemini
)

// ProviderConfig selects and configures the speech (Bhashini) and language (Gemini) providers.
type ProviderConfig struct {
	Mode                   string   `env:"PROVIDERS" envDefault:"fake"`
	BhashiniUserID         string   `env:"BHASHINI_USER_ID"`
	BhashiniULCAKey        string   `env:"BHASHINI_ULCA_API_KEY"`
	BhashiniPipelineID     string   `env:"BHASHINI_PIPELINE_ID" envDefault:"64392f96daac500b55c543cd"`
	BhashiniConfigURL      string   `env:"BHASHINI_CONFIG_URL" envDefault:"https://meity-auth.ulcacontrib.org/ulca/apis/v0/model/getModelsPipeline"`
	GeminiAPIKey           string   `env:"GEMINI_API_KEY"`
	GeminiModel            string   `env:"GEMINI_MODEL" envDefault:"gemini-3.8-flash"`
	GeminiFallbackModel    string   `env:"GEMINI_FALLBACK_MODEL" envDefault:"gemini-3.5-flash-lite"`
	GeminiSummaryModel     string   `env:"GEMINI_SUMMARY_MODEL" envDefault:"gemini-3.5-flash-lite"`
	GeminiTemperature      string   `env:"GEMINI_TEMPERATURE"` // empty keeps the model default
	GeminiThinkingLevel    string   `env:"GEMINI_THINKING_LEVEL" envDefault:"minimal"`
	EnabledLanguages       []string `env:"ENABLED_LANGUAGES" envSeparator:"," envDefault:"hi,mr,bn,ta,te,gu,kn,ml,en"`
	ProviderMaxConcurrency int      `env:"PROVIDER_MAX_CONCURRENCY" envDefault:"64"`
}

// LoadProviderConfig reads only the provider settings from the process environment, for tools
// that need no database or JWT settings.
func LoadProviderConfig() (ProviderConfig, error) {
	return LoadProviderConfigFrom(envMap(os.Environ()))
}

// LoadProviderConfigFrom reads only the provider settings from the given variables.
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
	p.GeminiThinkingLevel = strings.ToLower(strings.TrimSpace(p.GeminiThinkingLevel))
	p.GeminiTemperature = strings.TrimSpace(p.GeminiTemperature)
	langs := cleanList(p.EnabledLanguages)
	for i, code := range langs {
		langs[i] = strings.ToLower(code)
	}
	p.EnabledLanguages = langs
}

// Validate checks the provider settings. Error messages name variables, never their values.
func (p ProviderConfig) Validate() error {
	var errs []error
	switch p.Mode {
	case ProvidersFake:
	case ProvidersReal:
		for name, v := range map[string]string{
			"BHASHINI_USER_ID": p.BhashiniUserID, "BHASHINI_ULCA_API_KEY": p.BhashiniULCAKey,
			"BHASHINI_PIPELINE_ID": p.BhashiniPipelineID, "BHASHINI_CONFIG_URL": p.BhashiniConfigURL,
			"GEMINI_API_KEY": p.GeminiAPIKey, "GEMINI_MODEL": p.GeminiModel,
		} {
			if strings.TrimSpace(v) == "" {
				errs = append(errs, fmt.Errorf("PROVIDERS=real requires %s", name))
			}
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

// Temperature returns GEMINI_TEMPERATURE, or nil when it is unset (keep the model default).
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
