// Package audio reads and writes the WAV files used for clips and provider payloads.
package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// DecodeWAV extracts 16-bit mono PCM and its sample rate from a RIFF/WAVE file.
// Anything else (other bit depths, stereo, compression) is rejected rather than guessed at.
func DecodeWAV(b []byte) (pcm []byte, rate int, err error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, errors.New("audio: not a RIFF/WAVE file")
	}
	var haveFmt bool
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		body := off + 8
		if size < 0 || body+size > len(b) {
			if id == "data" && haveFmt { // a streaming writer may leave the size wrong: take the rest
				size = len(b) - body
			} else {
				return nil, 0, errors.New("audio: truncated chunk")
			}
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, errors.New("audio: fmt chunk too short")
			}
			format := binary.LittleEndian.Uint16(b[body:])
			channels := binary.LittleEndian.Uint16(b[body+2:])
			rate = int(binary.LittleEndian.Uint32(b[body+4:]))
			bits := binary.LittleEndian.Uint16(b[body+14:])
			if format != 1 || channels != 1 || bits != 16 {
				return nil, 0, fmt.Errorf("audio: need 16-bit mono PCM, got format %d, %d channels, %d bits", format, channels, bits)
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, 0, errors.New("audio: data chunk before fmt chunk")
			}
			return b[body : body+size&^1], rate, nil
		}
		off = body + size + size&1 // chunks are word-aligned
	}
	return nil, 0, errors.New("audio: no data chunk")
}

// EncodePCM16WAV wraps 16-bit mono PCM in a WAV header.
func EncodePCM16WAV(pcm []byte, rate int) []byte {
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(1)) // mono
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}
