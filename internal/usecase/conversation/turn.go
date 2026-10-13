package conversation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

var (
	errBargeIn      = errors.New("learner barged in")
	errContinuation = errors.New("learner kept speaking")
	errTurnDeadline = errors.New("turn took too long")
	errEmptyReply   = errors.New("the model returned no text")
)

const (
	asrRetryBudget  = 1500 * time.Millisecond
	finishMaxTokens = "MAX_TOKENS" // Delta.FinishReason when the reply hit the output token limit
	inputVoice      = entity.InputVoice
	inputText       = entity.InputText
)

// segText is a spoken segment as the learner reads it in the caption.
type segText struct {
	seq  int
	text string
}

// turnResult is how a turn ended, as reported to the actor.
type turnResult struct {
	status      string // StatusComplete or StatusFailed
	learnerText string // "" when nothing was recognized
}

// turn is one learner utterance and the tutor's reply. It runs on its own goroutine and never
// touches actor state; it reads only what it was given at start.
type turn struct {
	id     uint32
	no     int
	mode   string
	pcm    []byte
	text   string
	ctx    context.Context
	cancel context.CancelCauseFunc
	timing *timing

	deps    *Deps
	out     Output
	lang    entity.Language
	system  string
	history []ChatMessage

	mu          sync.Mutex
	learnerText string
	segs        []segText

	audioQueued atomic.Bool
	completed   atomic.Bool // the turn ran to its natural end
	finalized   bool        // actor only
}

func (t *turn) snapshot() (learner string, segs []segText) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.learnerText, append([]segText(nil), t.segs...)
}

func (t *turn) addSegment(seg segText) {
	t.mu.Lock()
	t.segs = append(t.segs, seg)
	t.mu.Unlock()
}

func (t *turn) now() time.Time { return t.deps.Now() }

// send queues a turn-scoped event. Errors mean the turn was cancelled or the connection is gone.
// A turn that ran out of time can still report that, on a short fresh deadline.
func (t *turn) send(e Event) error {
	ctx := t.ctx
	if errors.Is(context.Cause(ctx), errTurnDeadline) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
	}
	return t.out.Send(ctx, e)
}

// run executes the turn and hands its result to done. A turn the actor cancelled reports nothing
// to the client: the actor owns the interruption.
func (t *turn) run(done func(turnResult)) {
	res := t.execute()
	t.timing.finish(t.now())
	cause := context.Cause(t.ctx)
	if cause != nil && !errors.Is(cause, errTurnDeadline) {
		done(res) // cancelled by the actor: it already moved on
		return
	}
	if cause != nil { // out of time
		t.fail(ErrLLMFailed, "the tutor took too long to answer", true)
		res.status = StatusFailed
	}
	if res.status == StatusComplete || res.status == StatusFailed {
		_ = t.send(TutorDone{TurnID: t.id, Status: res.status, Latency: t.timing.latency()})
	}
	t.completed.Store(res.status == StatusComplete)
	done(res)
}

func (t *turn) execute() turnResult {
	if t.mode == inputVoice {
		text, res, ok := t.recognize()
		if !ok {
			return res
		}
		t.text = text
	}
	t.mu.Lock()
	t.learnerText = t.text
	t.mu.Unlock()
	if t.send(Transcript{TurnID: t.id, Text: t.text}) != nil {
		return turnResult{status: StatusFailed}
	}
	return t.respond()
}

// recognize runs speech recognition. ok is false when the turn ends here (nothing heard, or ASR failed).
func (t *turn) recognize() (text string, res turnResult, ok bool) {
	// One immediate retry for a retryable failure, while the retry budget lasts.
	err := Retry(t.ctx, 2, asrRetryBudget, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, t.deps.Cfg.ASRTimeout)
		defer cancel()
		r, err := t.deps.ASR.Transcribe(ctx, ASRRequest{Lang: t.lang.Code, PCM: t.pcm, SampleRate: InputSampleRate})
		text = r.Text
		return err
	})
	t.timing.mark(markASR, t.now())
	if t.ctx.Err() != nil {
		return "", turnResult{status: StatusFailed}, false
	}
	if err != nil {
		t.fail(ErrASRFailed, "speech recognition failed", true)
		return "", turnResult{status: StatusFailed}, false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		_ = t.send(TranscriptEmpty{TurnID: t.id})
		t.playClip(ClipRepeat, KindClip)
		return "", turnResult{status: StatusComplete}, false
	}
	return text, turnResult{}, true
}

// fail reports an error to the client and plays the error clip.
func (t *turn) fail(code, message string, retry bool) {
	id := t.id
	_ = t.send(ErrorEvent{Code: code, Message: message, Retryable: retry, TurnID: &id})
	t.playClip(ClipError, KindClip)
}

// playClip sends a pre-recorded clip, if there is one.
func (t *turn) playClip(kind ClipKind, frameKind byte) bool {
	clip, ok := t.deps.Clips.Get(kind, t.lang.Code)
	if !ok || len(clip.PCM) == 0 {
		return false
	}
	return t.sendPCM(frameKind, 0, clip.PCM, clip.SampleRate, frameKind != KindFiller) == nil
}

// sendPCM splits PCM into frames of about FrameMs and queues them. last marks the final frame of the turn.
func (t *turn) sendPCM(kind byte, seq int, pcm []byte, rate int, lastOfTurn bool) error {
	frameBytes := max(rate*bytesPerSample*t.deps.Cfg.FrameMs/1000, 2) &^ 1
	for part, off := 0, 0; off < len(pcm); part++ {
		end := min(off+frameBytes, len(pcm))
		last := end == len(pcm)
		f := AudioFrame{
			Kind: kind, LastOfSeg: last, LastOfTurn: last && lastOfTurn, TurnID: t.id,
			ChunkSeq: uint16(seq), Part: uint16(part), SampleRate: rate, PCM: pcm[off:end],
		}
		if err := t.send(f); err != nil {
			return err
		}
		if kind == KindSpeech {
			if !t.audioQueued.Swap(true) {
				t.timing.mark(markFirstAudio, t.now())
			}
		}
		off = end
	}
	return nil
}

// producerResult is what the LLM producer reports once its stream is finished.
type producerResult struct {
	err           error // the stream failed
	spoke         bool  // at least one segment was queued for speech
	blocked       bool  // the safety filter stopped the reply
	redirectClip  bool  // blocked before any speech: play the redirect clip instead of a phrase
	gotFirstToken bool
}

func (t *turn) respond() turnResult {
	pool := newTTSPool(t.ctx, t.deps.Cfg.TTSParallelism, t.synthesize)
	prodCh := make(chan producerResult, 1)
	go func() { prodCh <- t.produce(pool) }()

	t.emit(pool)
	pool.Wait()
	prod := <-prodCh

	if t.ctx.Err() != nil {
		return turnResult{status: StatusFailed, learnerText: t.text}
	}
	if prod.redirectClip {
		t.playClip(ClipRedirect, KindClip)
	}
	if prod.err != nil {
		t.deps.Log.Warn("llm failed", "turn_id", t.id, "err", prod.err)
		if !prod.spoke {
			t.fail(ErrLLMFailed, "the tutor could not answer", true)
			return turnResult{status: StatusFailed, learnerText: t.text}
		}
		id := t.id
		_ = t.send(ErrorEvent{Code: ErrLLMFailed, Message: "the answer was cut short", Retryable: true, TurnID: &id})
	}
	if !prod.spoke && !prod.redirectClip && prod.err == nil {
		t.fail(ErrLLMFailed, "the tutor could not answer", true)
		return turnResult{status: StatusFailed, learnerText: t.text}
	}
	return turnResult{status: StatusComplete, learnerText: t.text}
}

// produce streams the reply, cuts it into segments and feeds the TTS pool. A failure before the
// first token is retried once on the fallback model.
func (t *turn) produce(pool *ttsPool) producerResult {
	defer pool.CloseInput()
	seg := NewSegmenter(t.deps.Cfg.FirstMinRunes)
	var res producerResult
	seq := 0
	submit := func(raw string) bool {
		text := NormalizeForSpeech(raw)
		if text == "" {
			return true
		}
		if seq == 0 {
			t.timing.mark(markFirstSegment, t.now())
		}
		t.addSegment(segText{seq: seq, text: text})
		res.spoke = true
		err := pool.Submit(t.ctx, segment{seq: seq, text: text})
		seq++
		return err == nil
	}

	req := LLMRequest{System: t.system, Messages: append(append([]ChatMessage(nil), t.history...), ChatMessage{Role: RoleUser, Text: t.text})}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			req.Model = t.deps.Cfg.FallbackModel
		}
		err := t.stream(req, seg, submit, &res)
		res.err = err
		if err == nil || res.gotFirstToken || t.ctx.Err() != nil {
			break
		}
		t.deps.Log.Warn("llm failed before the first token", "turn_id", t.id, "attempt", attempt, "err", err)
	}
	if t.ctx.Err() != nil {
		return res
	}
	if rest := seg.Flush(); rest != "" {
		submit(rest)
	}
	if res.blocked {
		if res.spoke {
			submit(t.lang.Phrases.Redirect)
		} else {
			res.redirectClip = true
		}
		res.err = nil
	}
	return res
}

// stream reads one LLM attempt. The first-token watchdog cancels the attempt when no token arrives in time.
func (t *turn) stream(req LLMRequest, seg *Segmenter, submit func(string) bool, res *producerResult) error {
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	watchdog := time.AfterFunc(t.deps.Cfg.LLMFirstToken, cancel)
	defer watchdog.Stop()
	got := false
	for d, err := range t.deps.LLM.Stream(ctx, req) {
		if err != nil {
			return err
		}
		if !got {
			got = true
			res.gotFirstToken = true
			watchdog.Stop()
			t.timing.mark(markLLMFirstToken, t.now())
		}
		if d.Blocked { // text in the blocked chunk is not spoken
			res.blocked = true
			return nil
		}
		for _, s := range seg.Push(d.Text) {
			if !submit(s) {
				return t.ctx.Err()
			}
		}
		if d.FinishReason == finishMaxTokens {
			t.deps.Log.Info("reply hit the output token limit", "turn_id", t.id)
		}
	}
	if !got {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return errEmptyReply
	}
	return nil
}

// synthesize turns one segment into speech, retrying once on a retryable failure or timeout.
func (t *turn) synthesize(ctx context.Context, s segment) ttsResult {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, t.deps.Cfg.TTSTimeout)
		var r TTSResult
		r, err = t.deps.TTS.Synthesize(cctx, TTSRequest{Lang: t.lang.Code, Text: s.text, Gender: t.lang.TTSGender})
		timedOut := cctx.Err() != nil && ctx.Err() == nil
		cancel()
		if err == nil && len(r.PCM) > 0 {
			t.timing.mark(markFirstTTS, t.now())
			return ttsResult{pcm: r.PCM, rate: r.SampleRate}
		}
		if err == nil {
			err = errors.New("speech synthesis returned no audio")
		}
		if ctx.Err() != nil || !(IsRetryable(err) || timedOut) {
			break
		}
	}
	return ttsResult{err: err}
}

// emit sends captions and audio in segment order, and plays a filler if the answer is slow.
func (t *turn) emit(pool *ttsPool) {
	var fillerC <-chan time.Time
	if after := t.deps.Cfg.FillerAfter; after > 0 {
		timer := time.NewTimer(max(0, after-t.now().Sub(t.timing.start)))
		defer timer.Stop()
		fillerC = timer.C
	}
	for seq := 0; ; {
		res, st := pool.Poll(seq)
		switch st {
		case pollDone:
			return
		case pollPending:
			select {
			case <-pool.Notify():
			case <-fillerC:
				fillerC = nil
				if !t.audioQueued.Load() && t.playClip(ClipFiller, KindFiller) {
					t.timing.setFiller()
					t.deps.Metrics.FillerPlayed()
				}
			case <-t.ctx.Done():
				return
			}
		case pollReady:
			if !t.emitSegment(pool, seq, res) {
				return
			}
			seq++
		}
	}
}

func (t *turn) emitSegment(pool *ttsPool, seq int, res ttsResult) bool {
	text := t.segmentText(seq)
	noAudio := res.err != nil || len(res.pcm) == 0
	if t.send(TutorSegment{TurnID: t.id, ChunkSeq: seq, Text: text, SampleRate: res.rate, NoAudio: noAudio}) != nil {
		return false
	}
	if noAudio {
		id := t.id
		return t.send(ErrorEvent{Code: ErrTTSFailed, Message: "could not speak this part", Retryable: false, TurnID: &id}) == nil
	}
	return t.sendPCM(KindSpeech, seq, res.pcm, res.rate, pool.final(seq)) == nil
}

func (t *turn) segmentText(seq int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.segs {
		if s.seq == seq {
			return s.text
		}
	}
	return ""
}

// chatHistory converts the stored conversation, as usecase.BuildHistory shapes it, to the LLM port's messages.
func chatHistory(msgs []usecase.ChatMessage) []ChatMessage {
	out := make([]ChatMessage, len(msgs))
	for i, m := range msgs {
		out[i] = ChatMessage{Role: Role(m.Role), Text: m.Text}
	}
	return out
}
