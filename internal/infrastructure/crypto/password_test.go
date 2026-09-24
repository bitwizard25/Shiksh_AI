package crypto

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHashThenVerify(t *testing.T) {
	h := NewArgon2Hasher(2)
	ctx := context.Background()
	enc, err := h.Hash(ctx, "correct horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("encoded = %q, want argon2id PHC prefix with OWASP params", enc)
	}
	if ok, err := h.Verify(ctx, "correct horse", enc); err != nil || !ok {
		t.Fatalf("Verify(correct) = %v, %v; want true", ok, err)
	}
	if ok, err := h.Verify(ctx, "wrong horse", enc); err != nil || ok {
		t.Fatalf("Verify(wrong) = %v, %v; want false", ok, err)
	}
}

func TestHashUsesRandomSalt(t *testing.T) {
	h := NewArgon2Hasher(2)
	a, _ := h.Hash(context.Background(), "same password")
	b, _ := h.Hash(context.Background(), "same password")
	if a == b {
		t.Fatal("two hashes of the same password are identical; salt is not random")
	}
}

func TestVerifyHandlesUnicodePasswords(t *testing.T) {
	h := NewArgon2Hasher(2)
	enc, err := h.Hash(context.Background(), "पासवर्ड१२३")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.Verify(context.Background(), "पासवर्ड१२३", enc); !ok {
		t.Fatal("Devanagari password did not verify")
	}
}

// TestHashNormalizesUnicode proves passwords are NFC-normalized before hashing/verifying, so a
// password typed with decomposed combining marks (e.g. from a different keyboard or OS) still
// matches its precomposed form.
func TestHashNormalizesUnicode(t *testing.T) {
	h := NewArgon2Hasher(2)
	ctx := context.Background()

	enc, err := h.Hash(ctx, "passज़word1") // decomposed (base + combining nukta)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.Verify(ctx, "passज़word1", enc); err != nil || !ok { // precomposed
		t.Fatalf("Verify(precomposed) = %v, %v; want true", ok, err)
	}

	enc2, err := h.Hash(ctx, "passज़word1") // precomposed
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.Verify(ctx, "passज़word1", enc2); err != nil || !ok { // decomposed
		t.Fatalf("Verify(decomposed) = %v, %v; want true", ok, err)
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	h := NewArgon2Hasher(1)
	for _, enc := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=19456,t=2,p=1$AAAA$AAAA",
		"$argon2id$v=18$m=19456,t=2,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=x,t=2,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=19456,t=2,p=1$!!!!$AAAA",
	} {
		if _, err := h.Verify(context.Background(), "pw", enc); err == nil {
			t.Errorf("Verify(%q) err = nil, want error", enc)
		}
	}
}

// The dummy hash backs timing equalization for unknown accounts. VerifyDummy discards errors, so
// this test is what proves the dummy hash is well formed and never matches.
func TestDummyHashIsWellFormed(t *testing.T) {
	h := NewArgon2Hasher(1)
	ok, err := h.Verify(context.Background(), "anything", h.dummy)
	if err != nil || ok {
		t.Fatalf("Verify(dummy) = %v, %v; want false, nil", ok, err)
	}
}

func TestHashRespectsContextWhenSaturated(t *testing.T) {
	h := NewArgon2Hasher(1)
	h.sem <- struct{}{} // occupy the only slot
	defer func() { <-h.sem }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash err = %v, want context.Canceled", err)
	}
}
