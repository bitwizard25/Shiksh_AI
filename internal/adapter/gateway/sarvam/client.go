// Package sarvam implements the ASR, TTS, and LanguageAvailability ports on Sarvam AI's REST API
// (Saaras STT + Bulbul TTS).
package sarvam

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

const (
	provider          = "sarvam"
	defaultBaseURL    = "https://api.sarvam.ai"
	defaultSTTModel   = "saaras:v4"
	defaultTTSModel   = "bulbul:v4-flash"
	defaultTTSSpeaker = "shubh_enhi_ads"
	defaultSampleRate = 22050
	maxErrorBodyBytes = 4096
)

var bcp47 = map[string]string{
	"hi": "hi-IN",
	"mr": "mr-IN",
	"bn": "bn-IN",
	"ta": "ta-IN",
	"te": "te-IN",
	"gu": "gu-IN",
	"kn": "kn-IN",
	"ml": "ml-IN",
	"en": "en-IN",
}

// Config configures the Sarvam AI speech client.
type Config struct {
	APIKey     string
	BaseURL    string
	STTModel   string
	TTSModel   string
	TTSSpeaker string
	Languages  []string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Client calls Sarvam AI's Speech-to-Text and Text-to-Speech APIs.
type Client struct {
	cfg     Config
	baseURL string
	client  *http.Client
	log     *slog.Logger
	langs   map[string]bool
}

// New constructs a Sarvam AI speech provider client.
func New(cfg Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	if strings.TrimSpace(cfg.STTModel) == "" {
		cfg.STTModel = defaultSTTModel
	}
	if strings.TrimSpace(cfg.TTSModel) == "" {
		cfg.TTSModel = defaultTTSModel
	}
	if strings.TrimSpace(cfg.TTSSpeaker) == "" {
		cfg.TTSSpeaker = defaultTTSSpeaker
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	langs := make(map[string]bool, len(bcp47))
	if len(cfg.Languages) == 0 {
		for code := range bcp47 {
			langs[code] = true
		}
	} else {
		for _, code := range cfg.Languages {
			c := strings.ToLower(strings.TrimSpace(code))
			if _, ok := bcp47[c]; ok {
				langs[c] = true
			}
		}
	}
	return &Client{cfg: cfg, baseURL: base, client: hc, log: log, langs: langs}
}

func (c *Client) Name() string                      { return provider }
func (c *Client) Warm(_ context.Context, _ []string) {}
func (c *Client) Endpoints() []string               { return []string{c.baseURL} }

func (c *Client) Available(lang string) bool {
	return c.langs[strings.ToLower(strings.TrimSpace(lang))]
}

func (c *Client) resolveLang(lang string) (string, error) {
	code := strings.ToLower(strings.TrimSpace(lang))
	if !c.langs[code] {
		return "", fmt.Errorf("%w: %s", conversation.ErrLanguageUnsupported, lang)
	}
	return bcp47[code], nil
}

func (c *Client) httpError(op string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if c.cfg.APIKey != "" {
		msg = strings.ReplaceAll(msg, c.cfg.APIKey, "[REDACTED]")
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &conversation.ProviderError{
		Provider:  provider,
		Op:        op,
		Status:    status,
		Retryable: status == http.StatusTooManyRequests || status >= 500,
		Err:       errors.New(msg),
	}
}

func (c *Client) transportError(op string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if c.cfg.APIKey != "" {
		msg = strings.ReplaceAll(msg, c.cfg.APIKey, "[REDACTED]")
	}
	return &conversation.ProviderError{
		Provider:  provider,
		Op:        op,
		Retryable: !errors.Is(err, context.Canceled),
		Err:       errors.New(msg),
	}
}
