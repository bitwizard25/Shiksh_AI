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

// slowReply streams three sentences one second apart, so a turn is still running while the learner acts.
func slowReply(int, LLMRequest) llmScript {
	return llmScript{deltas: threeSentences, firstDelay: 300 * time.Millisecond, gap: time.Second}
}

// indexOf returns the position of the first recorded event matching match, or -1.
func (o *fakeOut) indexOf(match func(Event) bool) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, r := range o.recs {
		if match(r.ev) {
			return i
		}
	}
	return -1
}

// after returns the events recorded after index i that belong to turn id.
func (o *fakeOut) after(i int, id uint32) []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Event
	for _, r := range o.recs[i+1:] {
		if tid, scoped := r.ev.Turn(); scoped && tid == id {
			out = append(out, r.ev)
		}
	}
	return out
}

func TestLiveBargeInStopsTheTurnAndStoresWhatWasHeard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = slowReply
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(1100 * time.Millisecond) // segment 0 is out, segment 1 is still being generated

		h.s.Message(ClientMsg{Type: MsgSpeechStart, Played: &Played{TurnID: 1, ChunkSeq: 0}})
		time.Sleep(10 * time.Second)

		doneAt := h.out.indexOf(func(e Event) bool { d, ok := e.(TutorDone); return ok && d.Status == StatusInterrupted })
		if doneAt < 0 {
			t.Fatalf("no interrupted tutor.done: %v", h.out.names())
		}
		if late := h.out.after(doneAt, 1); len(late) != 0 {
			t.Fatalf("%d turn-1 events were sent after the interruption: %v", len(late), late)
		}
		h.endSession()
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 || inserts[0].Tutor.Status != entity.MessageInterrupted || inserts[0].Tutor.Content != "First sentence is here." {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestBargeInWithoutPlayedMeansNothingWasHeard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = slowReply
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(1100 * time.Millisecond)
		h.msg(MsgSpeechStart)
		synctest.Wait()
		h.endSession()
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 || inserts[0].Tutor.Content != "" || inserts[0].Tutor.Status != entity.MessageInterrupted {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestBargeInThenNewTurnIsNotPolluted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		calls := 0
		h.llm.script = func(call int, r LLMRequest) llmScript {
			calls++
			if call == 0 {
				return slowReply(call, r)
			}
			return llmScript{deltas: []string{"The new answer is here."}}
		}
		h.start(t.Context())
		h.text("first question")
		time.Sleep(1100 * time.Millisecond)
		h.msg(MsgSpeechStart)
		h.audio(1000)
		h.msg(MsgSpeechEnd)
		time.Sleep(10 * time.Second)

		for _, seg := range h.out.segments() {
			if seg.TurnID == 2 && seg.Text != "The new answer is here." {
				t.Fatalf("turn 2 got a segment of the old reply: %+v", seg)
			}
		}
		// The interrupted first reply was heard as nothing, so only the learner's question stays in history.
		if msgs := h.llm.requests()[1].Messages; len(msgs) != 2 || msgs[0].Text != "first question" || msgs[0].Role != usecase.ChatUser {
			t.Fatalf("history = %+v", msgs)
		}
		h.endSession()
		inserts, _ := h.store.snapshot()
		if len(inserts) != 2 || inserts[0].TurnNo != 1 || inserts[1].TurnNo != 2 {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestContinuationMergesPausedSpeech(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(int, LLMRequest) llmScript {
			return llmScript{deltas: []string{"Here is the full answer."}, firstDelay: 3 * time.Second}
		}
		h.start(t.Context())
		h.utterance(1000)
		time.Sleep(500 * time.Millisecond) // inside the 2 s continuation window, no tutor audio yet
		h.msg(MsgSpeechStart)
		h.audio(500)
		h.msg(MsgSpeechEnd)
		time.Sleep(10 * time.Second)

		reqs := h.asr.requests()
		if len(reqs) != 2 || len(reqs[0].PCM) != 32000 || len(reqs[1].PCM) != 48000 {
			sizes := []int{}
			for _, r := range reqs {
				sizes = append(sizes, len(r.PCM))
			}
			t.Fatalf("ASR audio sizes = %v, want [32000 48000]", sizes)
		}
		var started int
		for _, n := range h.out.names() {
			if n == "turn.started" {
				started++
			}
		}
		if started != 2 {
			t.Fatalf("trace = %v", h.out.names())
		}
		h.endSession()
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 || inserts[0].TurnNo != 1 || *inserts[0].Learner.AudioMs != 1500 {
			t.Fatalf("inserts = %+v (the merged turn must be stored once, as turn 1)", inserts)
		}
		for _, seg := range h.out.segments() {
			if seg.TurnID == 1 {
				t.Fatalf("the cancelled attempt spoke: %+v", seg)
			}
		}
	})
}

func TestSpeechAfterContinuationWindowIsABargeIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.llm.script = func(int, LLMRequest) llmScript { return llmScript{hang: true} }
		h.start(t.Context())
		h.utterance(1000)
		time.Sleep(2500 * time.Millisecond) // past the window
		h.msg(MsgSpeechStart)
		synctest.Wait()
		if !slices.Contains(h.out.names(), "done:interrupted") {
			t.Fatalf("trace = %v", h.out.names())
		}
		h.endSession()
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 || inserts[0].Tutor.Status != entity.MessageInterrupted || inserts[0].Tutor.Content != "" {
			t.Fatalf("inserts = %+v", inserts)
		}
	})
}

func TestLateBargeInTruncatesTheStoredReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(2 * time.Second) // the whole reply is already sent; the client is still playing it
		h.s.Message(ClientMsg{Type: MsgSpeechStart, Played: &Played{TurnID: 1, ChunkSeq: 0}})
		h.audio(500)
		h.msg(MsgSpeechCancel)
		synctest.Wait()

		// The next turn's history must show only the heard part.
		h.text("another question")
		time.Sleep(3 * time.Second)
		reqs := h.llm.requests()
		hist := reqs[1].Messages
		if len(hist) != 3 || hist[1].Role != usecase.ChatModel || hist[1].Text != "First sentence is here. (learner interrupted)" {
			t.Fatalf("history = %+v", hist)
		}
		h.endSession()
		_, updates := h.store.snapshot()
		if !slices.Equal(updates, []string{"1:First sentence is here.:interrupted"}) {
			t.Fatalf("updates = %v", updates)
		}
	})
}

func TestLateBargeInIgnoredAfterPlaybackDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(2 * time.Second)
		h.s.Message(ClientMsg{Type: MsgPlaybackDone, TurnID: 1})
		h.msg(MsgSpeechStart)
		h.msg(MsgSpeechCancel)
		synctest.Wait()
		h.endSession()
		if _, updates := h.store.snapshot(); len(updates) != 0 {
			t.Fatalf("a fully played reply was truncated: %v", updates)
		}
	})
}

func TestLateBargeInWithoutPlayedMeansNothingHeard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.text("explain fractions")
		time.Sleep(2 * time.Second)
		h.msg(MsgSpeechStart)
		h.msg(MsgSpeechCancel)
		synctest.Wait()
		h.endSession()
		if _, updates := h.store.snapshot(); !slices.Equal(updates, []string{"1::interrupted"}) {
			t.Fatalf("updates = %v", updates)
		}
	})
}

func TestPlaybackStartedRecordsClientLatency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		h.text("hello")
		time.Sleep(time.Second)
		h.s.Message(ClientMsg{Type: MsgPlaybackStarted, TurnID: 1})
		synctest.Wait()
		h.endSession()
		inserts, _ := h.store.snapshot()
		if len(inserts) != 1 {
			t.Fatalf("inserts = %d", len(inserts))
		}
		// The persisted latency was written when the turn finalized, before playback.started arrived;
		// the live timing object still carries the value for metrics.
		if h.s.last == nil {
			t.Fatal("no last turn")
		}
		if d := h.s.last.timing.latency().ClientPlaybackMs; d != 1000 {
			t.Fatalf("client playback = %d ms, want 1000", d)
		}
	})
}

func TestResumedSessionContinuesNumbering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		history := []entity.Message{
			{TurnNo: 3, Role: entity.RoleLearner, Content: "old question", Status: entity.MessageComplete},
			{TurnNo: 3, Role: entity.RoleTutor, Content: "old answer", Status: entity.MessageComplete},
		}
		h := newHarnessWith(t, nil, func(p *SessionParams) { p.History, p.MaxTurnNo, p.Memory = history, 3, []string{"learned fractions"} })
		h.start(t.Context())
		h.text("new question")
		time.Sleep(3 * time.Second)

		ready := h.out.find(func(e Event) bool { _, ok := e.(SessionReady); return ok })[0].(SessionReady)
		if !ready.Resumed || ready.NextTurnID != 4 || len(ready.History) != 2 || ready.History[1].Content != "old answer" {
			t.Fatalf("ready = %+v", ready)
		}
		req := h.llm.requests()[0]
		if len(req.Messages) != 3 || req.Messages[0].Text != "old question" || !strings.Contains(req.System, "learned fractions") {
			t.Fatalf("request = %+v", req)
		}
		h.endSession()
		if inserts, _ := h.store.snapshot(); len(inserts) != 1 || inserts[0].TurnNo != 4 {
			t.Fatalf("inserts = %+v", inserts)
		}
		if turn := h.out.find(func(e Event) bool { _, ok := e.(TurnStarted); return ok })[0].(TurnStarted); turn.TurnID != 4 {
			t.Fatalf("first wire turn id = %d, want 4", turn.TurnID)
		}
	})
}

func TestFreshSessionReadyHasNoHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.start(t.Context())
		synctest.Wait()
		ready := h.out.find(func(e Event) bool { _, ok := e.(SessionReady); return ok })[0].(SessionReady)
		if ready.Resumed || ready.NextTurnID != 1 || ready.History == nil || len(ready.History) != 0 ||
			ready.AudioIn.SampleRate != 16000 || ready.Language != "hi" || ready.Subject != "math" || ready.Limits.MaxUtteranceMs != 30000 {
			t.Fatalf("ready = %+v", ready)
		}
		h.endSession()
	})
}
