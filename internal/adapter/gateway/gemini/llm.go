// Package gemini implements the LLM port on Google's Gemini API through the genai SDK. It uses
// the stateless Models API: the conversation history lives in Postgres and is sent every turn,
// which keeps reconnects and multiple instances trivially consistent.
package gemini

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"google.golang.org/genai"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

const provider = "gemini"

// Config configures the Gemini client.
type Config struct {
	APIKey        string
	Model         string       // default model; LLMRequest.Model overrides it
	Temperature   *float32     // nil keeps the model default; LLMRequest.Temperature overrides it
	ThinkingLevel string       // "", "minimal", "low", "medium" or "high" (any case); "" keeps the model default
	HTTPClient    *http.Client // nil uses the SDK default
	BaseURL       string       // empty uses Google's endpoint; tests point it at an httptest server
}

// LLM streams tutor replies from Gemini. It is safe for concurrent use.
type LLM struct {
	client   *genai.Client
	apiKey   string
	model    string
	temp     *float32
	thinking genai.ThinkingLevel
}

var _ conversation.LLM = (*LLM)(nil)

var thinkingLevels = map[string]genai.ThinkingLevel{
	"minimal": genai.ThinkingLevelMinimal,
	"low":     genai.ThinkingLevelLow,
	"medium":  genai.ThinkingLevelMedium,
	"high":    genai.ThinkingLevelHigh,
}

// blockedFinish are the finish reasons that mean a safety or policy filter cut the reply off.
var blockedFinish = map[genai.FinishReason]bool{
	genai.FinishReasonSafety:            true,
	genai.FinishReasonProhibitedContent: true,
	genai.FinishReasonBlocklist:         true,
	genai.FinishReasonSPII:              true,
	genai.FinishReasonRecitation:        true,
	genai.FinishReasonLanguage:          true,
}

// New builds a client. It makes no network call.
func New(ctx context.Context, cfg Config) (*LLM, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("gemini: API key is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("gemini: model is required")
	}
	l := &LLM{apiKey: cfg.APIKey, model: cfg.Model, temp: cfg.Temperature}
	if cfg.ThinkingLevel != "" {
		level, ok := thinkingLevels[strings.ToLower(cfg.ThinkingLevel)]
		if !ok {
			return nil, fmt.Errorf("gemini: unknown thinking level %q (want minimal, low, medium or high)", cfg.ThinkingLevel)
		}
		l.thinking = level
	}
	cc := &genai.ClientConfig{APIKey: cfg.APIKey, Backend: genai.BackendGeminiAPI, HTTPClient: cfg.HTTPClient}
	if cfg.BaseURL != "" {
		cc.HTTPOptions = genai.HTTPOptions{BaseURL: cfg.BaseURL}
	}
	client, err := genai.NewClient(ctx, cc)
	if err != nil {
		return nil, fmt.Errorf("gemini: new client: %s", l.redact(err.Error()))
	}
	l.client = client
	return l, nil
}

// Stream yields the reply as Gemini generates it. A refused prompt or a reply cut off by a safety
// filter yields one Blocked delta (with any partial text) and ends the stream without an error.
func (l *LLM) Stream(ctx context.Context, req conversation.LLMRequest) iter.Seq2[conversation.Delta, error] {
	return func(yield func(conversation.Delta, error) bool) {
		model, contents, config := l.request(req)
		for resp, err := range l.client.Models.GenerateContentStream(ctx, model, contents, config) {
			if err != nil {
				yield(conversation.Delta{}, l.mapError(ctx, err))
				return
			}
			d, ok := toDelta(resp)
			if !ok {
				continue
			}
			if !yield(d, nil) || d.Blocked {
				return
			}
		}
	}
}

// Complete returns the whole reply. A blocked reply is a non-retryable *ProviderError wrapping
// conversation.ErrResponseBlocked.
func (l *LLM) Complete(ctx context.Context, req conversation.LLMRequest) (string, error) {
	var text strings.Builder
	for d, err := range l.Stream(ctx, req) {
		if err != nil {
			return "", err
		}
		if d.Blocked {
			return "", &conversation.ProviderError{Provider: provider, Op: "llm", Err: conversation.ErrResponseBlocked}
		}
		text.WriteString(d.Text)
	}
	return strings.TrimSpace(text.String()), nil
}

// request builds a fresh SDK request; the SDK mutates the config, so it is never shared.
func (l *LLM) request(req conversation.LLMRequest) (string, []*genai.Content, *genai.GenerateContentConfig) {
	contents := make([]*genai.Content, 0, len(req.Messages))
	for _, m := range req.Messages {
		var role genai.Role = genai.RoleUser
		if m.Role == conversation.RoleModel {
			role = genai.RoleModel
		}
		contents = append(contents, genai.NewContentFromText(m.Text, role))
	}
	config := &genai.GenerateContentConfig{
		Temperature:     l.temp,
		MaxOutputTokens: req.MaxOutputTokens,
		SafetySettings: []*genai.SafetySetting{
			{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockThresholdBlockMediumAndAbove},
			{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockThresholdBlockMediumAndAbove},
			{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockThresholdBlockMediumAndAbove},
			{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockThresholdBlockMediumAndAbove},
		},
	}
	if req.Temperature != nil {
		config.Temperature = req.Temperature
	}
	if l.thinking != "" {
		config.ThinkingConfig = &genai.ThinkingConfig{ThinkingLevel: l.thinking}
	}
	if req.System != "" {
		config.SystemInstruction = genai.NewContentFromText(req.System, genai.RoleUser)
	}
	return cmp.Or(req.Model, l.model), contents, config
}

// toDelta maps one streamed response; ok is false for chunks carrying nothing (thoughts only,
// no candidates).
func toDelta(resp *genai.GenerateContentResponse) (conversation.Delta, bool) {
	if resp == nil {
		return conversation.Delta{}, false
	}
	if pf := resp.PromptFeedback; pf != nil && pf.BlockReason != "" && pf.BlockReason != genai.BlockedReasonUnspecified {
		return conversation.Delta{Blocked: true, FinishReason: string(pf.BlockReason)}, true
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0] == nil {
		return conversation.Delta{}, false
	}
	c := resp.Candidates[0]
	var text strings.Builder
	if c.Content != nil {
		for _, p := range c.Content.Parts {
			if p != nil && !p.Thought {
				text.WriteString(p.Text)
			}
		}
	}
	d := conversation.Delta{Text: text.String()}
	if c.FinishReason != "" && c.FinishReason != genai.FinishReasonUnspecified {
		d.FinishReason = string(c.FinishReason)
		d.Blocked = blockedFinish[c.FinishReason]
	}
	return d, d.Text != "" || d.FinishReason != ""
}

// mapError turns SDK errors into port errors. Caller cancellation stays a plain context.Canceled;
// API errors keep their status; anything else (network, timeout) is retryable.
func (l *LLM) mapError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var ae genai.APIError
	if errors.As(err, &ae) {
		return &conversation.ProviderError{
			Provider: provider, Op: "llm", Status: ae.Code,
			Retryable: ae.Code == http.StatusTooManyRequests || ae.Code >= 500,
			Err:       errors.New(l.redact(strings.TrimSpace(ae.Status + " " + ae.Message))),
		}
	}
	if msg := err.Error(); strings.Contains(msg, l.apiKey) {
		err = errors.New(l.redact(msg))
	}
	return &conversation.ProviderError{Provider: provider, Op: "llm", Retryable: true, Err: err}
}

func (l *LLM) redact(s string) string {
	if l.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, l.apiKey, "[redacted]")
}
