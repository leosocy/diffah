package zstdpatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

var ErrZstdBinaryMissing = errors.New("zstd binary required but unavailable")

func newErrZstdBinaryMissing(reason string) error {
	return fmt.Errorf("%w: %s", ErrZstdBinaryMissing, reason)
}

type availableCtx struct {
	once   sync.Once
	ok     bool
	reason string
}

var probeCache availableCtx

func Available(ctx context.Context) (ok bool, reason string) {
	probeCache.once.Do(func() {
		probeCache.ok, probeCache.reason = availableForTesting(ctx, exec.LookPath, runZstdVersion)
	})
	return probeCache.ok, probeCache.reason
}

func availableForTesting(
	ctx context.Context,
	lookup func(string) (string, error),
	version func(context.Context, string) (string, error),
) (ok bool, reason string) {
	path, err := lookup("zstd")
	if err != nil {
		return false, "zstd not on $PATH"
	}
	banner, err := version(ctx, path)
	if err != nil {
		return false, fmt.Sprintf("zstd --version failed: %v", err)
	}
	major, minor, matched, err := parseZstdVersion(banner)
	if err != nil {
		return false, fmt.Sprintf("zstd --version parse failed: %v", err)
	}
	if major < 1 || (major == 1 && minor < 5) {
		return false, fmt.Sprintf("zstd %s too old; need ≥1.5", matched)
	}
	return true, ""
}

func runZstdVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("zstd --version timed out")
		}
		return "", err
	}
	return out.String(), nil
}

var zstdVersionRE = regexp.MustCompile(`v?(\d+)\.(\d+)(?:\.\d+)?`)

func parseZstdVersion(banner string) (major, minor int, matched string, err error) {
	m := zstdVersionRE.FindStringSubmatch(banner)
	if m == nil {
		return 0, 0, "", fmt.Errorf("no version number in %q", firstLine(banner))
	}
	matched = m[0]
	major, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, "", err
	}
	minor, err = strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, "", err
	}
	return major, minor, matched, nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}
