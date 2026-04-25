package importer

import (
	"context"
	"time"

	"github.com/leosocy/diffah/pkg/diff"
)

// maxExponentialBackoff caps the computed delay when the caller leaves
// RetryDelay at zero (exponential mode). At RetryTimes=10 the uncapped
// backoff reaches ~51 s, which is longer than any user wants to wait
// mid-command; 30 s keeps a retry loop painful-but-finite.
const maxExponentialBackoff = 30 * time.Second

// withRetry re-invokes op up to times+1 total times (1 original + times
// retries) with exponential backoff (or fixed delay if set). Only fires
// for errors that diff.IsRetryableRegistryErr marks as transient.
func withRetry[T any](ctx context.Context, times int, delay time.Duration,
	op func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		v, err := op(ctx)
		if err == nil {
			return v, nil
		}
		if attempt >= times || !diff.IsRetryableRegistryErr(err) {
			return zero, err
		}
		d := delay
		if d == 0 {
			d = time.Duration(1<<attempt) * 100 * time.Millisecond
			if d > maxExponentialBackoff {
				d = maxExponentialBackoff
			}
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(d):
		}
	}
}
