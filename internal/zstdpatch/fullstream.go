package zstdpatch

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// EncodeFullStream wraps klauspost/compress/zstd.Encoder and writes the
// compressed bytes of targetPath to w. No subprocess. No temp files. The
// streaming pipeline uses this with a counting writer to compute the
// "full-zstd ceiling" size without materializing the encoded bytes — that
// ceiling is purely a comparator for the patch decision and is never the
// surviving payload (see spec §5.4).
//
// Note on encoder API choice: this uses the streaming Write+Close path,
// whereas the deprecated EncodeFull uses one-shot EncodeAll. Both produce
// valid zstd frames that decode to the same target bytes, but the encoded
// byte sequences (and sizes) MAY differ for identical input + options.
// For this reason EncodeFull is NOT a wrapper around EncodeFullStream —
// see the deprecation comment on EncodeFull for the rationale.
func EncodeFullStream(ctx context.Context, targetPath string, w io.Writer, opts EncodeOpts) (int64, error) {
	f, err := os.Open(targetPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: open target: %w", err)
	}
	defer f.Close()

	level := opts.Level
	if level == 0 {
		level = 3
	}
	windowLog := opts.WindowLog
	if windowLog == 0 {
		windowLog = 27
	}

	// Counter sits between encoder output and w so cw.n accumulates the
	// COMPRESSED byte count (not the uncompressed input). This is the
	// load-bearing detail — getting it backwards makes the size returned
	// to PlanShippedTopK meaningless.
	cw := &writeCounter{w: w}
	enc, err := zstd.NewWriter(cw,
		zstd.WithEncoderLevel(zstdLevelToKlauspost(level)),
		zstd.WithWindowSize(1<<windowLog),
	)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: new encoder: %w", err)
	}

	if _, err := io.Copy(enc, contextReader{ctx: ctx, r: f}); err != nil {
		_ = enc.Close()
		return 0, fmt.Errorf("zstdpatch: copy target: %w", err)
	}
	if err := enc.Close(); err != nil {
		return 0, fmt.Errorf("zstdpatch: close encoder: %w", err)
	}
	return cw.n, nil
}

type writeCounter struct {
	w io.Writer
	n int64
}

func (c *writeCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
