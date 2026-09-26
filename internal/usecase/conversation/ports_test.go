package conversation_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

func TestProviderErrorFormatsAndUnwraps(t *testing.T) {
	cause := errors.New("boom")
	e := &conversation.ProviderError{Provider: "bhashini", Op: "tts", Status: 503, Retryable: true, Err: cause}
	if !strings.Contains(e.Error(), "bhashini tts") || !strings.Contains(e.Error(), "503") || !strings.Contains(e.Error(), "boom") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Fatal("ProviderError does not unwrap to its cause")
	}
	noStatus := &conversation.ProviderError{Provider: "gemini", Op: "llm", Err: cause}
	if strings.Contains(noStatus.Error(), "status") {
		t.Fatalf("Error() without status = %q", noStatus.Error())
	}
}

func TestIsRetryable(t *testing.T) {
	retry := &conversation.ProviderError{Provider: "p", Op: "o", Retryable: true, Err: errors.New("x")}
	final := &conversation.ProviderError{Provider: "p", Op: "o", Err: errors.New("x")}
	if !conversation.IsRetryable(retry) || !conversation.IsRetryable(fmt.Errorf("wrapped: %w", retry)) {
		t.Fatal("retryable provider error not detected")
	}
	if conversation.IsRetryable(final) || conversation.IsRetryable(errors.New("plain")) || conversation.IsRetryable(nil) {
		t.Fatal("non-retryable error reported as retryable")
	}
}
