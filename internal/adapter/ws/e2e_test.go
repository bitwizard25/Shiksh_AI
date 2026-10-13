package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/adapter/gateway/fake"
	"github.com/bitwizard25/Shiksh_AI/internal/adapter/repository"
	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/crypto"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// rig is the real realtime stack (Postgres, hub, handler) with fake providers behind a test server.
type rig struct {
	srv      *httptest.Server
	sessions *usecase.Sessions
	hub      *conversation.Hub
	users    *repository.Users
	repo     *repository.Sessions
	msgs     *repository.Messages
	userID   uuid.UUID
}

func newRig(t *testing.T, mutate func(*conversation.Config)) *rig {
	t.Helper()
	pool := dbtest.NewPool(t)
	r := &rig{users: repository.NewUsers(pool), repo: repository.NewSessions(pool), msgs: repository.NewMessages(pool)}
	r.sessions = usecase.NewSessions(usecase.SessionsDeps{
		Users: r.users, Sessions: r.repo, Messages: r.msgs, Tickets: repository.NewTickets(pool),
		Tx: database.NewTxManager(pool), Opaque: crypto.Opaque{},
	}, usecase.SessionsConfig{TicketTTL: time.Minute}, nil)

	cfg := conversation.DefaultConfig()
	cfg.FillerAfter = 0
	cfg.IdleTimeout = time.Minute
	if mutate != nil {
		mutate(&cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r.hub = conversation.NewHub(conversation.Deps{
		ASR: fake.NewASR(fake.Options{ASRText: "what is a fraction"}), LLM: fake.NewLLM(fake.Options{LLMFirstToken: 10 * time.Millisecond, LLMPerToken: time.Millisecond}),
		TTS: fake.NewTTS(fake.Options{TTSDelay: 5 * time.Millisecond}), Clips: fake.Clips{},
		Store: repository.NewTurns(pool), Users: r.users, Sessions: r.repo, Messages: r.msgs, Cfg: cfg, Log: log,
	})
	mux := http.NewServeMux()
	mux.Handle("GET /v1/ws", NewHandler(Options{Hub: r.hub, Tickets: r.sessions, Log: log}))
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)

	u, err := r.users.Create(t.Context(), usecase.NewUser{Email: entity.Email(uuid.NewString() + "@example.com"), PasswordHash: "x", DisplayName: "Asha", PreferredLang: "en", AcceptedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	r.userID = u.ID
	return r
}

func (r *rig) newSession(t *testing.T) usecase.StartedSession {
	t.Helper()
	s, err := r.sessions.Create(t.Context(), r.userID, usecase.SessionInput{Subject: "math", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (r *rig) url(ticket string) string {
	return "ws" + strings.TrimPrefix(r.srv.URL, "http") + "/v1/ws?ticket=" + ticket
}

func (r *rig) dial(t *testing.T, ticket string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, r.url(ticket), &websocket.DialOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

type msg struct {
	typ  websocket.MessageType
	json map[string]any
	pcm  conversation.AudioFrame
}

func (m msg) kind() string {
	if m.typ == websocket.MessageBinary {
		return "audio"
	}
	s, _ := m.json["type"].(string)
	return s
}

func read(t *testing.T, c *websocket.Conn) (msg, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		return msg{}, err
	}
	if typ == websocket.MessageBinary {
		f, err := DecodeAudio(data)
		if err != nil {
			t.Fatal(err)
		}
		return msg{typ: typ, pcm: f}, nil
	}
	m := msg{typ: typ}
	if err := json.Unmarshal(data, &m.json); err != nil {
		t.Fatalf("bad JSON %s", data)
	}
	return m, nil
}

func mustRead(t *testing.T, c *websocket.Conn) msg {
	t.Helper()
	m, err := read(t, c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.Write(t.Context(), websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readUntil collects messages up to and including the first of the given kind.
func readUntil(t *testing.T, c *websocket.Conn, kind string) []msg {
	t.Helper()
	var out []msg
	for {
		m := mustRead(t, c)
		out = append(out, m)
		if m.kind() == kind {
			return out
		}
	}
}

func kinds(ms []msg) []string {
	var out []string
	for _, m := range ms {
		k := m.kind()
		if k == "audio" && len(out) > 0 && out[len(out)-1] == "audio" {
			continue
		}
		out = append(out, k)
	}
	return out
}

func TestEndToEndTextAndVoiceTurns(t *testing.T) {
	r := newRig(t, nil)
	s := r.newSession(t)
	c := r.dial(t, s.Ticket.Token)

	ready := mustRead(t, c)
	if ready.kind() != "session.ready" || ready.json["language"] != "en" || ready.json["resumed"] != false || ready.json["next_turn_id"] != 1.0 {
		t.Fatalf("ready = %v", ready.json)
	}

	// A typed turn.
	send(t, c, map[string]any{"type": "text.input", "text": "what is a fraction"})
	turn := readUntil(t, c, "tutor.done")
	got := kinds(turn)
	want := []string{"turn.started", "transcript", "tutor.segment", "audio", "tutor.segment", "audio", "tutor.segment", "audio", "tutor.done"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("typed turn trace:\n got %v\nwant %v", got, want)
	}
	var frames []conversation.AudioFrame
	for _, m := range turn {
		if m.kind() == "audio" {
			frames = append(frames, m.pcm)
		}
	}
	last := frames[len(frames)-1]
	if frames[0].Kind != conversation.KindSpeech || frames[0].TurnID != 1 || frames[0].SampleRate != fake.SampleRate || !last.LastOfSeg || !last.LastOfTurn || last.ChunkSeq != 2 {
		t.Fatalf("frames: first %+v last %+v", frames[0], last)
	}
	if done := turn[len(turn)-1].json; done["status"] != "complete" || done["latency"].(map[string]any)["first_audio_ms"].(float64) <= 0 {
		t.Fatalf("tutor.done = %v", done)
	}
	send(t, c, map[string]any{"type": "playback.done", "turn_id": 1})

	// A spoken turn: 1 s of audio in 100 ms frames.
	send(t, c, map[string]any{"type": "speech.start"})
	for range 10 {
		if err := c.Write(t.Context(), websocket.MessageBinary, make([]byte, 3200)); err != nil {
			t.Fatal(err)
		}
	}
	send(t, c, map[string]any{"type": "speech.end"})
	voice := readUntil(t, c, "tutor.done")
	if got := kinds(voice); got[0] != "turn.started" || voice[0].json["input_mode"] != "voice" || voice[0].json["turn_id"] != 2.0 || got[len(got)-1] != "tutor.done" {
		t.Fatalf("voice turn trace = %v", got)
	}

	send(t, c, map[string]any{"type": "session.end"})
	var tail []msg
	for {
		m, err := read(t, c)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("close status = %v (%v)", websocket.CloseStatus(err), err)
			}
			break
		}
		tail = append(tail, m)
	}
	if len(tail) != 1 || tail[0].kind() != "session.ended" || tail[0].json["reason"] != "learner" {
		t.Fatalf("tail = %+v", tail)
	}

	// Everything was stored, and the session is over.
	sess, err := r.repo.Get(t.Context(), s.Session.ID)
	if err != nil || !sess.Ended() || sess.EndReason != entity.EndLearner || sess.TurnCount != 2 {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	msgs, _ := r.msgs.List(t.Context(), s.Session.ID)
	if len(msgs) != 4 || msgs[0].InputMode != entity.InputText || msgs[2].InputMode != entity.InputVoice || *msgs[2].AudioMs != 1000 ||
		!strings.Contains(msgs[1].Content, "You asked: what is a fraction.") || msgs[1].Status != entity.MessageComplete {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestHandshakeChecksComeBeforeTheTicketIsRedeemed(t *testing.T) {
	r := newRig(t, nil)
	s := r.newSession(t)

	// No subprotocol: 400, and the ticket survives.
	resp, err := http.Get(r.srv.URL + "/v1/ws?ticket=" + s.Ticket.Token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no subprotocol status = %d, want 400", resp.StatusCode)
	}

	// Unknown ticket: 401.
	_, resp2, err := websocket.Dial(t.Context(), r.url("nonsense"), &websocket.DialOptions{Subprotocols: []string{Subprotocol}})
	if err == nil || resp2 == nil || resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad ticket: err=%v resp=%v", err, resp2)
	}

	// The real ticket still works once, and only once.
	c := r.dial(t, s.Ticket.Token)
	if mustRead(t, c).kind() != "session.ready" {
		t.Fatal("no session.ready")
	}
	_, resp3, err := websocket.Dial(t.Context(), r.url(s.Ticket.Token), &websocket.DialOptions{Subprotocols: []string{Subprotocol}})
	if err == nil || resp3 == nil || resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused ticket: err=%v resp=%v", err, resp3)
	}
}

func TestMalformedMessagesGetBadMessageErrors(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t, r.newSession(t).Ticket.Token)
	mustRead(t, c)
	for _, raw := range []string{`not json`, `{"type":"fly"}`, `{"type":"speech.end","bogus":1}`} {
		if err := c.Write(t.Context(), websocket.MessageText, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		m := mustRead(t, c)
		if m.kind() != "error" || m.json["code"] != "bad_message" {
			t.Fatalf("%s -> %v", raw, m.json)
		}
	}
	send(t, c, map[string]any{"type": "ping", "t": 5})
	if m := mustRead(t, c); m.kind() != "pong" || m.json["t"] != 5.0 {
		t.Fatalf("pong = %v", m.json)
	}
}

func TestNewConnectionReplacesTheOldOne(t *testing.T) {
	r := newRig(t, nil)
	s := r.newSession(t)
	first := r.dial(t, s.Ticket.Token)
	mustRead(t, first)
	send(t, first, map[string]any{"type": "text.input", "text": "hello"})
	readUntil(t, first, "tutor.done")

	tk, err := r.sessions.NewTicket(t.Context(), r.userID, s.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := r.dial(t, tk.Token)
	ready := mustRead(t, second)
	if ready.json["resumed"] != true || ready.json["next_turn_id"] != 2.0 || len(ready.json["history"].([]any)) != 2 {
		t.Fatalf("second ready = %v", ready.json)
	}

	var sawReplaced bool
	for {
		m, err := read(t, first)
		if err != nil {
			if websocket.CloseStatus(err) != 4009 {
				t.Fatalf("first close status = %v (%v)", websocket.CloseStatus(err), err)
			}
			break
		}
		if m.kind() == "error" && m.json["code"] == "session_replaced" {
			sawReplaced = true
		}
	}
	if !sawReplaced {
		t.Fatal("the old connection was not told it was replaced")
	}
	send(t, second, map[string]any{"type": "text.input", "text": "again"})
	readUntil(t, second, "tutor.done")
	// Turns are saved in the background, so wait for the write instead of assuming it already happened.
	var msgs []entity.Message
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if msgs, _ = r.msgs.List(t.Context(), s.Session.ID); len(msgs) == 4 {
			break
		}
	}
	if len(msgs) != 4 || msgs[2].TurnNo != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestDrainClosesLiveConnectionsWithGoingAway(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t, r.newSession(t).Ticket.Token)
	mustRead(t, c)
	if err := r.hub.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var sawShutdown bool
	for {
		m, err := read(t, c)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusGoingAway {
				t.Fatalf("close status = %v (%v)", websocket.CloseStatus(err), err)
			}
			break
		}
		if m.kind() == "error" && m.json["code"] == "server_shutdown" && m.json["retryable"] == true {
			sawShutdown = true
		}
	}
	if !sawShutdown {
		t.Fatal("no server_shutdown error")
	}
	resp, err := http.Get(r.srv.URL + "/v1/ws?ticket=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Without the subprotocol the request is rejected before the drain check.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_, resp2, err := websocket.Dial(t.Context(), r.url("x"), &websocket.DialOptions{Subprotocols: []string{Subprotocol}})
	if err == nil || resp2 == nil || resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("connect while draining: err=%v resp=%v", err, resp2)
	}
}

func TestIdleTimeoutClosesWith4008(t *testing.T) {
	r := newRig(t, func(c *conversation.Config) { c.IdleTimeout = 300 * time.Millisecond })
	s := r.newSession(t)
	c := r.dial(t, s.Ticket.Token)
	mustRead(t, c)
	var ended bool
	for {
		m, err := read(t, c)
		if err != nil {
			if websocket.CloseStatus(err) != 4008 {
				t.Fatalf("close status = %v (%v)", websocket.CloseStatus(err), err)
			}
			break
		}
		ended = ended || (m.kind() == "session.ended" && m.json["reason"] == "idle")
	}
	if !ended {
		t.Fatal("no session.ended{idle}")
	}
	if sess, _ := r.repo.Get(t.Context(), s.Session.ID); sess.Ended() {
		t.Fatal("an idle disconnect ended the session; it must stay resumable")
	}
}

func TestClientDisconnectKeepsSessionResumable(t *testing.T) {
	r := newRig(t, nil)
	s := r.newSession(t)
	c := r.dial(t, s.Ticket.Token)
	mustRead(t, c)
	send(t, c, map[string]any{"type": "text.input", "text": "hello"})
	readUntil(t, c, "tutor.done")
	c.CloseNow()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := hubLive(r.hub, s.Session.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the hub still lists the session after the client left")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sess, _ := r.repo.Get(t.Context(), s.Session.ID)
	if sess.Ended() || sess.TurnCount != 1 {
		t.Fatalf("session = %+v", sess)
	}
}

// hubLive reports whether the hub considers the session live, using the public End probe's inverse
// without ending anything: End on a missing session is a no-op returning false.
func hubLive(h *conversation.Hub, id uuid.UUID) (struct{}, bool) {
	return struct{}{}, h.Live(id)
}
