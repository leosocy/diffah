package zstdpatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

var ErrZstdBinaryMissing = &zstdErr{
	msg:    "zstd binary required but unavailable",
	action: "install zstd 1.5+ (brew install zstd / apt install zstd)",
}

var ErrZstdEncodeFailure = &zstdErr{
	msg:    "zstd encode failed",
	action: "re-run with --log-level=debug for zstd stderr capture",
}

type zstdErr struct {
	msg    string
	action string
}

func (e *zstdErr) Error() string           { return e.msg }
func (e *zstdErr) Category() errs.Category { return errs.CategoryEnvironment }
func (e *zstdErr) NextAction() string      { return e.action }

func newErrZstdBinaryMissing(reason string) error {
	return fmt.Errorf("%w: %s", ErrZstdBinaryMissing, reason)
}

// Available reports whether zstd >= 1.5 is usable for patch-from
// encode/decode. Each call does a fresh LookPath + `zstd --version`;
// callers invoke Available at most once per top-level operation, so
// process-wide caching isn't worth the concurrency hazard.
func Available(ctx context.Context) (ok bool, reason string) {
	ok, _, reason = availableDetailForTesting(ctx, exec.LookPath, runZstdVersion)
	return ok, reason
}

func AvailableDetail(ctx context.Context) (ok bool, detail string) {
	ok, detail, _ = availableDetailForTesting(ctx, exec.LookPath, runZstdVersion)
	return ok, detail
}

func availableDetailForTesting(
	ctx context.Context,
	lookup func(string) (string, error),
	version func(context.Context, string) (string, error),
) (ok bool, detail, reason string) {
	path, err := lookup("zstd")
	if err != nil {
		log().Debug("zstd not on $PATH")
		return false, "zstd not on $PATH", "zstd not on $PATH"
	}
	banner, err := version(ctx, path)
	if err != nil {
		r := fmt.Sprintf("zstd --version failed: %v", err)
		return false, r, r
	}
	major, minor, matched, err := parseZstdVersion(banner)
	if err != nil {
		r := fmt.Sprintf("zstd --version parse failed: %v", err)
		return false, r, r
	}
	if major < 1 || (major == 1 && minor < 5) {
		log().Debug("zstd version too old", "version", matched)
		r := fmt.Sprintf("zstd %s too old; need ≥1.5", matched)
		return false, r, r
	}
	log().Debug("zstd available", "path", path, "version", matched)
	return true, fmt.Sprintf("%s via %s", matched, path), ""
}

func runZstdVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
			return "", fmt.Errorf("zstd --version timed out: %w", ctxErr)
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
