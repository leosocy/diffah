package zstdpatch

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fakeZstdPath = "/usr/bin/zstd"

func TestAvailable_Table(t *testing.T) {
	cases := []struct {
		name       string
		lookup     func(string) (string, error)
		version    func(context.Context, string) (string, error)
		wantOK     bool
		reasonHint string
	}{
		{
			name:       "missing binary",
			lookup:     func(string) (string, error) { return "", exec.ErrNotFound },
			wantOK:     false,
			reasonHint: "not on $PATH",
		},
		{
			name:   "supported unix banner",
			lookup: func(string) (string, error) { return "/usr/bin/zstd", nil },
			version: func(context.Context, string) (string, error) {
				return "*** zstd command line interface 64-bits v1.5.6 ***\n", nil
			},
			wantOK: true,
		},
		{
			name:   "supported chocolatey banner",
			lookup: func(string) (string, error) { return "C:\\tools\\zstd.exe", nil },
			version: func(context.Context, string) (string, error) {
				return "zstd 1.5.6\n", nil
			},
			wantOK: true,
		},
		{
			name:   "too old",
			lookup: func(string) (string, error) { return "/usr/bin/zstd", nil },
			version: func(context.Context, string) (string, error) {
				return "*** zstd v1.4.4 ***\n", nil
			},
			wantOK:     false,
			reasonHint: "1.4.4",
		},
		{
			name:   "unparseable banner",
			lookup: func(string) (string, error) { return fakeZstdPath, nil },
			version: func(context.Context, string) (string, error) {
				return "this is not a version string\n", nil
			},
			wantOK:     false,
			reasonHint: "parse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := availableForTesting(context.Background(), tc.lookup, tc.version)
			require.Equal(t, tc.wantOK, ok, "reason=%q", reason)
			if tc.reasonHint != "" {
				require.Contains(t, reason, tc.reasonHint)
			}
		})
	}
}

func TestAvailable_RealPath(t *testing.T) {
	t.Setenv("PATH", "")
	ok, reason := availableForTesting(context.Background(), exec.LookPath, runZstdVersion)
	require.False(t, ok)
	require.Contains(t, reason, "$PATH")
}

func TestErrZstdBinaryMissing_IsSentinel(t *testing.T) {
	wrapped := newErrZstdBinaryMissing("zstd 1.4.4 too old; need ≥1.5")
	require.True(t, errors.Is(wrapped, ErrZstdBinaryMissing))
	require.Contains(t, wrapped.Error(), "1.4.4")
}

func TestRunZstdVersion_TimeoutWrapsCtxErr(t *testing.T) {
	// Simulate a slow binary by pointing at `sleep`.
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not on $PATH")
	}
	// Shorten the timeout by swapping the function out of context's way:
	// runZstdVersion has a 1s timeout; we invoke with a pre-cancelled ctx
	// to force the timeout branch deterministically.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // ensure ctx is already dead

	_, err = runZstdVersion(ctx, sleep) // will attempt `sleep --version`
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded),
		"expected errors.Is(err, context.DeadlineExceeded); got %v", err)
}
