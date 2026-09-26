package gemini_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/gemini"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

type wireRequest struct {
	Contents []struct {
		Role  string                  `json:"role"`
		Parts []struct{ Text string } `json:"parts"`
	} `json:"contents"`
	SystemInstruction *struct {
		Parts []struct{ Text string } `json:"parts"`
	} `json:"systemInstruction"`
	GenerationConfig struct {
		Temperature     *float64 `json:"temperature"`
		MaxOutputTokens int      `json:"maxOutputTokens"`
		ThinkingConfig  *struct {
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
	SafetySettings []struct{ Category, Threshold string } `json:"safetySettings"`
}

// captured records the last request the fake Gemini server received.
type captured struct {
	mu     sync.Mutex
	path   string
	alt    string
	apiKey string
	body   wireRequest
}

func (c *captured) record(t *testing.T, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path, c.alt, c.apiKey = r.URL.Path, r.URL.Query().Get("alt"), r.Header.Get("x-goog-api-key")
	if err := json.NewDecoder(r.Body).Decode(&c.body); err != nil {
		t.Errorf("decode request: %v", err)
	}
}

// sse answers with the given JSON payloads as server-sent events.
func sse(t *testing.T, c *captured, events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c != nil {
			c.record(t, r)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\r\n\r\n", e)
			w.(http.Flusher).Flush()
		}
	}
}

// chunk is one streamed GenerateContentResponse. Parts prefixed with "thought:" are thought parts.
func chunk(finish string, parts ...string) string {
	ps := []map[string]any{}
	for _, p := range parts {
		if s, ok := strings.CutPrefix(p, "thought:"); ok {
			ps = append(ps, map[string]any{"text": s, "thought": true})
		} else {
			ps = append(ps, map[string]any{"text": p})
		}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": ps}}
	if finish != "" {
		cand["finishReason"] = finish
	}
	b, _ := json.Marshal(map[string]any{"candidates": []any{cand}})
	return string(b)
}

func newLLM(t *testing.T, cfg gemini.Config, h http.HandlerFunc) *gemini.LLM {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.APIKey, cfg.HTTPClient, cfg.BaseURL = "test-key", srv.Client(), srv.URL
	if cfg.Model == "" {
		cfg.Model = "gemini-test"
	}
	llm, err := gemini.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return llm
}

func collect(seq iter.Seq2[conversation.Delta, error]) ([]conversation.Delta, error) {
	var out []conversation.Delta
	for d, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, d)
	}
	return out, nil
}

var question = conversation.LLMRequest{
	System: "You are Shiksha.",
	Messages: []conversation.ChatMessage{
		{Role: conversation.RoleUser, Text: "पहला सवाल"},
		{Role: conversation.RoleModel, Text: "पहला जवाब"},
		{Role: conversation.RoleUser, Text: "भिन्न क्या है?"},
	},
	MaxOutputTokens: 400,
}

func TestStreamSendsRequestAndYieldsText(t *testing.T) {
	var c captured
	llm := newLLM(t, gemini.Config{ThinkingLevel: "minimal"}, sse(t, &c, chunk("", "भिन्न"), chunk("", " एक भाग है।"), chunk("STOP", " समझे?")))

	deltas, err := collect(llm.Stream(context.Background(), question))
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, d := range deltas {
		text.WriteString(d.Text)
	}
	if text.String() != "भिन्न एक भाग है। समझे?" || deltas[len(deltas)-1].FinishReason != "STOP" || deltas[len(deltas)-1].Blocked {
		t.Fatalf("deltas = %+v", deltas)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != "/v1beta/models/gemini-test:streamGenerateContent" || c.alt != "sse" || c.apiKey != "test-key" {
		t.Errorf("request = %s alt=%s key=%q", c.path, c.alt, c.apiKey)
	}
	b := c.body
	if len(b.Contents) != 3 || b.Contents[0].Role != "user" || b.Contents[1].Role != "model" || b.Contents[2].Parts[0].Text != "भिन्न क्या है?" {
		t.Errorf("contents = %+v", b.Contents)
	}
	if b.SystemInstruction == nil || b.SystemInstruction.Parts[0].Text != "You are Shiksha." {
		t.Errorf("systemInstruction = %+v", b.SystemInstruction)
	}
	g := b.GenerationConfig
	if g.MaxOutputTokens != 400 || g.Temperature != nil || g.ThinkingConfig == nil || g.ThinkingConfig.ThinkingLevel != string(genai.ThinkingLevelMinimal) {
		t.Errorf("generationConfig = %+v (thinking %+v)", g, g.ThinkingConfig)
	}
	if len(b.SafetySettings) != 4 {
		t.Fatalf("safetySettings = %+v", b.SafetySettings)
	}
	for _, s := range b.SafetySettings {
		if s.Threshold != string(genai.HarmBlockThresholdBlockMediumAndAbove) {
			t.Errorf("safety setting %+v", s)
		}
	}
}

func TestModelAndTemperatureOverrides(t *testing.T) {
	var c captured
	temp := float32(0.4)
	llm := newLLM(t, gemini.Config{Temperature: &temp}, sse(t, &c, chunk("STOP", "ok")))

	if _, err := collect(llm.Stream(context.Background(), question)); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	if tp := c.body.GenerationConfig.Temperature; tp == nil || math.Abs(*tp-0.4) > 1e-6 || c.body.GenerationConfig.ThinkingConfig != nil {
		t.Errorf("config temperature: got %v, thinking %+v", tp, c.body.GenerationConfig.ThinkingConfig)
	}
	c.mu.Unlock()

	req := question
	hot := float32(0.9)
	req.Model, req.Temperature = "gemini-other", &hot
	if _, err := collect(llm.Stream(context.Background(), req)); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if tp := c.body.GenerationConfig.Temperature; c.path != "/v1beta/models/gemini-other:streamGenerateContent" || tp == nil || math.Abs(*tp-0.9) > 1e-6 {
		t.Errorf("override: path %s, temperature %v", c.path, tp)
	}
}

func TestStreamReportsBlockedPrompt(t *testing.T) {
	llm := newLLM(t, gemini.Config{}, sse(t, nil, `{"promptFeedback":{"blockReason":"SAFETY"}}`))
	deltas, err := collect(llm.Stream(context.Background(), question))
	if err != nil || len(deltas) != 1 || !deltas[0].Blocked || deltas[0].Text != "" || deltas[0].FinishReason != "SAFETY" {
		t.Fatalf("deltas = %+v, err = %v", deltas, err)
	}
}

func TestStreamFinishReasons(t *testing.T) {
	cases := []struct {
		finish  string
		blocked bool
	}{{"SAFETY", true}, {"PROHIBITED_CONTENT", true}, {"RECITATION", true}, {"MAX_TOKENS", false}, {"STOP", false}}
	for _, tc := range cases {
		llm := newLLM(t, gemini.Config{}, sse(t, nil, chunk("", "आधा"), chunk(tc.finish, " जवाब"), chunk("", "never read")))
		deltas, err := collect(llm.Stream(context.Background(), question))
		if err != nil {
			t.Fatalf("%s: %v", tc.finish, err)
		}
		last := deltas[len(deltas)-1]
		if tc.blocked && (len(deltas) != 2 || !last.Blocked || last.Text != " जवाब") {
			t.Errorf("%s: deltas = %+v; want the partial text flagged blocked and the stream stopped", tc.finish, deltas)
		}
		if !tc.blocked && (deltas[1].Blocked || deltas[1].FinishReason != tc.finish) {
			t.Errorf("%s: deltas = %+v", tc.finish, deltas)
		}
	}
}

func TestStreamSkipsThoughtsAndEmptyChunks(t *testing.T) {
	llm := newLLM(t, gemini.Config{}, sse(t, nil, chunk("", "thought:let me think"), `{"candidates":[]}`, chunk(""), chunk("STOP", "नमस्ते")))
	deltas, err := collect(llm.Stream(context.Background(), question))
	if err != nil || len(deltas) != 1 || deltas[0].Text != "नमस्ते" {
		t.Fatalf("deltas = %+v, err = %v", deltas, err)
	}
}

func TestStreamErrorMapping(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
	}{{http.StatusTooManyRequests, true}, {http.StatusServiceUnavailable, true}, {http.StatusBadRequest, false}}
	for _, tc := range cases {
		llm := newLLM(t, gemini.Config{}, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			fmt.Fprintf(w, `{"error":{"code":%d,"message":"quota exhausted","status":"RESOURCE_EXHAUSTED"}}`, tc.status)
		})
		_, err := collect(llm.Stream(context.Background(), question))
		var pe *conversation.ProviderError
		if !errors.As(err, &pe) || pe.Provider != "gemini" || pe.Status != tc.status || pe.Retryable != tc.retryable {
			t.Errorf("status %d: err = %#v", tc.status, err)
			continue
		}
		if !strings.Contains(err.Error(), "quota exhausted") || strings.Contains(err.Error(), "test-key") {
			t.Errorf("status %d: message %q", tc.status, err.Error())
		}
	}
}

func TestStreamCancellationIsNotAProviderError(t *testing.T) {
	llm := newLLM(t, gemini.Config{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\r\n\r\n", chunk("", "पहला"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold the stream open until the client goes away
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got error
	for _, err := range llm.Stream(ctx, question) {
		if err != nil {
			got = err
			break
		}
		cancel() // barge-in after the first delta
	}
	var pe *conversation.ProviderError
	if !errors.Is(got, context.Canceled) || errors.As(got, &pe) {
		t.Fatalf("err = %v, want plain context.Canceled", got)
	}
}

func TestStreamConsumerCanStopEarly(t *testing.T) {
	llm := newLLM(t, gemini.Config{}, sse(t, nil, chunk("", "एक"), chunk("", "दो"), chunk("STOP", "तीन")))
	n := 0
	for range llm.Stream(context.Background(), question) {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("consumed %d deltas", n)
	}
}

func TestComplete(t *testing.T) {
	llm := newLLM(t, gemini.Config{}, sse(t, nil, chunk("", "Learner asked "), chunk("STOP", "about fractions. ")))
	got, err := llm.Complete(context.Background(), question)
	if err != nil || got != "Learner asked about fractions." {
		t.Fatalf("Complete = %q, %v", got, err)
	}

	blocked := newLLM(t, gemini.Config{}, sse(t, nil, `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`))
	_, err = blocked.Complete(context.Background(), question)
	var pe *conversation.ProviderError
	if !errors.Is(err, conversation.ErrResponseBlocked) || !errors.As(err, &pe) || pe.Retryable {
		t.Fatalf("blocked Complete err = %v", err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	ctx := context.Background()
	for name, cfg := range map[string]gemini.Config{
		"no key":    {Model: "m"},
		"no model":  {APIKey: "k"},
		"bad level": {APIKey: "k", Model: "m", ThinkingLevel: "extreme"},
	} {
		if _, err := gemini.New(ctx, cfg); err == nil {
			t.Errorf("%s: New accepted %+v", name, cfg)
		}
	}
	if _, err := gemini.New(ctx, gemini.Config{APIKey: "k", Model: "m", ThinkingLevel: "HIGH", HTTPClient: &http.Client{Timeout: time.Second}}); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
