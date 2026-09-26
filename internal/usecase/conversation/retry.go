package conversation

import (
	"context"
	"time"
)

// Retry runs fn, retrying immediately while the error is retryable, attempts remain and less than
// budget has elapsed since the first attempt. It never sleeps between attempts: inside a spoken
// turn one immediate retry is worth more than a backoff. It returns ctx.Err() once ctx is done.
func Retry(ctx context.Context, attempts int, budget time.Duration, fn func(context.Context) error) error {
	start := time.Now()
	var err error
	for i := 0; i < max(1, attempts); i++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err = fn(ctx); err == nil || !IsRetryable(err) {
			return err
		}
		if time.Since(start) >= budget {
			return err
		}
	}
	return err
}
