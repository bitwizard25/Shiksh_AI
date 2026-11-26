package sarvam_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/sarvam"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

var _ gateway.SpeechProvider = (*sarvam.Client)(nil)

func TestTranscribeAndSynthesize(t *testing.T) {
	t.Parallel()

	samplePCM := make([]byte, 4410)
	sampleWAV := audio.EncodeWAV(samplePCM, 22050, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-subscription-key") != "secret-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/speech-to-text":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if got := r.FormValue("language_code"); got != "hi-IN" {
				http.Error(w, "bad lang "+got, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"transcript": "नमस्ते दुनिया"})
		case "/text-to-speech":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req["target_language_code"] != "hi-IN" {
				http.Error(w, "bad lang", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string][]string{
				"audios": {base64.StdEncoding.EncodeToString(sampleWAV)},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := sarvam.New(sarvam.Config{
		APIKey: "secret-key", BaseURL: srv.URL, Languages: []string{"hi", "en"}, HTTPClient: srv.Client(),
	})

	if c.Name() != "sarvam" || !c.Available("hi") || c.Available("ta") {
		t.Fatalf("unexpected metadata/availability")
	}

	asrRes, err := c.Transcribe(context.Background(), conversation.ASRRequest{
		Lang: "hi", PCM: make([]byte, 3200), SampleRate: 16000,
	})
	if err != nil || asrRes.Text != "नमस्ते दुनिया" {
		t.Fatalf("Transcribe = (%q, %v)", asrRes.Text, err)
	}

	ttsRes, err := c.Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "नमस्ते"})
	if err != nil || ttsRes.SampleRate != 22050 || len(ttsRes.PCM) != len(samplePCM) {
		t.Fatalf("Synthesize = (%+v, %v)", ttsRes, err)
	}

	_, err = c.Transcribe(context.Background(), conversation.ASRRequest{Lang: "ta", PCM: make([]byte, 320), SampleRate: 16000})
	if !errors.Is(err, conversation.ErrLanguageUnsupported) {
		t.Fatalf("want ErrLanguageUnsupported, got %v", err)
	}
}

func TestRetryableAndRedactedErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited for super-secret-token"))
	}))
	defer srv.Close()

	c := sarvam.New(sarvam.Config{APIKey: "super-secret-token", BaseURL: srv.URL, HTTPClient: srv.Client()})
	_, err := c.Synthesize(context.Background(), conversation.TTSRequest{Lang: "en", Text: "hello"})
	if err == nil || !conversation.IsRetryable(err) || strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("unexpected error: %v", err)
	}
}
