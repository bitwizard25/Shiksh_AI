package bhashini

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestTranscribeSendsWAVAndReturnsTranscript(t *testing.T) {
	f := newFakeServer(t)
	pcm := []byte{1, 0, 2, 0, 3, 0}
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		if got := r.Header.Get("Authorization"); got != "inference-key-1" {
			t.Errorf("inference auth header = %q", got)
		}
		task := body.PipelineTasks[0]
		if task.TaskType != taskASR || task.Config.ServiceID != "asr-hi" || task.Config.AudioFormat != "wav" ||
			task.Config.SamplingRate != 16000 || task.Config.Language.SourceLanguage != "hi" {
			t.Errorf("task = %+v", task)
		}
		raw, err := base64.StdEncoding.DecodeString(body.InputData.Audio[0].AudioContent)
		if err != nil {
			t.Errorf("audioContent is not base64: %v", err) // never t.Fatal outside the test goroutine
			return
		}
		got, format, err := audio.DecodeWAV(raw)
		if err != nil || !bytes.Equal(got, pcm) || format.SampleRate != 16000 || format.Channels != 1 {
			t.Errorf("uploaded audio = %v %+v %v", got, format, err)
		}
		writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{TaskType: taskASR, Output: []textOutput{{Source: "  नमस्ते दुनिया  "}}}}})
	})

	res, err := f.client().Transcribe(context.Background(), conversation.ASRRequest{Lang: "hi", PCM: pcm, SampleRate: 16000})
	if err != nil || res.Text != "नमस्ते दुनिया" {
		t.Fatalf("Transcribe = %+v, %v", res, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.configHeaders.Get("userID") != "user-1" || f.configHeaders.Get("ulcaApiKey") != "ulca-key" {
		t.Errorf("config headers = %v", f.configHeaders)
	}
	if f.configBody.PipelineRequestConfig.PipelineID != "pipe-1" || len(f.configBody.PipelineTasks) != 2 {
		t.Errorf("config body = %+v", f.configBody)
	}
}

func TestPunctuationOnlyTranscriptIsEmpty(t *testing.T) {
	for _, heard := range []string{" । ", "...", "", "  \n"} {
		f := newFakeServer(t)
		f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
			writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{TaskType: taskASR, Output: []textOutput{{Source: heard}}}}})
		})
		res, err := f.client().Transcribe(context.Background(), conversation.ASRRequest{Lang: "hi", PCM: []byte{0, 0}, SampleRate: 16000})
		if err != nil || res.Text != "" {
			t.Errorf("heard %q -> %+v, %v; want empty text", heard, res, err)
		}
	}
}

func TestConfigIsCachedAcrossCallsAndTasks(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	ctx := context.Background()
	for range 2 {
		if _, err := c.Transcribe(ctx, conversation.ASRRequest{Lang: "hi", PCM: []byte{0, 0}, SampleRate: 16000}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Synthesize(ctx, conversation.TTSRequest{Lang: "hi", Text: "नमस्ते"}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := f.counts(); cfg != 1 {
		t.Fatalf("config calls = %d, want 1 (cached)", cfg)
	}
	if got := c.Endpoints(); len(got) != 1 || got[0] != f.srv.URL+"/compute" {
		t.Fatalf("Endpoints = %v", got)
	}
}

func TestConcurrentFirstCallsShareOneConfigFetch(t *testing.T) {
	f := newFakeServer(t)
	f.configDelay = 100 * time.Millisecond // every caller joins the one in-flight fetch
	c := f.client()
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Transcribe(context.Background(), conversation.ASRRequest{Lang: "hi", PCM: []byte{0, 0}, SampleRate: 16000}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if cfg, _ := f.counts(); cfg != 1 {
		t.Fatalf("config calls = %d, want 1 (single flight)", cfg)
	}
}

func TestSynthesizeDecodesWAVAtItsOwnRate(t *testing.T) {
	f := newFakeServer(t)
	pcm := make([]byte, 2400*2) // 100 ms at 24 kHz
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		task := body.PipelineTasks[0]
		if task.TaskType != taskTTS || task.Config.ServiceID != "tts-hi" || task.Config.Gender != "female" || task.Config.AudioFormat != "wav" {
			t.Errorf("task = %+v", task)
		}
		if src := body.InputData.Input[0].Source; src == nil || *src != "नमस्ते" {
			t.Errorf("input = %+v", body.InputData.Input)
		}
		writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{TaskType: taskTTS, Audio: []audioInput{{AudioContent: b64(audio.EncodeWAV(pcm, 24000, 1))}}}}})
	})
	res, err := f.client().Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "नमस्ते"})
	if err != nil || res.SampleRate != 24000 || !bytes.Equal(res.PCM, pcm) || res.DurationMs != 100 {
		t.Fatalf("Synthesize = rate %d, %d bytes, %d ms, %v", res.SampleRate, len(res.PCM), res.DurationMs, err)
	}
}

func TestSynthesizeRawPCMUsesConfigRate(t *testing.T) {
	f := newFakeServer(t)
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{
			TaskType: taskTTS, Audio: []audioInput{{AudioContent: b64(make([]byte, 441*2+1))}}, Config: &audioConfig{SamplingRate: 22050},
		}}})
	})
	res, err := f.client().Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "x"})
	if err != nil || res.SampleRate != 22050 || len(res.PCM) != 441*2 {
		t.Fatalf("Synthesize = rate %d, %d bytes, %v", res.SampleRate, len(res.PCM), err)
	}
}

func TestSynthesizeRawPCMWithoutRateFails(t *testing.T) {
	f := newFakeServer(t)
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{TaskType: taskTTS, Audio: []audioInput{{AudioContent: b64([]byte{1, 2, 3, 4})}}}}})
	})
	if _, err := f.client().Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "x"}); err == nil {
		t.Fatal("raw PCM without a sampling rate was accepted")
	}
}

func TestSynthesizeRejectsUnsupportedRawAudioFormat(t *testing.T) {
	f := newFakeServer(t)
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{
			TaskType: taskTTS, Audio: []audioInput{{AudioContent: b64([]byte{1, 2, 3, 4})}}, Config: &audioConfig{AudioFormat: "mp3", SamplingRate: 22050},
		}}})
	})
	if _, err := f.client().Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "x"}); err == nil {
		t.Fatal("mp3-labelled raw audio was accepted")
	}
}

func TestUnauthorizedComputeRefreshesConfigOnce(t *testing.T) {
	f := newFakeServer(t)
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		if r.Header.Get("Authorization") == "inference-key-1" {
			f.mu.Lock()
			f.authValue = "inference-key-2" // the key rotated; the next config call hands out the new one
			f.mu.Unlock()
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{TaskType: taskASR, Output: []textOutput{{Source: "ok"}}}}})
	})
	res, err := f.client().Transcribe(context.Background(), conversation.ASRRequest{Lang: "hi", PCM: []byte{0, 0}, SampleRate: 16000})
	if err != nil || res.Text != "ok" {
		t.Fatalf("Transcribe = %+v, %v", res, err)
	}
	if cfg, compute := f.counts(); cfg != 2 || compute != 2 {
		t.Fatalf("config=%d compute=%d, want 2 and 2", cfg, compute)
	}

	g := newFakeServer(t)
	g.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		http.Error(w, "nope", http.StatusForbidden)
	})
	_, err = g.client().Transcribe(context.Background(), conversation.ASRRequest{Lang: "hi", PCM: []byte{0, 0}, SampleRate: 16000})
	var pe *conversation.ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusForbidden {
		t.Fatalf("persistent 403: err = %v", err)
	}
	if _, compute := g.counts(); compute != 2 {
		t.Fatalf("compute calls = %d, want exactly 2 (one retry)", compute)
	}
}

func TestComputeErrorMapping(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
	}{{http.StatusServiceUnavailable, true}, {http.StatusTooManyRequests, true}, {http.StatusBadRequest, false}}
	for _, tc := range cases {
		f := newFakeServer(t)
		f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
			http.Error(w, "model overloaded", tc.status)
		})
		_, err := f.client().Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "x"})
		var pe *conversation.ProviderError
		if !errors.As(err, &pe) || pe.Status != tc.status || pe.Retryable != tc.retryable || !strings.Contains(err.Error(), "model overloaded") {
			t.Errorf("status %d: err = %v", tc.status, err)
		}
		if strings.Contains(err.Error(), "inference-key-1") || strings.Contains(err.Error(), "ulca-key") {
			t.Errorf("status %d: error leaks a secret: %v", tc.status, err)
		}
	}
}

func TestComputeErrorBodyRedactsInferenceKey(t *testing.T) {
	f := newFakeServer(t)
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		http.Error(w, "bad request, saw header value inference-key-1", http.StatusBadRequest)
	})
	_, err := f.client().Synthesize(context.Background(), conversation.TTSRequest{Lang: "hi", Text: "x"})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "inference-key-1") {
		t.Fatalf("err leaks the inference key: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("err does not mark the redaction: %v", err)
	}
}

func TestUnsupportedLanguage(t *testing.T) {
	f := newFakeServer(t)
	f.tasks["xx"] = []string{"asr"} // ASR only, no TTS
	c := f.client()
	_, err := c.Synthesize(context.Background(), conversation.TTSRequest{Lang: "xx", Text: "x"})
	if !errors.Is(err, conversation.ErrLanguageUnsupported) {
		t.Fatalf("err = %v, want ErrLanguageUnsupported", err)
	}
	if c.Available("xx") {
		t.Fatal("a language without TTS is reported available")
	}
}

func TestStaleWhileRevalidate(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	var clock atomic.Int64 // unix nanoseconds
	c.now = func() time.Time { return time.Unix(0, clock.Load()) }
	fetchedAt := func() time.Time {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.cache[cacheKey{taskASR, "hi"}].fetchedAt
	}
	idle := func() bool { // no background refresh in flight
		c.mu.RLock()
		defer c.mu.RUnlock()
		return !c.refreshing["hi"]
	}
	transcribe := func() {
		t.Helper()
		if _, err := c.Transcribe(context.Background(), conversation.ASRRequest{Lang: "hi", PCM: []byte{0, 0}, SampleRate: 16000}); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
	}

	transcribe() // fetches at t=0
	clock.Store(int64(5 * time.Hour))
	transcribe() // served from cache; past 80 % of 6 h, so a background refresh starts
	eventually(t, "background refresh stored and finished", func() bool {
		return fetchedAt().Equal(time.Unix(0, int64(5*time.Hour))) && idle()
	})

	f.mu.Lock()
	f.configFail = 100 // every refresh from now on fails
	f.mu.Unlock()
	clock.Store(int64(10 * time.Hour))
	transcribe() // stale entry still serves while the refresh fails
	eventually(t, "failed refresh attempted and finished", func() bool { cfg, _ := f.counts(); return cfg == 3 && idle() })
	transcribe() // the stale entry is still served after a failed refresh
	if got := fetchedAt(); !got.Equal(time.Unix(0, int64(5*time.Hour))) {
		t.Fatalf("fetchedAt = %v; a failed refresh must keep the stale entry", got)
	}
}

func TestWarmRetriesTransientFailures(t *testing.T) {
	f := newFakeServer(t)
	f.configFail = 2
	c := f.client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Warm(ctx, []string{"hi"})
	eventually(t, "hi available", func() bool { return c.Available("hi") })
	if cfg, _ := f.counts(); cfg != 3 {
		t.Fatalf("config calls = %d, want 3 (two failures, one success)", cfg)
	}
}

func TestWarmStopsForUnsupportedLanguage(t *testing.T) {
	f := newFakeServer(t)
	f.tasks["xx"] = []string{"asr"}
	c := f.client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Warm(ctx, []string{"xx"})
	eventually(t, "config called", func() bool { cfg, _ := f.counts(); return cfg >= 1 })
	time.Sleep(60 * time.Millisecond) // six retry intervals
	if cfg, _ := f.counts(); cfg != 1 || c.Available("xx") {
		t.Fatalf("config calls = %d, available = %v; want 1 call and unavailable", cfg, c.Available("xx"))
	}
}

func TestWarmLogsRetryWarnings(t *testing.T) {
	f := newFakeServer(t)
	f.configFail = 2
	var buf syncBuffer
	c := f.clientWithLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Warm(ctx, []string{"hi"})
	eventually(t, "hi available", func() bool { return c.Available("hi") })

	out := buf.String()
	warnLines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "lang=hi") {
			warnLines++
		}
	}
	if warnLines != 2 {
		t.Fatalf("warn lines with lang=hi = %d, want 2\n%s", warnLines, out)
	}
	if strings.Contains(out, "ulca-key") || strings.Contains(out, "inference-key-1") {
		t.Fatalf("Warm's log leaks a secret:\n%s", out)
	}
}

func TestWarmLogsUnavailableLanguageInfo(t *testing.T) {
	f := newFakeServer(t)
	f.tasks["xx"] = []string{"asr"} // ASR only, no TTS
	var buf syncBuffer
	c := f.clientWithLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Warm(ctx, []string{"xx"})
	eventually(t, "xx logged", func() bool { return strings.Contains(buf.String(), "lang=xx") })

	out := buf.String()
	infoLines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "level=INFO") && strings.Contains(line, "lang=xx") {
			infoLines++
		}
	}
	if infoLines != 1 {
		t.Fatalf("info lines with lang=xx = %d, want 1\n%s", infoLines, out)
	}
}

func TestCanceledCallIsNotAProviderError(t *testing.T) {
	f := newFakeServer(t)
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		<-r.Context().Done() // hang until the client gives up
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := f.client().Transcribe(ctx, conversation.ASRRequest{Lang: "hi", PCM: []byte{0, 0}, SampleRate: 16000})
	var pe *conversation.ProviderError
	if !errors.Is(err, context.Canceled) || errors.As(err, &pe) {
		t.Fatalf("err = %v, want plain context.Canceled", err)
	}
}
