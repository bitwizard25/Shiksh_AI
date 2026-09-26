package bhashini

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/audio"
)

// fakeServer imitates Bhashini's config endpoint (/config) and inference endpoint (/compute).
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	configCalls   int
	computeCalls  int
	configFail    int                 // upcoming config calls that answer 500
	configDelay   time.Duration       // delay before answering a config call
	tasks         map[string][]string // language -> tasks the pipeline offers
	authValue     string              // inference key handed out by the config call
	configHeaders http.Header
	configBody    configRequest
	compute       func(w http.ResponseWriter, r *http.Request, body computeRequest)
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, tasks: map[string][]string{"hi": {"asr", "tts"}, "en": {"asr", "tts"}}, authValue: "inference-key-1"}
	f.serveOK()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /config", f.handleConfig)
	mux.HandleFunc("POST /compute", func(w http.ResponseWriter, r *http.Request) {
		var body computeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode compute body: %v", err)
		}
		f.mu.Lock()
		f.computeCalls++
		handler := f.compute
		f.mu.Unlock()
		handler(w, r, body)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// serveOK answers ASR with "ok" and TTS with a short 22050 Hz WAV.
func (f *fakeServer) serveOK() {
	f.setCompute(func(w http.ResponseWriter, r *http.Request, body computeRequest) {
		switch body.PipelineTasks[0].TaskType {
		case taskASR:
			writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{TaskType: taskASR, Output: []textOutput{{Source: "ok"}}}}})
		default:
			wav := audio.EncodeWAV(make([]byte, 441*2), 22050, 1)
			writeJSON(w, computeResponse{PipelineResponse: []taskResponse{{TaskType: taskTTS, Audio: []audioInput{{AudioContent: b64(wav)}}}}})
		}
	})
}

func (f *fakeServer) setCompute(h func(w http.ResponseWriter, r *http.Request, body computeRequest)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compute = h
}

func (f *fakeServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	var body configRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decode config body: %v", err)
	}
	f.mu.Lock()
	f.configCalls++
	f.configHeaders, f.configBody = r.Header.Clone(), body
	fail := f.configFail > 0
	if fail {
		f.configFail--
	}
	delay, auth := f.configDelay, f.authValue
	offered := make(map[string][]string, len(f.tasks))
	for k, v := range f.tasks {
		offered[k] = slices.Clone(v)
	}
	f.mu.Unlock()

	time.Sleep(delay)
	if fail {
		http.Error(w, "temporarily broken", http.StatusInternalServerError)
		return
	}
	resp := configResponse{PipelineInferenceAPIEndPoint: inferenceEndpoint{
		CallbackURL:     f.srv.URL + "/compute",
		InferenceAPIKey: apiKey{Name: "Authorization", Value: auth},
	}}
	for _, task := range body.PipelineTasks {
		lang := task.Config.Language.SourceLanguage
		if slices.Contains(offered[lang], task.TaskType) {
			resp.PipelineResponseConfig = append(resp.PipelineResponseConfig, taskServices{
				TaskType: task.TaskType,
				Config:   []serviceConfig{{ServiceID: task.TaskType + "-" + lang, Language: language{SourceLanguage: lang}}},
			})
		}
	}
	writeJSON(w, resp)
}

func (f *fakeServer) client() *Client {
	return New(Config{
		UserID: "user-1", ULCAKey: "ulca-key", PipelineID: "pipe-1",
		ConfigURL: f.srv.URL + "/config", HTTPClient: f.srv.Client(), RetryBackoff: 10 * time.Millisecond,
	})
}

// clientWithLogger is like client but logs to log, for tests that assert on Warm's log output.
func (f *fakeServer) clientWithLogger(log *slog.Logger) *Client {
	return New(Config{
		UserID: "user-1", ULCAKey: "ulca-key", PipelineID: "pipe-1",
		ConfigURL: f.srv.URL + "/config", HTTPClient: f.srv.Client(), RetryBackoff: 10 * time.Millisecond,
		Logger: log,
	})
}

// syncBuffer is a mutex-guarded io.Writer: slog handlers may be written to from Warm's background
// goroutines while a test concurrently reads the buffer's contents.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (f *fakeServer) counts() (config, compute int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configCalls, f.computeCalls
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
