package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
)

func realEnv() map[string]string {
	return map[string]string{
		"PROVIDERS":             "real",
		"BHASHINI_USER_ID":      "user-1",
		"BHASHINI_ULCA_API_KEY": "secret-ulca",
		"GEMINI_API_KEY":        "secret-gemini",
	}
}

func TestProviderDefaults(t *testing.T) {
	p, err := config.LoadProviderConfigFrom(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != config.ProvidersFake || p.GeminiModel != "gemini-3.8-flash" || p.GeminiFallbackModel != "gemini-3.5-flash-lite" ||
		p.GeminiSummaryModel != "gemini-3.5-flash-lite" || p.GeminiThinkingLevel != "minimal" || p.ProviderMaxConcurrency != 64 {
		t.Errorf("defaults = %+v", p)
	}
	if p.BhashiniPipelineID != "64392f96daac500b55c543cd" || !strings.HasPrefix(p.BhashiniConfigURL, "https://meity-auth.ulcacontrib.org/") {
		t.Errorf("bhashini defaults = %q %q", p.BhashiniPipelineID, p.BhashiniConfigURL)
	}
	if want := []string{"hi", "mr", "bn", "ta", "te", "gu", "kn", "ml", "en"}; !slices.Equal(p.EnabledLanguages, want) {
		t.Errorf("EnabledLanguages = %v", p.EnabledLanguages)
	}
	if p.Temperature() != nil {
		t.Errorf("Temperature = %v, want nil (model default)", *p.Temperature())
	}
}

func TestRealProvidersRequireKeys(t *testing.T) {
	if _, err := config.LoadProviderConfigFrom(realEnv()); err != nil {
		t.Fatalf("complete real config rejected: %v", err)
	}
	for _, key := range []string{"BHASHINI_USER_ID", "BHASHINI_ULCA_API_KEY", "GEMINI_API_KEY"} {
		env := realEnv()
		delete(env, key)
		_, err := config.LoadProviderConfigFrom(env)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("without %s: err = %v", key, err)
			continue
		}
		if strings.Contains(err.Error(), "secret-") {
			t.Errorf("error leaks a secret: %v", err)
		}
	}
}

func TestProviderValidation(t *testing.T) {
	for name, kv := range map[string][2]string{
		"unknown mode":          {"PROVIDERS", "magic"},
		"unsupported language":  {"ENABLED_LANGUAGES", "hi,xx"},
		"no languages":          {"ENABLED_LANGUAGES", " , "},
		"temperature not float": {"GEMINI_TEMPERATURE", "hot"},
		"temperature too high":  {"GEMINI_TEMPERATURE", "3"},
		"thinking level":        {"GEMINI_THINKING_LEVEL", "extreme"},
		"concurrency":           {"PROVIDER_MAX_CONCURRENCY", "0"},
	} {
		env := realEnv()
		env[kv[0]] = kv[1]
		if _, err := config.LoadProviderConfigFrom(env); err == nil || !strings.Contains(err.Error(), kv[0]) {
			t.Errorf("%s: err = %v, want it to mention %s", name, err, kv[0])
		}
	}
}

func TestProviderNormalization(t *testing.T) {
	env := realEnv()
	env["PROVIDERS"] = " Real "
	env["ENABLED_LANGUAGES"] = " HI , en ,"
	env["GEMINI_TEMPERATURE"] = "0.3"
	env["GEMINI_THINKING_LEVEL"] = "LOW"
	p, err := config.LoadProviderConfigFrom(env)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != config.ProvidersReal || !slices.Equal(p.EnabledLanguages, []string{"hi", "en"}) || p.GeminiThinkingLevel != "low" {
		t.Errorf("normalized = %+v", p)
	}
	if tp := p.Temperature(); tp == nil || *tp != 0.3 {
		t.Errorf("Temperature = %v", tp)
	}
}

func TestLoadFromIncludesProviders(t *testing.T) {
	env := baseEnv()
	env["ENABLED_LANGUAGES"] = "hi"
	cfg, err := config.LoadFrom(env)
	if err != nil || !slices.Equal(cfg.Providers.EnabledLanguages, []string{"hi"}) || cfg.Providers.Mode != config.ProvidersFake {
		t.Fatalf("LoadFrom = %+v, %v", cfg.Providers, err)
	}
	env["PROVIDERS"] = "real"
	if _, err := config.LoadFrom(env); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("real without keys: err = %v", err)
	}
}
