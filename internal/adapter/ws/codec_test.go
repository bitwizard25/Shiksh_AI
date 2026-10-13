package ws

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/google/go-cmp/cmp"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

func jsonOf(t *testing.T, e conversation.Event) map[string]any {
	t.Helper()
	typ, data, err := Encode(e)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("Encode(%T) = %v, %v", e, typ, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not JSON: %s", data)
	}
	return m
}

func TestEncodeControlEvents(t *testing.T) {
	id := uint32(7)
	cases := []struct {
		e    conversation.Event
		want map[string]any
	}{
		{conversation.TurnStarted{TurnID: 3, InputMode: "voice"}, map[string]any{"type": "turn.started", "turn_id": 3.0, "input_mode": "voice"}},
		{conversation.Transcript{TurnID: 3, Text: "नमस्ते"}, map[string]any{"type": "transcript", "turn_id": 3.0, "text": "नमस्ते"}},
		{conversation.TranscriptEmpty{TurnID: 3}, map[string]any{"type": "transcript.empty", "turn_id": 3.0}},
		{conversation.TutorSegment{TurnID: 3, ChunkSeq: 1, Text: "hi", SampleRate: 22050, NoAudio: true}, map[string]any{"type": "tutor.segment", "turn_id": 3.0, "chunk_seq": 1.0, "text": "hi", "sample_rate": 22050.0}},
		{conversation.ErrorEvent{Code: "tts_failed", Message: "m", Retryable: false, TurnID: &id}, map[string]any{"type": "error", "code": "tts_failed", "message": "m", "retryable": false, "turn_id": 7.0}},
		{conversation.ErrorEvent{Code: "rate_limited", Message: "m", Retryable: true}, map[string]any{"type": "error", "code": "rate_limited", "message": "m", "retryable": true}},
		{conversation.SessionEnded{Reason: "idle"}, map[string]any{"type": "session.ended", "reason": "idle"}},
		{conversation.Pong{T: json.RawMessage(`42`)}, map[string]any{"type": "pong", "t": 42.0}},
		{conversation.Pong{}, map[string]any{"type": "pong"}},
	}
	for _, c := range cases {
		if diff := cmp.Diff(c.want, jsonOf(t, c.e)); diff != "" {
			t.Errorf("%T mismatch (-want +got):\n%s", c.e, diff)
		}
	}
}

func TestEncodeTutorDoneAndReady(t *testing.T) {
	done := jsonOf(t, conversation.TutorDone{TurnID: 2, Status: "interrupted", Latency: conversation.Latency{FirstAudioMs: 900, Filler: true}, Control: true})
	lat, _ := done["latency"].(map[string]any)
	if done["type"] != "tutor.done" || done["status"] != "interrupted" || lat["first_audio_ms"] != 900.0 || lat["filler"] != true || done["Control"] != nil {
		t.Fatalf("tutor.done = %v", done)
	}
	ready := jsonOf(t, conversation.SessionReady{
		SessionID: "s", Language: "hi", NextTurnID: 4, History: []conversation.HistoryItem{},
		AudioIn:  conversation.AudioIn{Encoding: "pcm_s16le", SampleRate: 16000, Channels: 1},
		AudioOut: conversation.AudioOut{Encoding: "pcm_s16le", Channels: 1},
	})
	in, _ := ready["audio_in"].(map[string]any)
	if ready["type"] != "session.ready" || ready["next_turn_id"] != 4.0 || in["sample_rate"] != 16000.0 || ready["history"] == nil {
		t.Fatalf("session.ready = %v", ready)
	}
}

func TestAudioFrameHeader(t *testing.T) {
	f := conversation.AudioFrame{Kind: conversation.KindSpeech, LastOfSeg: true, LastOfTurn: true, TurnID: 0x01020304, ChunkSeq: 5, Part: 9, SampleRate: 22050, PCM: []byte{1, 2, 3, 4}}
	typ, b, err := Encode(f)
	if err != nil || typ != websocket.MessageBinary {
		t.Fatalf("Encode = %v, %v", typ, err)
	}
	wantHeader := []byte{1, 0b11, 1, 2, 3, 4, 0, 5, 0, 9, 0x56, 0x22}
	if string(b[:HeaderSize]) != string(wantHeader) || string(b[HeaderSize:]) != "\x01\x02\x03\x04" {
		t.Fatalf("frame = %x, want header %x", b, wantHeader)
	}
	got, err := DecodeAudio(b)
	if err != nil || cmp.Diff(f, got) != "" {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if _, err := DecodeAudio(b[:5]); err == nil {
		t.Fatal("short frame accepted")
	}
	flags := EncodeAudio(conversation.AudioFrame{LastOfSeg: true})[1]
	if flags != 1 {
		t.Fatalf("segment-only flags = %b", flags)
	}
}

func TestDecodeClient(t *testing.T) {
	good := map[string]conversation.ClientMsg{
		`{"type":"speech.start"}`:                                      {Type: "speech.start"},
		`{"type":"speech.start","played":{"turn_id":3,"chunk_seq":1}}`: {Type: "speech.start", Played: &conversation.Played{TurnID: 3, ChunkSeq: 1}},
		`{"type":"text.input","text":"हेलो"}`:                          {Type: "text.input", Text: "हेलो"},
		`{"type":"playback.started","turn_id":9}`:                      {Type: "playback.started", TurnID: 9},
		`{"type":"ping","t":1700000000}`:                               {Type: "ping", T: json.RawMessage(`1700000000`)},
	}
	for in, want := range good {
		got, err := DecodeClient([]byte(in))
		if err != nil || cmp.Diff(want, got) != "" {
			t.Errorf("DecodeClient(%s) = %+v, %v", in, got, err)
		}
	}
	for _, in := range []string{
		``, `not json`, `{}`, `{"type":"bogus"}`, `{"type":"speech.end","extra":1}`, `{"type":"speech.end"} {"type":"speech.end"}`,
		`{"type":"ping","t":"` + strings.Repeat("x", 100) + `"}`, `{"type":"playback.done","turn_id":-1}`, `[]`,
	} {
		if _, err := DecodeClient([]byte(in)); err == nil {
			t.Errorf("DecodeClient(%q) accepted", in)
		}
	}
}
