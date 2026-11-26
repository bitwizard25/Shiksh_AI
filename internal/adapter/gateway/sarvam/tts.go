package sarvam

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

type ttsRequest struct {
	Text               string `json:"text"`
	TargetLanguageCode string `json:"target_language_code"`
	Speaker            string `json:"speaker,omitempty"`
	Model              string `json:"model,omitempty"`
	SpeechSampleRate   int    `json:"speech_sample_rate,omitempty"`
	OutputAudioCodec   string `json:"output_audio_codec,omitempty"`
}

type ttsResponse struct {
	Audios []string `json:"audios"`
}

func (c *Client) Synthesize(ctx context.Context, req conversation.TTSRequest) (conversation.TTSResult, error) {
	langCode, err := c.resolveLang(req.Lang)
	if err != nil {
		return conversation.TTSResult{}, err
	}

	payload, err := json.Marshal(ttsRequest{
		Text:               req.Text,
		TargetLanguageCode: langCode,
		Speaker:            c.cfg.TTSSpeaker,
		Model:              c.cfg.TTSModel,
		SpeechSampleRate:   defaultSampleRate,
		OutputAudioCodec:   "wav",
	})
	if err != nil {
		return conversation.TTSResult{}, &conversation.ProviderError{Provider: provider, Op: "tts", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/text-to-speech", bytes.NewReader(payload))
	if err != nil {
		return conversation.TTSResult{}, &conversation.ProviderError{Provider: provider, Op: "tts", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-subscription-key", c.cfg.APIKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return conversation.TTSResult{}, c.transportError("tts", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return conversation.TTSResult{}, c.httpError("tts", resp.StatusCode, errBody)
	}

	var parsed ttsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return conversation.TTSResult{}, &conversation.ProviderError{
			Provider: provider, Op: "tts", Err: errors.New("malformed sarvam tts response: " + err.Error()),
		}
	}
	if len(parsed.Audios) == 0 || parsed.Audios[0] == "" {
		return conversation.TTSResult{}, &conversation.ProviderError{
			Provider: provider, Op: "tts", Err: errors.New("response has no audio"),
		}
	}

	raw, err := base64.StdEncoding.DecodeString(parsed.Audios[0])
	if err != nil {
		return conversation.TTSResult{}, &conversation.ProviderError{
			Provider: provider, Op: "tts", Err: errors.New("audio payload is not valid base64"),
		}
	}
	pcm, f, err := audio.DecodeWAV(raw)
	if err != nil {
		return conversation.TTSResult{}, &conversation.ProviderError{Provider: provider, Op: "tts", Err: err}
	}
	if f.Channels != 1 {
		return conversation.TTSResult{}, &conversation.ProviderError{
			Provider: provider, Op: "tts", Err: fmt.Errorf("tts audio has %d channels, want mono", f.Channels),
		}
	}
	return conversation.TTSResult{
		PCM:        pcm,
		SampleRate: f.SampleRate,
		DurationMs: audio.DurationMs(len(pcm), f.SampleRate, 1),
	}, nil
}
