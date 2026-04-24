package importer

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

// withRetry re-invokes op up to times+1 total times (1 original + times
// retries) with exponential backoff (or fixed delay if set). Only fires
// for errors that retryable() marks as transient.
func withRetry[T any](ctx context.Context, times int, delay time.Duration,
	op func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		v, err := op(ctx)
		if err == nil {
			return v, nil
		}
		if attempt >= times || !retryable(err) {
			return zero, err
		}
		d := delay
		if d == 0 {
			d = time.Duration(1<<attempt) * 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(d):
		}
	}
}

// retryable returns true when err suggests a transient failure: HTTP
// 429/5xx (recognised by substring), connection-refused / EOF, or wrapped
// net.OpError / url.Error. Everything else is surfaced immediately.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"too many requests",    // 429
		"service unavailable",  // 503
		"bad gateway",          // 502
		"gateway timeout",      // 504
		"internal server error", // 500
		"eof",
		"connection reset",
		"connection refused",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	return false
}
