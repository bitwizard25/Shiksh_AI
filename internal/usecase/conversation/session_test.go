package conversation

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// endSession ends the session as the learner and returns how it closed.
func (h *harness) endSession() CloseInfo {
	h.t.Helper()
	h.msg(MsgSessionEnd)
	return h.close(10 * time.Second)
}

func TestVoiceTurnHappyPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.utterance(1000)
		time.Sleep(5 * time.Second)

		want := "ready turn.started transcript seg0 audio1 seg1 audio1 seg2 audio1 done:complete"
		if got := joinTrace(h.out.names()); got != want {
			t.Fatalf("trace:\n got %s\nwant %s", got, want)
		}
		segs := h.out.segments()
		if len(segs) != 3 || segs[0].Text != "First sentence is here." || segs[2].Text != "Third one ends it." || segs[0].SampleRate != ttsRate {
			t.Fatalf("segments = %+v", segs)
		}
		frames := h.out.find(func(e Event) bool { f, ok := e.(AudioFrame); return ok && f.Kind == KindSpeech })
		if len(frames) != 6 { // 3 segments x 2 frames of 200 ms
			t.Fatalf("got %d frames, want 6", len(frames))
		}
		last := frames[5].(AudioFrame)
		if !last.LastOfSeg || !last.LastOfTurn || last.ChunkSeq != 2 || last.TurnID != 1 {
			t.Fatalf("last frame = %+v", last)
		}
		if first := frames[0].(AudioFrame); first.LastOfSeg || first.LastOfTurn || first.Part != 0 || frames[1].(AudioFrame).Part != 1 {
			t.Fatalf("first frames = %+v %+v", first, frames[1])
		}

		lat := h.out.done()[0].Latency
		// ASR 200 ms, first token +300 ms (the first sentence is complete in it), TTS 100 ms.
		if lat.ASRMs != 200 || lat.LLMTTFTMs != 500 || lat.FirstSegmentMs != 500 || lat.FirstTTSMs != 600 || lat.FirstAudioMs != 600 || lat.TotalMs < lat.FirstAudioMs {
			t.Fatalf("latency = %+v", lat)
		}

		if info := h.endSession(); info.Code != CloseNormal {
			t.Fatalf("close = %+v", info)
		}
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 {
			t.Fatalf("inserts = %d", len(inserts))
		}
		in := inserts[0]
		if in.TurnNo != 1 || in.Learner.Content != "what is a fraction" || in.Learner.InputMode != entity.InputVoice || *in.Learner.AudioMs != 1000 ||
			in.Tutor.Content != "First sentence is here. Second sentence follows. Third one ends it." || in.Tutor.Status != entity.MessageComplete || len(in.Tutor.Latency) == 0 {
			t.Fatalf("insert = %+v", in)
		}
		if got := h.db.ends(); !slices.Equal(got, []string{entity.EndLearner}) || len(h.ended) != 1 {
			t.Fatalf("ends = %v, summaries queued = %d", got, len(h.ended))
		}
	})
}

func TestTextTurnSkipsASR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.text("  why is the sky blue?  ")
		time.Sleep(5 * time.Second)
		if len(h.asr.requests()) != 0 {
			t.Fatal("ASR was called for typed text")
		}
		if got := joinTrace(h.out.names()); !strings.HasPrefix(got, "ready turn.started transcript seg0") || !strings.HasSuffix(got, "done:complete") {
			t.Fatalf("trace = %s", got)
		}
		req := h.llm.requests()[0]
		if last := req.Messages[len(req.Messages)-1]; last.Role != usecase.ChatUser || last.Text != "why is the sky blue?" {
			t.Fatalf("last message = %+v", last)
		}
		if !strings.Contains(req.System, "Mathematics") {
			t.Fatal("system prompt does not mention the subject")
		}
		h.endSession()
		if inserts, _ := h.store.snapshot(); len(inserts) != 1 || inserts[0].Learner.InputMode != entity.InputText || inserts[0].Learner.AudioMs != nil {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestTextInputValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.text("   ")
		h.text(strings.Repeat("ह", 1001))
		synctest.Wait()
		if got := h.out.names(); !slices.Equal(got, []string{"ready", "error:bad_message", "error:bad_message"}) {
			t.Fatalf("trace = %v", got)
		}
		h.s.Message(ClientMsg{Type: "bogus"})
		synctest.Wait()
		if n := len(h.out.names()); n != 4 {
			t.Fatalf("unknown type produced %d events", n)
		}
		h.endSession()
	})
}

func TestEmptyTranscriptPlaysRepeatClipWithoutLLM(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.asr.text = "   "
		h.start(t.Context())
		h.utterance(800)
		time.Sleep(3 * time.Second)
		if got := joinTrace(h.out.names()); got != "ready turn.started transcript.empty audio3 done:complete" {
			t.Fatalf("trace = %s", got)
		}
		if len(h.llm.requests()) != 0 {
			t.Fatal("LLM was called for an empty transcript")
		}
		h.endSession()
		if inserts, _ := h.store.snapshot(); len(inserts) != 0 {
			t.Fatalf("an empty turn was stored: %+v", inserts)
		}
	})
}

func TestShortUtteranceIsDiscarded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.utterance(100) // under MIN_UTTERANCE (250 ms)
		synctest.Wait()
		if got := h.out.names(); !slices.Equal(got, []string{"ready"}) {
			t.Fatalf("trace = %v", got)
		}
		h.endSession()
	})
}

func TestSpeechCancelDiscardsAudio(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.msg(MsgSpeechStart)
		h.audio(1000)
		h.msg(MsgSpeechCancel)
		h.msg(MsgSpeechEnd) // ignored: nothing is being captured
		synctest.Wait()
		if got := h.out.names(); !slices.Equal(got, []string{"ready"}) {
			t.Fatalf("trace = %v", got)
		}
		h.endSession()
	})
}

func TestAudioOutsideCaptureIsDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.audio(500)
		synctest.Wait()
		if h.s.droppedAud.Load() != 5 {
			t.Fatalf("dropped %d frames, want 5", h.s.droppedAud.Load())
		}
		h.endSession()
	})
}

func TestOverlongUtteranceAutoFinalizes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.MaxUtterance = time.Second })
		h.start(t.Context())
		h.msg(MsgSpeechStart)
		for range 15 {
			h.s.Audio(make([]byte, 3000)) // 93.75 ms frames, so the limit falls inside a frame
		}
		time.Sleep(5 * time.Second)
		trace := h.out.names()
		if !contains(trace, "error:utterance_too_long", "turn.started", "transcript") {
			t.Fatalf("trace = %v", trace)
		}
		reqs := h.asr.requests()
		if len(reqs) != 1 || len(reqs[0].PCM) != InputSampleRate*bytesPerSample { // exactly 1 s reached the ASR
			t.Fatalf("ASR requests = %d, bytes %d", len(reqs), len(reqs[0].PCM))
		}
		h.endSession()
	})
}

func TestASRFailureSendsErrorAndClip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.asr.err = &ProviderError{Provider: "bhashini", Op: "asr", Retryable: false, Err: errBoom}
		h.start(t.Context())
		h.utterance(800)
		time.Sleep(3 * time.Second)
		if got := joinTrace(h.out.names()); got != "ready turn.started error:asr_failed audio3 done:failed" {
			t.Fatalf("trace = %s", got)
		}
		if n := len(h.asr.requests()); n != 1 {
			t.Fatalf("non-retryable failure was tried %d times", n)
		}
		h.endSession()
		if inserts, _ := h.store.snapshot(); len(inserts) != 0 {
			t.Fatalf("a failed recognition was stored: %+v", inserts)
		}
	})
}

func TestASRRetriesOnceWhenRetryableAndFast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.asr.err = &ProviderError{Provider: "bhashini", Op: "asr", Retryable: true, Err: errBoom}
		h.asr.delay = 100 * time.Millisecond
		h.start(t.Context())
		h.utterance(800)
		time.Sleep(3 * time.Second)
		if n := len(h.asr.requests()); n != 2 {
			t.Fatalf("ASR tried %d times, want 2", n)
		}
		if !contains(h.out.names(), "error:asr_failed") {
			t.Fatalf("trace = %v", h.out.names())
		}
		h.endSession()
	})
}

func TestTurnRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.TurnBurst = 2; c.TurnRatePerMin = 1 })
		h.start(t.Context())
		for range 3 {
			h.text("hello there")
			time.Sleep(3 * time.Second)
		}
		var limited int
		for _, n := range h.out.names() {
			if n == "error:rate_limited" {
				limited++
			}
		}
		if limited != 1 || len(h.llm.requests()) != 2 {
			t.Fatalf("rate_limited=%d LLM calls=%d, trace %v", limited, len(h.llm.requests()), h.out.names())
		}
		h.endSession()
	})
}

func TestBlockedReplyAppendsRedirectPhrase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(int, LLMRequest) llmScript {
			return llmScript{deltas: []string{"Let me explain this idea. ", "Then something"}, blocked: true, gap: 10 * time.Millisecond}
		}
		h.start(t.Context())
		h.text("hello")
		time.Sleep(3 * time.Second)
		segs := h.out.segments()
		if len(segs) != 3 || segs[0].Text != "Let me explain this idea." || segs[1].Text != "Then something" || segs[2].Text != hindiRedirect() {
			t.Fatalf("segments = %+v", segs)
		}
		if got := h.out.done(); len(got) != 1 || got[0].Status != StatusComplete {
			t.Fatalf("done = %+v", got)
		}
		h.endSession()
	})
}

func TestBlockedBeforeAnySpeechPlaysRedirectClip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(int, LLMRequest) llmScript { return llmScript{blocked: true} }
		h.start(t.Context())
		h.text("hello")
		time.Sleep(3 * time.Second)
		if got := joinTrace(h.out.names()); got != "ready turn.started transcript audio3 done:complete" {
			t.Fatalf("trace = %s", got)
		}
		h.endSession()
	})
}

func TestLLMFailureBeforeFirstTokenRetriesOnFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(call int, r LLMRequest) llmScript {
			if r.Model != fallbackModel {
				return llmScript{err: &ProviderError{Provider: "gemini", Op: "stream", Retryable: true, Err: errBoom}}
			}
			return llmScript{deltas: []string{"The fallback answer works fine."}}
		}
		h.start(t.Context())
		h.text("hello")
		time.Sleep(3 * time.Second)
		reqs := h.llm.requests()
		if len(reqs) != 2 || reqs[0].Model != "" || reqs[1].Model != fallbackModel {
			t.Fatalf("LLM requests = %+v", reqs)
		}
		if got := h.out.segments(); len(got) != 1 || got[0].Text != "The fallback answer works fine." {
			t.Fatalf("segments = %+v", got)
		}
		h.endSession()
	})
}

func TestLLMFirstTokenTimeoutFallsBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(_ int, r LLMRequest) llmScript {
			if r.Model != fallbackModel {
				return llmScript{hang: true}
			}
			return llmScript{deltas: []string{"Here is the answer for you."}}
		}
		h.start(t.Context())
		h.text("hello")
		time.Sleep(10 * time.Second)
		if got := h.out.segments(); len(got) != 1 {
			t.Fatalf("segments = %+v, trace %v", got, h.out.names())
		}
		if lat := h.out.done()[0].Latency; lat.LLMTTFTMs < 4000 {
			t.Fatalf("first token at %d ms, want after the 4 s watchdog", lat.LLMTTFTMs)
		}
		h.endSession()
	})
}

func TestLLMFailureEverywhereFailsTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(int, LLMRequest) llmScript { return llmScript{err: errBoom} }
		h.start(t.Context())
		h.text("hello")
		time.Sleep(3 * time.Second)
		if got := joinTrace(h.out.names()); got != "ready turn.started transcript error:llm_failed audio3 done:failed" {
			t.Fatalf("trace = %s", got)
		}
		h.endSession()
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 || inserts[0].Tutor.Status != entity.MessageFailed || inserts[0].Tutor.Content != "" {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestMidStreamLLMFailureKeepsWhatWasSpoken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(int, LLMRequest) llmScript {
			return llmScript{deltas: []string{"Half of an answer is here. ", "and then"}, err: errBoom}
		}
		h.start(t.Context())
		h.text("hello")
		time.Sleep(3 * time.Second)
		trace := h.out.names()
		if !contains(trace, "seg0", "audio1", "error:llm_failed", "done:complete") {
			t.Fatalf("trace = %v", trace)
		}
		h.endSession()
	})
}

func TestTTSFailureOnOneSegmentKeepsCaption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.tts.fail = func(text string) error {
			if strings.HasPrefix(text, "Second") {
				return errBoom
			}
			return nil
		}
		h.start(t.Context())
		h.text("hello")
		time.Sleep(5 * time.Second)
		want := "ready turn.started transcript seg0 audio1 seg1 error:tts_failed seg2 audio1 done:complete"
		if got := joinTrace(h.out.names()); got != want {
			t.Fatalf("trace:\n got %s\nwant %s", got, want)
		}
		h.endSession()
		inserts, _ := h.store.snapshot()
		if !strings.Contains(inserts[0].Tutor.Content, "Second sentence follows.") {
			t.Fatalf("the caption of the failed segment is not in the stored reply: %q", inserts[0].Tutor.Content)
		}
	})
}

func TestTTSRetriesRetryableErrorOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		calls := 0
		h.llm.script = func(int, LLMRequest) llmScript { return llmScript{deltas: []string{"One short answer for you."}} }
		h.tts.fail = func(string) error {
			calls++
			if calls == 1 {
				return &ProviderError{Provider: "bhashini", Op: "tts", Retryable: true, Err: errBoom}
			}
			return nil
		}
		h.start(t.Context())
		h.text("hello")
		time.Sleep(3 * time.Second)
		if got := joinTrace(h.out.names()); got != "ready turn.started transcript seg0 audio1 done:complete" {
			t.Fatalf("trace = %s", got)
		}
		h.endSession()
	})
}

func TestFillerPlaysWhenAnswerIsSlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.FillerAfter = time.Second })
		h.llm.script = func(int, LLMRequest) llmScript {
			return llmScript{deltas: []string{"A slow but good answer."}, firstDelay: 2 * time.Second}
		}
		h.start(t.Context())
		h.text("hello")
		time.Sleep(6 * time.Second)
		if got := joinTrace(h.out.names()); got != "ready turn.started transcript audio2 seg0 audio1 done:complete" {
			t.Fatalf("trace = %s", got)
		}
		if !h.out.done()[0].Latency.Filler {
			t.Fatal("latency does not report the filler")
		}
		h.endSession()
	})
}

func TestNoFillerWhenAnswerIsFast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.FillerAfter = 5 * time.Second })
		h.start(t.Context())
		h.text("hello")
		time.Sleep(6 * time.Second)
		if slices.Contains(h.out.names(), "audio2") {
			t.Fatalf("filler played: %v", h.out.names())
		}
		h.endSession()
	})
}

func TestPingGetsPong(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.s.Message(ClientMsg{Type: MsgPing, T: []byte(`123`)})
		synctest.Wait()
		pongs := h.out.find(func(e Event) bool { _, ok := e.(Pong); return ok })
		if len(pongs) != 1 || string(pongs[0].(Pong).T) != "123" {
			t.Fatalf("pongs = %+v", pongs)
		}
		h.endSession()
	})
}

// hindiRedirect is the phrase spoken after a reply that was blocked mid-way (the session is in Hindi).
func hindiRedirect() string {
	hi, _ := entity.LookupLanguage("hi")
	return hi.Phrases.Redirect
}
