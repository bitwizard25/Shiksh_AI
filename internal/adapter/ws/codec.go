// Package ws is the WebSocket interface adapter: the wire codec, the outbox with its stale-frame
// drop rule, and the HTTP handler that upgrades a ticketed request and hands the connection to the
// conversation hub. The wire format is documented in docs/ws-protocol.md.
package ws

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/coder/websocket"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// Subprotocol is the WebSocket subprotocol clients must offer.
const Subprotocol = "shiksha.v1"

// HeaderSize is the length of the binary frame header that precedes PCM.
const HeaderSize = 12

// Frame flag bits.
const (
	flagLastOfSegment = 1 << 0
	flagLastOfTurn    = 1 << 1
)

// Encode turns a server event into a WebSocket message: JSON with a "type" field for control
// events, or a binary frame (12-byte header, then PCM) for audio.
func Encode(e conversation.Event) (websocket.MessageType, []byte, error) {
	if f, ok := e.(conversation.AudioFrame); ok {
		return websocket.MessageBinary, EncodeAudio(f), nil
	}
	name, ok := eventName(e)
	if !ok {
		return 0, nil, fmt.Errorf("ws: cannot encode %T", e)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return 0, nil, err
	}
	// Splice the type in front of the fields: {"type":"x", ...rest}
	out := make([]byte, 0, len(body)+len(name)+12)
	out = append(out, `{"type":"`...)
	out = append(out, name...)
	out = append(out, '"')
	if len(body) > 2 { // not just "{}"
		out = append(out, ',')
		out = append(out, body[1:]...)
	} else {
		out = append(out, '}')
	}
	return websocket.MessageText, out, nil
}

func eventName(e conversation.Event) (string, bool) {
	switch e.(type) {
	case conversation.SessionReady:
		return "session.ready", true
	case conversation.TurnStarted:
		return "turn.started", true
	case conversation.Transcript:
		return "transcript", true
	case conversation.TranscriptEmpty:
		return "transcript.empty", true
	case conversation.TutorSegment:
		return "tutor.segment", true
	case conversation.TutorDone:
		return "tutor.done", true
	case conversation.ErrorEvent:
		return "error", true
	case conversation.SessionEnded:
		return "session.ended", true
	case conversation.Pong:
		return "pong", true
	}
	return "", false
}

// EncodeAudio builds a binary downlink frame.
//
//	[0] kind  [1] flags  [2..5] turn_id  [6..7] chunk_seq  [8..9] part  [10..11] sample_rate  [12..] PCM
func EncodeAudio(f conversation.AudioFrame) []byte {
	b := make([]byte, HeaderSize+len(f.PCM))
	b[0] = f.Kind
	if f.LastOfSeg {
		b[1] |= flagLastOfSegment
	}
	if f.LastOfTurn {
		b[1] |= flagLastOfTurn
	}
	binary.BigEndian.PutUint32(b[2:], f.TurnID)
	binary.BigEndian.PutUint16(b[6:], f.ChunkSeq)
	binary.BigEndian.PutUint16(b[8:], f.Part)
	binary.BigEndian.PutUint16(b[10:], uint16(f.SampleRate))
	copy(b[HeaderSize:], f.PCM)
	return b
}

// DecodeAudio parses a binary downlink frame. Clients do this; the server uses it in tests.
func DecodeAudio(b []byte) (conversation.AudioFrame, error) {
	if len(b) < HeaderSize {
		return conversation.AudioFrame{}, errors.New("ws: audio frame shorter than its header")
	}
	return conversation.AudioFrame{
		Kind:       b[0],
		LastOfSeg:  b[1]&flagLastOfSegment != 0,
		LastOfTurn: b[1]&flagLastOfTurn != 0,
		TurnID:     binary.BigEndian.Uint32(b[2:]),
		ChunkSeq:   binary.BigEndian.Uint16(b[6:]),
		Part:       binary.BigEndian.Uint16(b[8:]),
		SampleRate: int(binary.BigEndian.Uint16(b[10:])),
		PCM:        b[HeaderSize:],
	}, nil
}

// MsgInvalid is the type the decoder gives a message it could not understand; the session answers it with bad_message.
const MsgInvalid = "invalid"

type clientJSON struct {
	Type   string               `json:"type"`
	Played *conversation.Played `json:"played"`
	Text   string               `json:"text"`
	TurnID uint32               `json:"turn_id"`
	T      json.RawMessage      `json:"t"`
}

var knownClientTypes = map[string]bool{
	conversation.MsgSpeechStart: true, conversation.MsgSpeechEnd: true, conversation.MsgSpeechCancel: true,
	conversation.MsgTextInput: true, conversation.MsgPlaybackStarted: true, conversation.MsgPlaybackDone: true,
	conversation.MsgSessionEnd: true, conversation.MsgPing: true,
}

// DecodeClient strictly parses a client text message: unknown fields, unknown types and trailing data are errors.
func DecodeClient(data []byte) (conversation.ClientMsg, error) {
	var m clientJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return conversation.ClientMsg{}, err
	}
	if dec.More() {
		return conversation.ClientMsg{}, errors.New("ws: trailing data after the message")
	}
	if !knownClientTypes[m.Type] {
		return conversation.ClientMsg{}, fmt.Errorf("ws: unknown message type %q", m.Type)
	}
	if len(m.T) > 64 { // echoed back in the pong: keep it small
		return conversation.ClientMsg{}, errors.New("ws: ping payload too large")
	}
	return conversation.ClientMsg{Type: m.Type, Played: m.Played, Text: m.Text, TurnID: m.TurnID, T: m.T}, nil
}
