package bhashini

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"unicode"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// Transcribe sends 16-bit mono PCM to Bhashini ASR as a WAV file. A transcript made only of
// whitespace or punctuation (silence, noise) comes back as an empty Text.
func (c *Client) Transcribe(ctx context.Context, req conversation.ASRRequest) (conversation.ASRResult, error) {
	content := base64.StdEncoding.EncodeToString(audio.EncodeWAV(req.PCM, req.SampleRate, 1))
	var resp computeResponse
	err := c.compute(ctx, taskASR, req.Lang, func(svc service) computeRequest {
		return computeRequest{
			PipelineTasks: []pipelineTask{{TaskType: taskASR, Config: taskConfig{
				Language: language{SourceLanguage: req.Lang}, ServiceID: svc.ServiceID, AudioFormat: "wav", SamplingRate: req.SampleRate,
			}}},
			InputData: inputData{Input: []textInput{{}}, Audio: []audioInput{{AudioContent: content}}},
		}
	}, &resp)
	if err != nil {
		return conversation.ASRResult{}, err
	}
	if len(resp.PipelineResponse) == 0 {
		return conversation.ASRResult{}, malformed(taskASR, "response has no pipelineResponse")
	}
	text := ""
	if out := resp.PipelineResponse[0].Output; len(out) > 0 {
		text = out[0].Source
	}
	return conversation.ASRResult{Text: cleanTranscript(text)}, nil
}

func cleanTranscript(s string) string {
	s = strings.TrimSpace(s)
	if strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) }) == "" {
		return ""
	}
	return s
}

func malformed(op, msg string) error {
	return &conversation.ProviderError{Provider: provider, Op: op, Err: errors.New(msg)}
}
