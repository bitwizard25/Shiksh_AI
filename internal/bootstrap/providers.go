package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/bhashini"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/fake"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/gemini"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/sarvam"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/clips"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

const geminiOrigin = "https://generativelanguage.googleapis.com"

type Providers struct {
	ASR          conversation.ASR
	TTS          conversation.TTS
	LLM          conversation.LLM
	Availability usecase.LanguageAvailability
	Clips        conversation.Clips
	Warmer       *gateway.Warmer
}

func buildSpeechProvider(cfg config.ProviderConfig, client *http.Client, log *slog.Logger) (gateway.SpeechProvider, error) {
	switch cfg.EffectiveSpeechProvider() {
	case config.SpeechProviderBhashini:
		return bhashini.New(bhashini.Config{
			UserID: cfg.BhashiniUserID, ULCAKey: cfg.BhashiniULCAKey,
			PipelineID: cfg.BhashiniPipelineID, ConfigURL: cfg.BhashiniConfigURL,
			HTTPClient: client, Logger: log,
		}), nil
	case config.SpeechProviderSarvam:
		return sarvam.New(sarvam.Config{
			APIKey: cfg.SarvamAPIKey, BaseURL: cfg.SarvamBaseURL,
			STTModel: cfg.SarvamSTTModel, TTSModel: cfg.SarvamTTSModel,
			TTSSpeaker: cfg.SarvamTTSSpeaker, Languages: cfg.EnabledLanguages,
			HTTPClient: client, Logger: log,
		}), nil
	default:
		return nil, fmt.Errorf("unknown speech provider %q", cfg.EffectiveSpeechProvider())
	}
}

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
			Clips:        fake.Clips{},
		}, nil
	case config.ProvidersReal:
		client := gateway.NewHTTPClient()
		speech, err := buildSpeechProvider(cfg, client, log)
		if err != nil {
			return Providers{}, err
		}
		llm, err := gemini.New(ctx, gemini.Config{
			APIKey: cfg.GeminiAPIKey, Model: cfg.GeminiModel, Temperature: cfg.Temperature(),
			ThinkingLevel: cfg.GeminiThinkingLevel, HTTPClient: client,
		})
		if err != nil {
			return Providers{}, err
		}
		speechGuard := gateway.NewGuard(gateway.GuardConfig{Provider: speech.Name(), MaxConcurrency: cfg.ProviderMaxConcurrency}, m)
		llmGuard := gateway.NewGuard(gateway.GuardConfig{Provider: "gemini", MaxConcurrency: cfg.ProviderMaxConcurrency}, m)
		speech.Warm(ctx, cfg.EnabledLanguages)
		return Providers{
			ASR:          speechGuard.ASR(speech),
			TTS:          speechGuard.TTS(speech),
			LLM:          llmGuard.LLM(llm),
			Availability: speech,
			Clips:        clips.Embedded(log),
			Warmer:       gateway.NewWarmer(client, func() []string { return append(speech.Endpoints(), geminiOrigin) }),
		}, nil
	default:
		return Providers{}, fmt.Errorf("unknown provider mode %q", cfg.Mode)
	}
}
