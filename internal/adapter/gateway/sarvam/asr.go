package sarvam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

type sttResponse struct {
	Transcript string `json:"transcript"`
}

func (c *Client) Transcribe(ctx context.Context, req conversation.ASRRequest) (conversation.ASRResult, error) {
	langCode, err := c.resolveLang(req.Lang)
	if err != nil {
		return conversation.ASRResult{}, err
	}

	wav := audio.EncodeWAV(req.PCM, req.SampleRate, 1)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return conversation.ASRResult{}, &conversation.ProviderError{Provider: provider, Op: "asr", Err: err}
	}
	if _, err := fw.Write(wav); err != nil {
		return conversation.ASRResult{}, &conversation.ProviderError{Provider: provider, Op: "asr", Err: err}
	}
	if err := mw.WriteField("model", c.cfg.STTModel); err != nil {
		return conversation.ASRResult{}, &conversation.ProviderError{Provider: provider, Op: "asr", Err: err}
	}
	if err := mw.WriteField("language_code", langCode); err != nil {
		return conversation.ASRResult{}, &conversation.ProviderError{Provider: provider, Op: "asr", Err: err}
	}
	if err := mw.Close(); err != nil {
		return conversation.ASRResult{}, &conversation.ProviderError{Provider: provider, Op: "asr", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/speech-to-text", &body)
	if err != nil {
		return conversation.ASRResult{}, &conversation.ProviderError{Provider: provider, Op: "asr", Err: err}
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("api-subscription-key", c.cfg.APIKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return conversation.ASRResult{}, c.transportError("asr", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return conversation.ASRResult{}, c.httpError("asr", resp.StatusCode, errBody)
	}

	var parsed sttResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return conversation.ASRResult{}, &conversation.ProviderError{
			Provider: provider, Op: "asr", Err: errors.New("malformed sarvam stt response: " + err.Error()),
		}
	}
	return conversation.ASRResult{Text: cleanTranscript(parsed.Transcript)}, nil
}

func cleanTranscript(s string) string {
	s = strings.TrimSpace(s)
	if strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) }) == "" {
		return ""
	}
	return s
}
