package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const (
	maxTextRunes   = 1000
	persistDrain   = 3 * time.Second
	endWriteTimout = 5 * time.Second
	captureSlack   = 2 * time.Second
)

type state int

const (
	stateIdle state = iota
	stateCapturing
	stateResponding
)

// StopCause says why the hub or the REST layer stops a live session.
type StopCause int

const (
	StopReplaced   StopCause = iota + 1 // a newer connection took over
	StopEndRequest                      // the learner ended it over REST
	StopShutdown                        // the server is draining
)

// CloseInfo is how a connection should be closed once the session is done.
type CloseInfo struct {
	Code   int
	Reason string
}

type (
	evMsg      struct{ m ClientMsg }
	evAudio    struct{ pcm []byte }
	evTurnDone struct {
		t   *turn
		res turnResult
	}
	evClosed  struct{ err error }
	evStop    struct{ cause StopCause }
	evFenced  struct{}
	evStorage struct{}
)

// closeReq describes how the session ends.
type closeReq struct {
	code      int
	reason    string
	event     Event  // sent to the client first, if any
	endReason string // end the session in the database with this reason; "" keeps it resumable
}

// turnRecord remembers the last finished turn, so a late barge-in can still cut its text.
type turnRecord struct {
	turnNo       int
	id           uint32
	texts        []string
	persisted    bool
	playbackDone bool
	truncated    bool
	timing       *timing
}

// SessionParams is everything the hub loaded before the actor starts.
type SessionParams struct {
	Deps      Deps
	Session   entity.TutoringSession
	User      entity.User
	Epoch     int
	Out       Output
	History   []entity.Message // oldest first
	Memory    []string
	MaxTurnNo int
}

// Session is the actor for one live connection to a tutoring session.
type Session struct {
	deps    Deps
	meta    entity.TutoringSession
	user    entity.User
	lang    entity.Language
	subject entity.Subject
	epoch   int
	out     Output
	system  string

	persist *Persister
	events  chan any
	done    chan struct{} // closed when the actor stops reading events
	ctx     context.Context

	state       state
	buf         []byte
	prevPCM     []byte
	speechEndAt time.Time
	turnNo      int
	nextTurnID  uint32
	cur         *turn
	last        *turnRecord
	history     []entity.Message
	limiter     *bucket
	turns       sync.WaitGroup
	closing     *closeReq
	resumed     bool
	maxBytes    int
	minBytes    int
	droppedAud  atomic.Int64

	idleT, maxT, captureT *time.Timer
}

// NewSession builds the actor. Call Run to start it.
func NewSession(p SessionParams) (*Session, error) {
	p.Deps.fill()
	lang, ok := entity.LookupLanguage(p.Session.Language)
	if !ok {
		return nil, errors.New("session has an unsupported language")
	}
	subject, ok := entity.LookupSubject(p.Session.Subject)
	if !ok {
		return nil, errors.New("session has an unsupported subject")
	}
	system, err := usecase.BuildSystemPrompt(usecase.PromptInput{
		Language: lang, Subject: subject, Grade: p.Session.Grade, LearnerName: p.User.DisplayName, Memory: p.Memory,
	})
	if err != nil {
		return nil, err
	}
	cfg := p.Deps.Cfg
	return &Session{
		deps: p.Deps, meta: p.Session, user: p.User, lang: lang, subject: subject, epoch: p.Epoch, out: p.Out, system: system,
		events: make(chan any, 256), done: make(chan struct{}),
		turnNo: p.MaxTurnNo, nextTurnID: uint32(p.MaxTurnNo) + 1, history: p.History,
		limiter:  newBucket(cfg.TurnBurst, cfg.TurnRatePerMin, p.Deps.Now()),
		resumed:  p.Session.ConnEpoch > 0 || p.MaxTurnNo > 0,
		maxBytes: int(cfg.MaxUtterance/time.Millisecond) * InputSampleRate * bytesPerSample / 1000,
		minBytes: int(cfg.MinUtterance/time.Millisecond) * InputSampleRate * bytesPerSample / 1000,
	}, nil
}

// ---- Inbound: called by the connection's reader ---------------------------------------------

func (s *Session) post(e any) {
	select {
	case s.events <- e:
	case <-s.done:
	}
}

// Message delivers a decoded client message.
func (s *Session) Message(m ClientMsg) { s.post(evMsg{m}) }

// Audio delivers one binary uplink frame. The actor takes ownership of pcm.
func (s *Session) Audio(pcm []byte) { s.post(evAudio{pcm}) }

// Closed reports that the connection's reader stopped.
func (s *Session) Closed(err error) { s.post(evClosed{err}) }

// Stop asks the session to finish for the given cause.
func (s *Session) Stop(cause StopCause) { s.post(evStop{cause}) }

// ---- Run -------------------------------------------------------------------------------------

// Run drives the session until it ends, the connection closes or ctx is cancelled (treated as a
// server shutdown). It returns how the connection should be closed.
func (s *Session) Run(ctx context.Context) CloseInfo {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.ctx = ctx
	cfg := s.deps.Cfg
	s.persist = NewPersister(s.deps.Store, s.meta.ID, s.epoch, s.deps.Now,
		func() { s.post(evFenced{}) },
		func(err error) {
			s.deps.Log.Error("persist failed", "session_id", s.meta.ID, "err", err)
			s.deps.Metrics.PersistFailure()
			s.post(evStorage{})
		})

	s.idleT = time.NewTimer(cfg.IdleTimeout)
	s.maxT = time.NewTimer(cfg.MaxSessionDuration)
	s.captureT = time.NewTimer(time.Hour)
	s.captureT.Stop()
	touch := time.NewTicker(cfg.TouchEvery)
	defer func() { s.idleT.Stop(); s.maxT.Stop(); s.captureT.Stop(); touch.Stop() }()

	s.out.SetTurn(0)
	s.send(s.ready())

	for s.closing == nil {
		select {
		case e := <-s.events:
			s.handle(e)
		case <-s.idleT.C:
			s.closing = &closeReq{code: CloseIdleTimeout, reason: "idle", event: SessionEnded{Reason: entity.EndIdle}}
		case <-s.maxT.C:
			s.closing = &closeReq{code: CloseNormal, reason: "max duration", event: SessionEnded{Reason: entity.EndMaxDuration}, endReason: entity.EndMaxDuration}
		case <-s.captureT.C:
			if s.state == stateCapturing {
				s.discardCapture()
			}
		case <-touch.C:
			s.persist.Touch()
		case <-ctx.Done():
			s.closing = shutdownReq()
		}
	}
	return s.finish(*s.closing)
}

func shutdownReq() *closeReq {
	return &closeReq{code: CloseGoingAway, reason: "server shutdown",
		event: ErrorEvent{Code: ErrServerShutdown, Message: "the server is restarting, reconnect", Retryable: true}}
}

func (s *Session) ready() SessionReady {
	hist := make([]HistoryItem, 0, len(s.history))
	if s.resumed {
		for _, m := range s.history {
			hist = append(hist, HistoryItem{TurnNo: m.TurnNo, Role: m.Role, Content: m.Content, Status: m.Status})
		}
	}
	cfg := s.deps.Cfg
	return SessionReady{
		SessionID: s.meta.ID.String(), Language: s.lang.Code, Subject: s.subject.Code, Resumed: s.resumed,
		NextTurnID: s.nextTurnID,
		AudioIn:    AudioIn{Encoding: "pcm_s16le", SampleRate: InputSampleRate, Channels: 1},
		AudioOut:   AudioOut{Encoding: "pcm_s16le", Channels: 1},
		Limits:     Limits{MaxUtteranceMs: cfg.MaxUtterance.Milliseconds(), IdleTimeoutMs: cfg.IdleTimeout.Milliseconds()},
		History:    hist,
	}
}

// send queues a control event. A full queue means a client too slow to keep up: the connection is closed.
func (s *Session) send(e Event) {
	if !s.out.TrySend(e) && s.closing == nil {
		s.closing = &closeReq{code: ClosePolicy, reason: "client too slow"}
	}
}

func (s *Session) sendError(code, message string, retry bool) {
	s.send(ErrorEvent{Code: code, Message: message, Retryable: retry})
}

func (s *Session) resetIdle() { s.idleT.Reset(s.deps.Cfg.IdleTimeout) }

// ---- Event handling -------------------------------------------------------------------------

func (s *Session) handle(e any) {
	switch e := e.(type) {
	case evMsg:
		s.resetIdle()
		s.handleMsg(e.m)
	case evAudio:
		s.resetIdle()
		s.handleAudio(e.pcm)
	case evTurnDone:
		s.handleTurnDone(e)
	case evClosed:
		s.closing = &closeReq{code: CloseNormal, reason: "connection closed"}
	case evStop:
		switch e.cause {
		case StopReplaced:
			s.closing = &closeReq{code: CloseReplaced, reason: "replaced",
				event: ErrorEvent{Code: ErrSessionReplaced, Message: "this session was opened on another connection"}}
		case StopEndRequest:
			s.closing = &closeReq{code: CloseNormal, reason: "ended", event: SessionEnded{Reason: entity.EndLearner}, endReason: entity.EndLearner}
		default:
			s.closing = shutdownReq()
		}
	case evFenced:
		s.closing = &closeReq{code: CloseReplaced, reason: "replaced",
			event: ErrorEvent{Code: ErrSessionReplaced, Message: "this session was opened on another connection"}}
	case evStorage:
		s.closing = &closeReq{code: CloseInternal, reason: "storage unavailable",
			event: ErrorEvent{Code: ErrStorageUnavailable, Message: "could not save the session, reconnect", Retryable: true}}
	}
}

func (s *Session) handleMsg(m ClientMsg) {
	switch m.Type {
	case MsgPing:
		s.send(Pong{T: m.T})
	case MsgSessionEnd:
		s.closing = &closeReq{code: CloseNormal, reason: "ended", event: SessionEnded{Reason: entity.EndLearner}, endReason: entity.EndLearner}
	case MsgPlaybackStarted:
		s.playbackStarted(m.TurnID)
	case MsgPlaybackDone:
		if s.last != nil && s.last.id == m.TurnID {
			s.last.playbackDone = true
		}
	case MsgSpeechStart:
		s.speechStart(m.Played)
	case MsgSpeechEnd:
		s.speechEnd()
	case MsgSpeechCancel:
		if s.state == stateCapturing {
			s.discardCapture()
		}
	case MsgTextInput:
		s.textInput(m.Text)
	default:
		s.sendError(ErrBadMessage, "unknown message type", false)
	}
}

func (s *Session) handleAudio(pcm []byte) {
	if s.state != stateCapturing {
		s.droppedAud.Add(1)
		return
	}
	s.buf = append(s.buf, pcm...)
	if len(s.buf) >= s.maxBytes {
		s.sendError(ErrUtteranceTooLong, "that was too long, answering what I heard so far", false)
		s.finishUtterance()
	}
}

func (s *Session) speechStart(played *Played) {
	switch s.state {
	case stateIdle:
		s.lateBargeIn(played)
		s.beginCapture(nil)
	case stateCapturing:
		s.beginCapture(nil) // a repeated start: begin again
	case stateResponding:
		cur := s.cur
		inWindow := s.deps.Now().Sub(s.speechEndAt) <= s.deps.Cfg.ContinuationWindow
		if cur != nil && cur.mode == inputVoice && !cur.audioQueued.Load() && inWindow {
			s.mergeContinuation()
			return
		}
		s.bargeIn(played)
		s.beginCapture(nil)
	}
}

// mergeContinuation drops the in-flight turn: the learner only paused, so their audio continues.
func (s *Session) mergeContinuation() {
	cur := s.cur
	s.out.SetTurn(0)
	cur.cancel(errContinuation)
	cur.finalized = true
	s.cur = nil
	s.turnNo--
	s.deps.Metrics.Continuation()
	s.beginCapture(s.prevPCM)
}

// bargeIn cuts the tutor off. The order matters: stale frames must stop being written before the
// turn is cancelled, and the turn is snapshotted only after it can no longer add segments.
func (s *Session) bargeIn(played *Played) {
	cur := s.cur
	if cur == nil {
		return
	}
	s.out.SetTurn(0)
	cur.cancel(errBargeIn)
	heard := 0
	if played != nil && played.TurnID == cur.id {
		heard = played.ChunkSeq + 1
	}
	s.finalize(cur, StatusInterrupted, heard)
	s.cur = nil
	s.send(TutorDone{TurnID: cur.id, Status: StatusInterrupted, Latency: cur.timing.latency(), Control: true})
	s.deps.Metrics.BargeIn("live")
}

// lateBargeIn handles speech that starts after the turn finished on the server but before the
// client played it all (TTS runs ahead of playback): the stored reply is cut to what was heard.
func (s *Session) lateBargeIn(played *Played) {
	l := s.last
	if l == nil || l.playbackDone || l.truncated || len(l.texts) == 0 {
		return
	}
	heard := 0
	if played != nil {
		if played.TurnID != l.id {
			return
		}
		heard = played.ChunkSeq + 1
	}
	if heard >= len(l.texts) {
		return
	}
	l.truncated = true
	content := strings.Join(l.texts[:heard], " ")
	for i := len(s.history) - 1; i >= 0; i-- {
		if m := &s.history[i]; m.TurnNo == l.turnNo && m.Role == entity.RoleTutor {
			m.Content, m.Status = content, entity.MessageInterrupted
			break
		}
	}
	if l.persisted {
		s.enqueue(persistOp{kind: opUpdateTutor, turnNo: l.turnNo, content: content, status: entity.MessageInterrupted})
	}
	s.deps.Metrics.BargeIn("late")
}

func (s *Session) beginCapture(initial []byte) {
	s.state = stateCapturing
	s.buf = append([]byte(nil), initial...)
	s.captureT.Reset(s.deps.Cfg.MaxUtterance + captureSlack)
}

func (s *Session) discardCapture() {
	s.state = stateIdle
	s.buf = nil
	s.captureT.Stop()
}

func (s *Session) speechEnd() {
	if s.state != stateCapturing {
		return
	}
	s.finishUtterance()
}

// finishUtterance ends capture: short audio is dropped, otherwise the audio becomes a turn.
func (s *Session) finishUtterance() {
	pcm := s.buf
	s.discardCapture()
	if len(pcm) < s.minBytes {
		return
	}
	if !s.limiter.allow(s.deps.Now()) {
		s.sendError(ErrRateLimited, "you are going too fast, wait a moment", true)
		return
	}
	s.prevPCM = pcm
	s.speechEndAt = s.deps.Now()
	s.startTurn(inputVoice, pcm, "")
}

func (s *Session) textInput(text string) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxTextRunes {
		s.sendError(ErrBadMessage, "text must be 1 to 1000 characters", false)
		return
	}
	switch s.state {
	case stateResponding:
		s.bargeIn(nil)
	case stateCapturing:
		s.discardCapture()
	}
	if !s.limiter.allow(s.deps.Now()) {
		s.sendError(ErrRateLimited, "you are going too fast, wait a moment", true)
		return
	}
	s.speechEndAt = s.deps.Now()
	s.startTurn(inputText, nil, text)
}

func (s *Session) playbackStarted(id uint32) {
	var tm *timing
	switch {
	case s.cur != nil && s.cur.id == id:
		tm = s.cur.timing
	case s.last != nil && s.last.id == id:
		tm = s.last.timing
	default:
		return
	}
	d := s.deps.Now().Sub(tm.start)
	tm.setPlayback(d)
	s.deps.Metrics.ClientFirstPlayback(s.lang.Code, d)
}

// ---- Turns -----------------------------------------------------------------------------------

func (s *Session) startTurn(mode string, pcm []byte, text string) {
	id := s.nextTurnID
	s.nextTurnID++
	s.turnNo++
	ctx, cancel := context.WithCancelCause(s.ctx)
	deadline := time.AfterFunc(s.deps.Cfg.TurnDeadline, func() { cancel(errTurnDeadline) })
	t := &turn{
		id: id, no: s.turnNo, mode: mode, pcm: pcm, text: text, ctx: ctx, cancel: cancel,
		timing: newTiming(s.deps.Now()), deps: &s.deps, out: s.out, lang: s.lang, system: s.system,
		history: usecase.BuildHistory(s.history, s.deps.Cfg.HistoryMaxMessages),
	}
	s.cur, s.state = t, stateResponding
	s.out.SetTurn(id) // before the goroutine starts, so none of its frames can be dropped as stale
	s.send(TurnStarted{TurnID: id, InputMode: mode})
	s.turns.Add(1)
	go func() {
		defer s.turns.Done()
		defer deadline.Stop()
		t.run(func(r turnResult) { s.post(evTurnDone{t: t, res: r}) })
	}()
}

func (s *Session) handleTurnDone(e evTurnDone) {
	if e.t != s.cur || e.t.finalized {
		return // a turn the learner already cut off
	}
	s.finalize(e.t, e.res.status, -1)
	s.cur = nil
	s.state = stateIdle
}

// finalize records a turn exactly once: history, the database and the "last turn" for late barge-in.
// heard is how many segments the learner heard; -1 means all of them.
func (s *Session) finalize(t *turn, status string, heard int) {
	if t.finalized {
		return
	}
	t.finalized = true
	t.timing.finish(s.deps.Now())
	learner, segs := t.snapshot()
	if heard < 0 || heard > len(segs) {
		heard = len(segs)
	}
	texts := make([]string, len(segs))
	for i, sg := range segs {
		texts[i] = sg.text
	}
	content := strings.Join(texts[:heard], " ")
	if status == StatusFailed {
		content = ""
	}
	tutorStatus := status
	if status == StatusComplete && heard < len(segs) {
		tutorStatus = StatusInterrupted
	}
	if status == StatusInterrupted && t.completed.Load() && heard >= len(segs) {
		tutorStatus = StatusComplete // it had finished and the learner heard all of it
	}
	lat := t.timing.latency()
	latJSON, _ := json.Marshal(lat)
	rec := &turnRecord{turnNo: t.no, id: t.id, texts: texts, timing: t.timing}

	if learner != "" {
		learnerMsg := entity.Message{TurnNo: t.no, Role: entity.RoleLearner, InputMode: t.mode, Content: learner, Status: entity.MessageComplete}
		if t.mode == inputVoice {
			ms := len(t.pcm) / (InputSampleRate * bytesPerSample / 1000)
			learnerMsg.AudioMs = &ms
		}
		tutorMsg := entity.Message{TurnNo: t.no, Role: entity.RoleTutor, Content: content, Status: tutorStatus, Latency: latJSON}
		s.history = append(s.history, learnerMsg, tutorMsg)
		if keep := 2 * s.deps.Cfg.HistoryMaxMessages; keep > 0 && len(s.history) > keep {
			s.history = append([]entity.Message(nil), s.history[len(s.history)-keep:]...)
		}
		rec.persisted = s.enqueue(persistOp{kind: opInsert, insert: usecase.TurnInsert{TurnNo: t.no, Learner: learnerMsg, Tutor: tutorMsg}})
	}
	s.last = rec

	s.deps.Metrics.TurnDone(tutorStatus)
	if status == StatusComplete {
		if lat.FirstAudioMs > 0 {
			s.deps.Metrics.TurnFirstAudio(s.lang.Code, t.mode, time.Duration(lat.FirstAudioMs)*time.Millisecond)
		}
		for stage, ms := range map[string]int64{"asr": lat.ASRMs, "llm_ttft": lat.LLMTTFTMs, "first_tts": lat.FirstTTSMs} {
			if ms > 0 {
				s.deps.Metrics.TurnStage(stage, s.lang.Code, time.Duration(ms)*time.Millisecond)
			}
		}
	}
	s.deps.Log.Info("turn_completed", "session_id", s.meta.ID, "turn_id", t.id, "turn_no", t.no,
		"status", tutorStatus, "input_mode", t.mode, "latency", lat)
}

// enqueue hands a write to the persister. A full queue ends the connection but keeps the session resumable.
func (s *Session) enqueue(op persistOp) bool {
	if s.persist.TryEnqueue(op) {
		return true
	}
	s.deps.Metrics.PersistFailure()
	if s.closing == nil {
		s.closing = &closeReq{code: CloseInternal, reason: "storage unavailable",
			event: ErrorEvent{Code: ErrStorageUnavailable, Message: "could not save the session, reconnect", Retryable: true}}
	}
	return false
}

// ---- Shutdown --------------------------------------------------------------------------------

// finish saves what the learner actually heard, tells the client why the session stops, and (when
// the session is over for good) ends it in the database.
func (s *Session) finish(req closeReq) CloseInfo {
	if cur := s.cur; cur != nil && !cur.finalized {
		heard := s.out.HeardSegments(cur.id)
		s.out.SetTurn(0)
		cur.cancel(errBargeIn)
		s.finalize(cur, StatusInterrupted, heard)
		s.cur = nil
	}
	if req.event != nil {
		s.out.TrySend(req.event)
	}
	close(s.done) // from here nothing reads events, so senders must not wait for it
	s.turns.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), persistDrain)
	if err := s.persist.Drain(ctx); err != nil {
		s.deps.Log.Warn("persister did not drain", "session_id", s.meta.ID, "err", err)
	}
	cancel()

	if req.endReason != "" {
		ctx, cancel := context.WithTimeout(context.Background(), endWriteTimout)
		ended, err := s.deps.Sessions.End(ctx, s.meta.ID, req.endReason, s.deps.Now())
		cancel()
		if err != nil {
			s.deps.Log.Error("end session failed", "session_id", s.meta.ID, "err", err)
		} else if ended {
			s.deps.OnEnded(s.meta.ID)
		}
	}
	return CloseInfo{Code: req.code, Reason: req.reason}
}

// ID returns the session's id.
func (s *Session) ID() uuid.UUID { return s.meta.ID }
