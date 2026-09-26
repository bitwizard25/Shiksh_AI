package audio

import (
	"encoding/binary"
	"math"
	"slices"
)

// Resample converts mono 16-bit PCM between sample rates by linear interpolation. It is meant
// for fixtures and tooling, not for quality-sensitive playback paths.
func Resample(pcm []byte, fromRate, toRate int) []byte {
	if fromRate == toRate || fromRate <= 0 || toRate <= 0 || len(pcm) < 2 {
		return slices.Clone(pcm)
	}
	in := len(pcm) / 2
	out := int(int64(in) * int64(toRate) / int64(fromRate))
	dst := make([]byte, 2*out)
	for i := 0; i < out; i++ {
		pos := float64(i) * float64(fromRate) / float64(toRate)
		j := int(pos)
		a := sampleAt(pcm, j)
		b := a
		if j+1 < in {
			b = sampleAt(pcm, j+1)
		}
		v := float64(a) + (float64(b)-float64(a))*(pos-float64(j))
		binary.LittleEndian.PutUint16(dst[2*i:], uint16(int16(math.Round(v))))
	}
	return dst
}

func sampleAt(pcm []byte, i int) int16 {
	return int16(binary.LittleEndian.Uint16(pcm[2*i:]))
}
