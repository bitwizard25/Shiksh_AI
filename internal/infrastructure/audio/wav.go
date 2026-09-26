// Package audio encodes and decodes 16-bit PCM WAV and resamples mono PCM.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var (
	ErrNotWAV            = errors.New("audio: not a RIFF/WAVE file")
	ErrUnsupportedFormat = errors.New("audio: only 16-bit integer PCM is supported")
)

// Format describes decoded PCM.
type Format struct {
	SampleRate int
	Channels   int
}

// EncodeWAV wraps little-endian 16-bit PCM in a canonical 44-byte WAV header.
func EncodeWAV(pcm []byte, sampleRate, channels int) []byte {
	buf := make([]byte, 44+len(pcm))
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+len(pcm)))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1) // integer PCM
	binary.LittleEndian.PutUint16(buf[22:], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(sampleRate*channels*2))
	binary.LittleEndian.PutUint16(buf[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(len(pcm)))
	copy(buf[44:], pcm)
	return buf
}

// DecodeWAV returns the samples and format of a 16-bit PCM WAV file. Unknown chunks (LIST, fact,
// ...) are skipped. A data chunk whose declared size runs past the end of the file (streamed WAV)
// is clamped to the bytes present.
func DecodeWAV(data []byte) ([]byte, Format, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, Format{}, ErrNotWAV
	}
	var (
		f       Format
		haveFmt bool
	)
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4:]))
		body := off + 8
		if id == "data" && haveFmt && body+size > len(data) {
			end := len(data) - (len(data)-body)%2
			return data[body:end], f, nil
		}
		if body+size > len(data) {
			return nil, Format{}, fmt.Errorf("audio: truncated %q chunk", id)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, Format{}, errors.New("audio: short fmt chunk")
			}
			format := binary.LittleEndian.Uint16(data[body:])
			f.Channels = int(binary.LittleEndian.Uint16(data[body+2:]))
			f.SampleRate = int(binary.LittleEndian.Uint32(data[body+4:]))
			bits := binary.LittleEndian.Uint16(data[body+14:])
			if (format != 1 && format != 0xFFFE) || bits != 16 || f.Channels < 1 || f.SampleRate < 1 {
				return nil, Format{}, ErrUnsupportedFormat
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, Format{}, errors.New("audio: data chunk before fmt chunk")
			}
			return data[body : body+size], f, nil
		}
		off = body + size + size%2 // chunks are word-aligned
	}
	return nil, Format{}, errors.New("audio: no data chunk")
}

// DurationMs returns the playback length of 16-bit PCM.
func DurationMs(pcmBytes, sampleRate, channels int) int {
	if sampleRate <= 0 || channels <= 0 {
		return 0
	}
	return pcmBytes * 1000 / (sampleRate * channels * 2)
}
