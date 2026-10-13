package conversation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// ---- fake output ----------------------------------------------------------------------------

type outRec struct {
	ev      Event
	control bool
}

// fakeOut records what the client would receive and applies the same stale-turn drop rule as the real outbox.
type fakeOut struct {
	mu      sync.Mutex
	recs    []outRec
	cur     uint32
	heard   map[uint32]int
	dropped int
	full    bool
	closed  *CloseInfo
}

func newFakeOut() *fakeOut { return &fakeOut{heard: map[uint32]int{}} }

func (o *fakeOut) SetTurn(id uint32) { o.mu.Lock(); o.cur = id; o.mu.Unlock() }

func (o *fakeOut) Send(ctx context.Context, e Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if id, scoped := e.Turn(); scoped && id != o.cur {
		o.dropped++
		return nil
	}
	o.add(e, false)
	return nil
}

func (o *fakeOut) TrySend(e Event) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.full {
		return false
	}
	o.add(e, true)
	return true
}

func (o *fakeOut) add(e Event, control bool) {
	o.recs = append(o.recs, outRec{e, control})
	switch e := e.(type) {
	case TutorSegment:
		if e.NoAudio {
			o.heard[e.TurnID]++
		}
	case AudioFrame:
		if e.Kind == KindSpeech && e.LastOfSeg {
			o.heard[e.TurnID]++
		}
	}
}

func (o *fakeOut) HeardSegments(id uint32) int { o.mu.Lock(); defer o.mu.Unlock(); return o.heard[id] }

func (o *fakeOut) Close(code int, reason string) {
	o.mu.Lock()
	o.closed = &CloseInfo{code, reason}
	o.mu.Unlock()
}

// names returns a compact trace such as ["ready", "turn.started", "transcript", "seg0", "audio1", "done:complete"].
func (o *fakeOut) names() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	var lastAudio string
	flush := func() {
		if lastAudio != "" {
			out = append(out, lastAudio)
			lastAudio = ""
		}
	}
	for _, r := range o.recs {
		var n string
		switch e := r.ev.(type) {
		case SessionReady:
			n = "ready"
		case TurnStarted:
			n = "turn.started"
		case Transcript:
			n = "transcript"
		case TranscriptEmpty:
			n = "transcript.empty"
		case TutorSegment:
			n = fmt.Sprintf("seg%d", e.ChunkSeq)
		case TutorDone:
			n = "done:" + e.Status
		case ErrorEvent:
			n = "error:" + e.Code
		case SessionEnded:
			n = "ended:" + e.Reason
		case Pong:
			n = "pong"
		case AudioFrame:
			a := fmt.Sprintf("audio%d", e.Kind)
			if a == lastAudio { // collapse runs of frames of one kind
				continue
			}
			flush()
			lastAudio = a
			continue
		}
		flush()
		out = append(out, n)
	}
	flush()
	return out
}

func (o *fakeOut) find(match func(Event) bool) []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Event
	for _, r := range o.recs {
		if match(r.ev) {
			out = append(out, r.ev)
		}
	}
	return out
}

func (o *fakeOut) segments() []TutorSegment {
	var out []TutorSegment
	for _, e := range o.find(func(e Event) bool { _, ok := e.(TutorSegment); return ok }) {
		out = append(out, e.(TutorSegment))
	}
	return out
}

func (o *fakeOut) done() []TutorDone {
	var out []TutorDone
	for _, e := range o.find(func(e Event) bool { _, ok := e.(TutorDone); return ok }) {
		out = append(out, e.(TutorDone))
	}
	return out
}

// ---- fake providers -------------------------------------------------------------------------

type fakeASR struct {
	mu    sync.Mutex
	text  string
	err   error
	delay time.Duration
	got   []ASRRequest
}

func (a *fakeASR) Transcribe(ctx context.Context, r ASRRequest) (ASRResult, error) {
	a.mu.Lock()
	a.got = append(a.got, r)
	text, err, delay := a.text, a.err, a.delay
	a.mu.Unlock()
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return ASRResult{}, ctx.Err()
	}
	return ASRResult{Text: text}, err
}

func (a *fakeASR) requests() []ASRRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ASRRequest(nil), a.got...)
}

// llmScript describes one LLM call.
type llmScript struct {
	deltas     []string
	firstDelay time.Duration // before the first delta
	gap        time.Duration // between deltas
	err        error         // returned after the deltas
	blocked    bool          // the last delta is a safety block
	hang       bool          // never produce anything (until cancelled)
}

type fakeLLM struct {
	mu     sync.Mutex
	script func(call int, r LLMRequest) llmScript
	reqs   []LLMRequest
}

func (l *fakeLLM) Stream(ctx context.Context, r LLMRequest) iter.Seq2[Delta, error] {
	l.mu.Lock()
	call := len(l.reqs)
	l.reqs = append(l.reqs, r)
	sc := l.script(call, r)
	l.mu.Unlock()
	return func(yield func(Delta, error) bool) {
		wait := func(d time.Duration) bool {
			select {
			case <-time.After(d):
				return true
			case <-ctx.Done():
				yield(Delta{}, ctx.Err())
				return false
			}
		}
		if sc.hang {
			<-ctx.Done()
			yield(Delta{}, ctx.Err())
			return
		}
		if !wait(sc.firstDelay) {
			return
		}
		for i, d := range sc.deltas {
			if i > 0 && !wait(sc.gap) {
				return
			}
			if !yield(Delta{Text: d}, nil) {
				return
			}
		}
		if sc.blocked {
			yield(Delta{Blocked: true}, nil)
			return
		}
		if sc.err != nil {
			yield(Delta{}, sc.err)
		}
	}
}

// Complete is part of the LLM port (session summaries); the realtime session only streams.
func (l *fakeLLM) Complete(context.Context, LLMRequest) (string, error) {
	return "", errors.New("fakeLLM: Complete is not used by the session")
}

func (l *fakeLLM) requests() []LLMRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LLMRequest(nil), l.reqs...)
}

type fakeTTS struct {
	mu    sync.Mutex
	delay func(text string) time.Duration
	fail  func(text string) error
	calls []string
}

const ttsRate = 8000

func (t *fakeTTS) Synthesize(ctx context.Context, r TTSRequest) (TTSResult, error) {
	t.mu.Lock()
	t.calls = append(t.calls, r.Text)
	delay, fail := t.delay, t.fail
	t.mu.Unlock()
	d := 100 * time.Millisecond
	if delay != nil {
		d = delay(r.Text)
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
		return TTSResult{}, ctx.Err()
	}
	if fail != nil {
		if err := fail(r.Text); err != nil {
			return TTSResult{}, err
		}
	}
	// 400 ms of audio (two 200 ms frames) per segment, so frame splitting is visible.
	return TTSResult{PCM: make([]byte, ttsRate*bytesPerSample*400/1000), SampleRate: ttsRate}, nil
}

type fakeClips map[ClipKind]bool

func (c fakeClips) Get(kind ClipKind, _ string) (Clip, bool) {
	if !c[kind] {
		return Clip{}, false
	}
	return Clip{PCM: make([]byte, 3200), SampleRate: ttsRate}, true
}

// ---- fake store -----------------------------------------------------------------------------

type memStore struct {
	mu        sync.Mutex
	epoch     int
	inserts   []usecase.TurnInsert
	updates   []string // "turn:content:status"
	touches   int
	ended     string
	failInert error
	maxTurn   int
}

func (s *memStore) Connect(context.Context, uuid.UUID, time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	return s.epoch, nil
}

func (s *memStore) MaxTurnNo(context.Context, uuid.UUID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.maxTurn
	for _, in := range s.inserts {
		n = max(n, in.TurnNo)
	}
	return n, nil
}

func (s *memStore) InsertTurn(_ context.Context, _ uuid.UUID, epoch int, t usecase.TurnInsert, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return entity.ErrFenced
	}
	if s.failInert != nil {
		return s.failInert
	}
	s.inserts = append(s.inserts, t)
	return nil
}

func (s *memStore) UpdateTutor(_ context.Context, _ uuid.UUID, epoch, turnNo int, content, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return entity.ErrFenced
	}
	s.updates = append(s.updates, fmt.Sprintf("%d:%s:%s", turnNo, content, status))
	return nil
}

func (s *memStore) Touch(_ context.Context, _ uuid.UUID, epoch int, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return entity.ErrFenced
	}
	s.touches++
	return nil
}

func (s *memStore) snapshot() (inserts []usecase.TurnInsert, updates []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]usecase.TurnInsert(nil), s.inserts...), append([]string(nil), s.updates...)
}

// memDB implements the repositories the hub and the actor read from and end sessions on.
type memDB struct {
	mu       sync.Mutex
	sess     entity.TutoringSession
	user     entity.User
	store    *memStore
	endCalls []string
	summary  []string
}

// The repository ports share method names, so each gets its own thin view of memDB.
type (
	dbUsers struct {
		usecase.UserRepository
		db *memDB
	}
	dbSessions struct {
		usecase.SessionRepository
		db *memDB
	}
	dbMessages struct {
		usecase.MessageRepository
		db *memDB
	}
)

func (d *memDB) users() dbUsers       { return dbUsers{db: d} }
func (d *memDB) sessions() dbSessions { return dbSessions{db: d} }
func (d *memDB) messages() dbMessages { return dbMessages{db: d} }

func (u dbUsers) GetByID(context.Context, uuid.UUID) (entity.User, error) { return u.db.user, nil }

func (v dbSessions) Get(context.Context, uuid.UUID) (entity.TutoringSession, error) {
	d := v.db
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sess, nil
}

func (v dbSessions) End(_ context.Context, _ uuid.UUID, reason string, _ time.Time) (bool, error) {
	d := v.db
	d.mu.Lock()
	defer d.mu.Unlock()
	d.endCalls = append(d.endCalls, reason)
	d.sess.Status = entity.SessionEnded
	return true, nil
}

func (v dbSessions) RecentSummaries(context.Context, uuid.UUID, string, int) ([]string, error) {
	return v.db.summary, nil
}

func (v dbMessages) Recent(_ context.Context, _ uuid.UUID, n int) ([]entity.Message, error) {
	d := v.db
	d.store.mu.Lock()
	defer d.store.mu.Unlock()
	var out []entity.Message
	for _, in := range d.store.inserts {
		out = append(out, in.Learner, in.Tutor)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

func (d *memDB) ends() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.endCalls...)
}

// ---- harness --------------------------------------------------------------------------------

type harness struct {
	t     *testing.T
	s     *Session
	out   *fakeOut
	store *memStore
	db    *memDB
	asr   *fakeASR
	llm   *fakeLLM
	tts   *fakeTTS
	cfg   Config
	ended []uuid.UUID
	res   chan CloseInfo
}

// fallbackModel is the model a reply is retried on after failing before its first token.
const fallbackModel = "fallback-model"

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.FillerAfter = 0
	cfg.FrameMs = 200
	cfg.FallbackModel = fallbackModel
	return cfg
}

// threeSentences streams a reply that becomes three segments.
var threeSentences = []string{"First sentence is here. ", "Second sentence follows. ", "Third one ends it."}

func newHarness(t *testing.T, mutate func(*Config)) *harness { return newHarnessWith(t, mutate, nil) }

func newHarnessWith(t *testing.T, mutate func(*Config), params func(*SessionParams)) *harness {
	t.Helper()
	cfg := testConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	h := &harness{
		t: t, out: newFakeOut(), store: &memStore{}, cfg: cfg, res: make(chan CloseInfo, 1),
		asr: &fakeASR{text: "what is a fraction", delay: 200 * time.Millisecond},
		llm: &fakeLLM{script: func(int, LLMRequest) llmScript {
			return llmScript{deltas: threeSentences, firstDelay: 300 * time.Millisecond, gap: 50 * time.Millisecond}
		}},
		tts: &fakeTTS{},
	}
	h.db = &memDB{
		store: h.store,
		sess:  entity.TutoringSession{ID: uuid.New(), UserID: uuid.New(), Subject: "math", Language: "hi", Status: entity.SessionActive},
		user:  entity.User{DisplayName: "Asha"},
	}
	epoch, _ := h.store.Connect(context.Background(), h.db.sess.ID, time.Time{})
	sp := SessionParams{
		Deps: Deps{ASR: h.asr, LLM: h.llm, TTS: h.tts, Clips: fakeClips{ClipRepeat: true, ClipError: true, ClipFiller: true, ClipRedirect: true},
			Store: h.store, Sessions: h.db.sessions(), Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), OnEnded: func(id uuid.UUID) { h.ended = append(h.ended, id) }},
		Session: h.db.sess, User: h.db.user, Epoch: epoch, Out: h.out,
	}
	if params != nil {
		params(&sp)
	}
	s, err := NewSession(sp)
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	return h
}

// start runs the actor; its close info arrives on h.res.
func (h *harness) start(ctx context.Context) {
	go func() { h.res <- h.s.Run(ctx) }()
}

func (h *harness) msg(typ string) { h.s.Message(ClientMsg{Type: typ}) }

// utterance sends speech.start, ms of audio and speech.end.
func (h *harness) utterance(ms int) {
	h.msg(MsgSpeechStart)
	h.audio(ms)
	h.msg(MsgSpeechEnd)
}

func (h *harness) audio(ms int) {
	for left := ms; left > 0; left -= 100 {
		h.s.Audio(make([]byte, InputSampleRate*bytesPerSample*min(left, 100)/1000))
	}
}

func (h *harness) text(s string) { h.s.Message(ClientMsg{Type: MsgTextInput, Text: s}) }

func (h *harness) close(timeout time.Duration) CloseInfo {
	h.t.Helper()
	select {
	case info := <-h.res:
		return info
	case <-time.After(timeout):
		h.t.Fatal("session did not close")
		return CloseInfo{}
	}
}

func contains(trace []string, want ...string) bool {
	i := 0
	for _, n := range trace {
		if i < len(want) && n == want[i] {
			i++
		}
	}
	return i == len(want)
}

func joinTrace(t []string) string { return strings.Join(t, " ") }

var errBoom = errors.New("boom")

func usecaseTurn(no int) usecase.TurnInsert {
	return usecase.TurnInsert{TurnNo: no, Learner: entity.Message{Content: "q"}, Tutor: entity.Message{Content: "a"}}
}
