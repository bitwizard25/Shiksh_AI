package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

var errRetry = &conversation.ProviderError{Provider: "p", Op: "o", Retryable: true, Err: errors.New("try again")}

func counting(errs ...error) (func(context.Context) error, *int) {
	calls := 0
	return func(context.Context) error {
		calls++
		if calls <= len(errs) {
			return errs[calls-1]
		}
		return nil
	}, &calls
}

func TestRetry(t *testing.T) {
	ctx := context.Background()
	final := errors.New("final")

	fn, calls := counting()
	if err := conversation.Retry(ctx, 2, time.Second, fn); err != nil || *calls != 1 {
		t.Errorf("success: err=%v calls=%d", err, *calls)
	}
	fn, calls = counting(errRetry)
	if err := conversation.Retry(ctx, 2, time.Second, fn); err != nil || *calls != 2 {
		t.Errorf("retry then success: err=%v calls=%d", err, *calls)
	}
	fn, calls = counting(final)
	if err := conversation.Retry(ctx, 3, time.Second, fn); !errors.Is(err, final) || *calls != 1 {
		t.Errorf("non-retryable: err=%v calls=%d", err, *calls)
	}
	fn, calls = counting(errRetry, errRetry, errRetry)
	if err := conversation.Retry(ctx, 2, time.Second, fn); !errors.Is(err, errRetry) || *calls != 2 {
		t.Errorf("attempts exhausted: err=%v calls=%d", err, *calls)
	}
}

func TestRetryStopsWhenBudgetIsSpent(t *testing.T) {
	calls := 0
	slow := func(context.Context) error {
		calls++
		time.Sleep(20 * time.Millisecond)
		return errRetry
	}
	if err := conversation.Retry(context.Background(), 3, 10*time.Millisecond, slow); !errors.Is(err, errRetry) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one call once the budget is spent", err, calls)
	}
}

func TestRetryHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fn, calls := counting()
	if err := conversation.Retry(ctx, 2, time.Second, fn); !errors.Is(err, context.Canceled) || *calls != 0 {
		t.Fatalf("err=%v calls=%d", err, *calls)
	}
}
