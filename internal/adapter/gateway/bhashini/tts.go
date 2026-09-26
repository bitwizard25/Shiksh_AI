package bhashini

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// Synthesize converts text to speech. The audio's sample rate comes from the returned WAV header,
// or from the response's samplingRate when Bhashini sends raw PCM; it is never assumed.
func (c *Client) Synthesize(ctx context.Context, req conversation.TTSRequest) (conversation.TTSResult, error) {
	gender := req.Gender
	if gender == "" {
		gender = "female"
	}
	text := req.Text
	var resp computeResponse
	err := c.compute(ctx, taskTTS, req.Lang, func(svc service) computeRequest {
		return computeRequest{
			PipelineTasks: []pipelineTask{{TaskType: taskTTS, Config: taskConfig{
				Language: language{SourceLanguage: req.Lang}, ServiceID: svc.ServiceID, Gender: gender, AudioFormat: "wav",
			}}},
			InputData: inputData{Input: []textInput{{Source: &text}}},
		}
	}, &resp)
	if err != nil {
		return conversation.TTSResult{}, err
	}
	if len(resp.PipelineResponse) == 0 || len(resp.PipelineResponse[0].Audio) == 0 {
		return conversation.TTSResult{}, malformed(taskTTS, "response has no audio")
	}
	raw, err := base64.StdEncoding.DecodeString(resp.PipelineResponse[0].Audio[0].AudioContent)
	if err != nil {
		return conversation.TTSResult{}, malformed(taskTTS, "audioContent is not base64")
	}
	pcm, rate, err := decodeTTSAudio(raw, resp.PipelineResponse[0].Config)
	if err != nil {
		return conversation.TTSResult{}, &conversation.ProviderError{Provider: provider, Op: taskTTS, Err: err}
	}
	return conversation.TTSResult{PCM: pcm, SampleRate: rate, DurationMs: audio.DurationMs(len(pcm), rate, 1)}, nil
}

func decodeTTSAudio(raw []byte, cfg *audioConfig) ([]byte, int, error) {
	pcm, f, err := audio.DecodeWAV(raw)
	if err == nil {
		if f.Channels != 1 {
			return nil, 0, fmt.Errorf("tts audio has %d channels, want mono", f.Channels)
		}
		return pcm, f.SampleRate, nil
	}
	if !errors.Is(err, audio.ErrNotWAV) {
		return nil, 0, err
	}
	if cfg == nil || cfg.SamplingRate <= 0 {
		return nil, 0, errors.New("tts audio is raw PCM without a sampling rate")
	}
	if format := cfg.AudioFormat; format != "" && !strings.EqualFold(format, "wav") && !strings.EqualFold(format, "pcm") {
		return nil, 0, fmt.Errorf("tts audio format %q is not supported", format)
	}
	return raw[:len(raw)&^1], cfg.SamplingRate, nil
}
