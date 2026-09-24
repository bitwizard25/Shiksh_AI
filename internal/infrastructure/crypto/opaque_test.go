package crypto

import (
	"bytes"
	"testing"
)

func TestOpaqueTokens(t *testing.T) {
	var o Opaque
	a, ha, err := o.New()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := o.New()
	if a == b {
		t.Fatal("two opaque tokens are equal")
	}
	if len(a) != 43 {
		t.Errorf("len(token) = %d, want 43 (32 bytes base64url)", len(a))
	}
	if len(ha) != 32 || !bytes.Equal(ha, o.Hash(a)) {
		t.Errorf("hash mismatch: %x vs %x", ha, o.Hash(a))
	}
}
