package importer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestWithRetry_SucceedsAfterRetries(t *testing.T) {
	var attempts int
	_, err := withRetry(context.Background(), 3, 1*time.Millisecond, func(context.Context) (int, error) {
		attempts++
		if attempts < 3 {
			return 0, errors.New("service unavailable")
		}
		return 42, nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, attempts)
}

func TestWithRetry_StopsOnNonRetryable(t *testing.T) {
	var attempts int
	_, err := withRetry(context.Background(), 5, 1*time.Millisecond, func(context.Context) (int, error) {
		attempts++
		return 0, errors.New("unauthorized")
	})
	require.Error(t, err)
	require.Equal(t, 1, attempts, "non-retryable must not retry")
}

func TestWithRetry_ZeroTimesMeansNoRetry(t *testing.T) {
	var attempts int
	_, err := withRetry(context.Background(), 0, 1*time.Millisecond, func(context.Context) (int, error) {
		attempts++
		return 0, errors.New("service unavailable")
	})
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

func TestRetryable_Matrix(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"503", errors.New("service unavailable"), true},
		{"429", errors.New("too many requests"), true},
		{"unauth", errors.New("unauthorized"), false},
		{"404", errors.New("not found"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, diff.IsRetryableRegistryErr(tc.err))
		})
	}
}
