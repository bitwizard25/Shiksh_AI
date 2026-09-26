package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func pcmOf(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func chunk(id string, body []byte) []byte {
	b := append([]byte(id), make([]byte, 4)...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	b = append(b, body...)
	if len(body)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func fmtBody(format, channels uint16, rate uint32, bits uint16) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b[0:], format)
	binary.LittleEndian.PutUint16(b[2:], channels)
	binary.LittleEndian.PutUint32(b[4:], rate)
	binary.LittleEndian.PutUint32(b[8:], rate*uint32(channels)*uint32(bits/8))
	binary.LittleEndian.PutUint16(b[12:], channels*bits/8)
	binary.LittleEndian.PutUint16(b[14:], bits)
	return b
}

func riff(chunks ...[]byte) []byte {
	body := []byte("WAVE")
	for _, c := range chunks {
		body = append(body, c...)
	}
	return append(append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...), body...)
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	pcm := pcmOf(0, 1000, -1000, 32767, -32768)
	wav := EncodeWAV(pcm, 16000, 1)
	if len(wav) != 44+len(pcm) || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("bad header: % x", wav[:44])
	}
	got, f, err := DecodeWAV(wav)
	if err != nil {
		t.Fatalf("DecodeWAV: %v", err)
	}
	if !bytes.Equal(got, pcm) || f != (Format{SampleRate: 16000, Channels: 1}) {
		t.Fatalf("got %v %+v", got, f)
	}
}

func TestDecodeSkipsUnknownChunksAndPadding(t *testing.T) {
	pcm := pcmOf(1, 2, 3)
	wav := riff(chunk("fmt ", fmtBody(1, 1, 22050, 16)), chunk("LIST", []byte("abc")), chunk("data", pcm))
	got, f, err := DecodeWAV(wav)
	if err != nil || !bytes.Equal(got, pcm) || f.SampleRate != 22050 {
		t.Fatalf("got %v %+v %v", got, f, err)
	}
}

func TestDecodeClampsOversizedDataChunk(t *testing.T) {
	wav := riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)))
	wav = append(wav, []byte("data")...)
	wav = binary.LittleEndian.AppendUint32(wav, 0xFFFFFFFF) // streamed WAV: size unknown
	wav = append(wav, pcmOf(5, 6, 7)...)
	wav = append(wav, 0x01) // odd trailing byte is dropped
	got, _, err := DecodeWAV(wav)
	if err != nil || !bytes.Equal(got, pcmOf(5, 6, 7)) {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]struct {
		data []byte
		want error
	}{
		"not riff":      {[]byte("hello world, not a wav"), ErrNotWAV},
		"8-bit":         {riff(chunk("fmt ", fmtBody(1, 1, 8000, 8)), chunk("data", []byte{1, 2})), ErrUnsupportedFormat},
		"float":         {riff(chunk("fmt ", fmtBody(3, 1, 16000, 32)), chunk("data", []byte{1, 2, 3, 4})), ErrUnsupportedFormat},
		"data first":    {riff(chunk("data", pcmOf(1)), chunk("fmt ", fmtBody(1, 1, 16000, 16))), nil},
		"no data chunk": {riff(chunk("fmt ", fmtBody(1, 1, 16000, 16))), nil},
	}
	for name, tc := range cases {
		_, _, err := DecodeWAV(tc.data)
		if err == nil {
			t.Errorf("%s: err = nil, want an error", name)
			continue
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestResample(t *testing.T) {
	constant := make([]int16, 1600)
	for i := range constant {
		constant[i] = 1234
	}
	in := pcmOf(constant...)
	half := Resample(in, 16000, 8000)
	if len(half) != len(in)/2 {
		t.Fatalf("len = %d, want %d", len(half), len(in)/2)
	}
	for i := 0; i < len(half); i += 2 {
		if v := int16(binary.LittleEndian.Uint16(half[i:])); v != 1234 {
			t.Fatalf("sample %d = %d, want 1234", i/2, v)
		}
	}
	same := Resample(in, 16000, 16000)
	same[0] = 0xFF
	if in[0] == 0xFF {
		t.Fatal("Resample with equal rates must return a copy")
	}
	if got := len(Resample(in, 22050, 16000)); got != 2*(1600*16000/22050) {
		t.Fatalf("22050->16000 len = %d", got)
	}
}

func TestDurationMs(t *testing.T) {
	if got := DurationMs(32000, 16000, 1); got != 1000 {
		t.Fatalf("DurationMs = %d, want 1000", got)
	}
	if got := DurationMs(100, 0, 1); got != 0 {
		t.Fatalf("zero rate = %d, want 0", got)
	}
}
