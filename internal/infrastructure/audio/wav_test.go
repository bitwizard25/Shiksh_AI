package audio

import (
	"bytes"
	"testing"
)

func TestWAVRoundTrip(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	b := EncodeWAV(pcm, 22050, 1)
	if len(b) != 44+len(pcm) || string(b[:4]) != "RIFF" {
		t.Fatalf("encoded %d bytes", len(b))
	}
	got, f, err := DecodeWAV(b)
	if err != nil || f.SampleRate != 22050 || f.Channels != 1 || !bytes.Equal(got, pcm) {
		t.Fatalf("DecodeWAV = %v, %+v, %v", got, f, err)
	}
}

func TestDecodeWAVSkipsUnknownChunksAndHandlesStreamingSizes(t *testing.T) {
	pcm := []byte{9, 0, 8, 0}
	b := EncodeWAV(pcm, 16000, 1)
	// Insert an odd-sized LIST chunk (with a pad byte) before the data chunk.
	list := append([]byte("LIST\x03\x00\x00\x00abc"), 0)
	withList := append(append(append([]byte{}, b[:36]...), list...), b[36:]...)
	if got, _, err := DecodeWAV(withList); err != nil || !bytes.Equal(got, pcm) {
		t.Fatalf("with LIST chunk: %v, %v", got, err)
	}
	// A data size larger than the file (streaming writers) means "the rest of the file".
	streaming := append([]byte{}, b...)
	streaming[40], streaming[41], streaming[42], streaming[43] = 0xff, 0xff, 0xff, 0x7f
	if got, _, err := DecodeWAV(streaming); err != nil || !bytes.Equal(got, pcm) {
		t.Fatalf("streaming size: %v, %v", got, err)
	}
}

// Stereo is valid for DecodeWAV (it reports Channels); the clips loader is what insists on mono.
func TestDecodeWAVRejectsWhatItCannotPlay(t *testing.T) {
	good := EncodeWAV([]byte{0, 0}, 16000, 1)
	eightBit := append([]byte{}, good...)
	eightBit[34] = 8
	float := append([]byte{}, good...)
	float[20] = 3
	for name, b := range map[string][]byte{
		"empty": nil, "not riff": []byte("hello world, definitely not wav"), "8-bit": eightBit,
		"float": float, "no data": good[:36], "truncated header": good[:20],
	} {
		if _, _, err := DecodeWAV(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
