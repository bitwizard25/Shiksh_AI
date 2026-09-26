package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/fake"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

func TestBuildFakeProviders(t *testing.T) {
	cfg, err := config.LoadProviderConfigFrom(map[string]string{"ENABLED_LANGUAGES": "hi,en"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	prov, err := BuildProviders(ctx, cfg, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if !prov.Availability.Available("hi") || prov.Availability.Available("ta") || prov.Warmer != nil {
		t.Fatalf("availability/warmer wrong: hi=%v ta=%v warmer=%v", prov.Availability.Available("hi"), prov.Availability.Available("ta"), prov.Warmer)
	}
	res, err := prov.TTS.Synthesize(ctx, conversation.TTSRequest{Lang: "hi", Text: "नमस्ते"})
	if err != nil || res.SampleRate != fake.SampleRate {
		t.Fatalf("TTS = %d Hz, %v", res.SampleRate, err)
	}
	if got, err := prov.ASR.Transcribe(ctx, conversation.ASRRequest{Lang: "hi", PCM: make([]byte, 3200), SampleRate: 16000}); err != nil || got.Text == "" {
		t.Fatalf("ASR = %+v, %v", got, err)
	}
	if got, err := prov.LLM.Complete(ctx, conversation.LLMRequest{Messages: []conversation.ChatMessage{{Role: conversation.RoleUser, Text: "q"}}}); err != nil || got == "" {
		t.Fatalf("LLM = %q, %v", got, err)
	}
}

func TestBuildRealProvidersWarmsBhashini(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PipelineTasks []struct {
				Config struct {
					Language struct{ SourceLanguage string } `json:"language"`
				} `json:"config"`
			} `json:"pipelineTasks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.PipelineTasks) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		lang := body.PipelineTasks[0].Config.Language.SourceLanguage
		fmt.Fprintf(w, `{"pipelineResponseConfig":[`+
			`{"taskType":"asr","config":[{"serviceId":"asr-%[1]s","language":{"sourceLanguage":%[1]q}}]},`+
			`{"taskType":"tts","config":[{"serviceId":"tts-%[1]s","language":{"sourceLanguage":%[1]q}}]}],`+
			`"pipelineInferenceAPIEndPoint":{"callbackUrl":"http://%[2]s/compute","inferenceApiKey":{"name":"Authorization","value":"k"}}}`, lang, r.Host)
	}))
	defer srv.Close()

	cfg, err := config.LoadProviderConfigFrom(map[string]string{
		"PROVIDERS": "real", "BHASHINI_USER_ID": "u", "BHASHINI_ULCA_API_KEY": "k", "GEMINI_API_KEY": "g",
		"BHASHINI_CONFIG_URL": srv.URL, "ENABLED_LANGUAGES": "hi,en",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov, err := BuildProviders(ctx, cfg, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if prov.ASR == nil || prov.TTS == nil || prov.LLM == nil || prov.Warmer == nil {
		t.Fatalf("providers = %+v", prov)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !prov.Availability.Available("hi") || !prov.Availability.Available("en") {
		if time.Now().After(deadline) {
			t.Fatal("languages never became available (Warm not started?)")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if prov.Availability.Available("ta") {
		t.Fatal("a language that was not enabled is available")
	}
}

func TestBuildProvidersRejectsUnknownMode(t *testing.T) {
	if _, err := BuildProviders(context.Background(), config.ProviderConfig{Mode: "magic"}, nil); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
