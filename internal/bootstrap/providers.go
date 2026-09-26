package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/bhashini"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/fake"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/gemini"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// geminiOrigin is warmed alongside the Bhashini inference endpoints.
const geminiOrigin = "https://generativelanguage.googleapis.com"

// Providers is the speech and language stack; every provider is wrapped in its guard.
type Providers struct {
	ASR          conversation.ASR
	TTS          conversation.TTS
	LLM          conversation.LLM
	Availability usecase.LanguageAvailability
	Warmer       *gateway.Warmer // nil for fake providers (a nil *Warmer is a no-op)
}

// BuildProviders builds fake or real providers from cfg and registers their metrics on reg (nil:
// unregistered). For real providers it starts resolving Bhashini's per-language configuration in
// the background for as long as ctx lives; a language reports unavailable until that succeeds. log
// receives provider warnings, such as a failed Bhashini config fetch during warming (nil discards
// them).
func BuildProviders(ctx context.Context, cfg config.ProviderConfig, reg prometheus.Registerer, log *slog.Logger) (Providers, error) {
	m := gateway.NewMetrics(reg)
	switch cfg.Mode {
	case config.ProvidersFake:
		o := fake.Defaults()
		g := gateway.NewGuard(gateway.GuardConfig{Provider: "fake", MaxConcurrency: cfg.ProviderMaxConcurrency}, m)
		return Providers{
			ASR:          g.ASR(fake.NewASR(o)),
			TTS:          g.TTS(fake.NewTTS(o)),
			LLM:          g.LLM(fake.NewLLM(o)),
			Availability: fake.NewAvailability(cfg.EnabledLanguages),
		}, nil
	case config.ProvidersReal:
		client := gateway.NewHTTPClient()
		speech := bhashini.New(bhashini.Config{
			UserID: cfg.BhashiniUserID, ULCAKey: cfg.BhashiniULCAKey,
			PipelineID: cfg.BhashiniPipelineID, ConfigURL: cfg.BhashiniConfigURL, HTTPClient: client,
			Logger: log,
		})
		llm, err := gemini.New(ctx, gemini.Config{
			APIKey: cfg.GeminiAPIKey, Model: cfg.GeminiModel, Temperature: cfg.Temperature(),
			ThinkingLevel: cfg.GeminiThinkingLevel, HTTPClient: client,
		})
		if err != nil {
			return Providers{}, err
		}
		speechGuard := gateway.NewGuard(gateway.GuardConfig{Provider: "bhashini", MaxConcurrency: cfg.ProviderMaxConcurrency}, m)
		llmGuard := gateway.NewGuard(gateway.GuardConfig{Provider: "gemini", MaxConcurrency: cfg.ProviderMaxConcurrency}, m)
		speech.Warm(ctx, cfg.EnabledLanguages)
		return Providers{
			ASR:          speechGuard.ASR(speech),
			TTS:          speechGuard.TTS(speech),
			LLM:          llmGuard.LLM(llm),
			Availability: speech,
			Warmer:       gateway.NewWarmer(client, func() []string { return append(speech.Endpoints(), geminiOrigin) }),
		}, nil
	default:
		return Providers{}, fmt.Errorf("unknown provider mode %q", cfg.Mode)
	}
}
