// Package zstdpatch — CLI-backed patch-from encode/decode.
//
// These functions shell out to `zstd ≥ 1.5`. EncodeFull / DecodeFull live
// in fullgo.go and do NOT require the CLI.
package zstdpatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// EncodeOpts tunes the producer-side zstd parameters. A zero value
// requests the historical Phase-3 defaults (level 3, --long=27) so
// existing callers and existing fixtures keep their byte-for-byte
// outputs.
type EncodeOpts struct {
	// Level is the zstd compression level (1..22). Zero means level 3.
	Level int
	// WindowLog is log2 of the long-mode window in bytes (10..31).
	// Zero means 27 (128 MiB), the historical Phase-3 cap.
	WindowLog int
}

func (o EncodeOpts) levelArg() string {
	l := o.Level
	if l == 0 {
		l = 3
	}
	return fmt.Sprintf("-%d", l)
}

func (o EncodeOpts) windowArg() string {
	w := o.WindowLog
	if w == 0 {
		w = 27
	}
	return fmt.Sprintf("--long=%d", w)
}

// Encode produces a zstd frame using --patch-from=ref that decodes to target.
// An empty target returns a precomputed empty frame to avoid invoking the CLI
// on a degenerate case that crashes older zstd builds. ctx cancellation kills
// the zstd subprocess. EncodeOpts tunes level and window; zero-valued opts
// reproduce Phase-3 byte-identical output.
func Encode(ctx context.Context, ref, target []byte, opts EncodeOpts) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	outPath := filepath.Join(dir, "patch.zst")

	if err := os.WriteFile(refPath, ref, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write ref: %w", err)
	}
	if err := os.WriteFile(targetPath, target, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write target: %w", err)
	}

	//nolint:gosec // G204: every argv path is created by this function via MkdirTemp; no user input reaches exec.Command.
	cmd := exec.CommandContext(ctx, "zstd",
		opts.levelArg(), opts.windowArg(),
		"--patch-from="+refPath,
		targetPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: encode: %w\n%s", err, out)
	}

	patch, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read patch: %w", err)
	}
	return patch, nil
}

// Decode reads a zstd frame produced by Encode and returns the original
// target bytes. ref must be byte-identical to the ref used at encode time.
// Callers are expected to verify the decoded bytes against the content
// digest recorded in the sidecar. ctx cancellation kills the zstd subprocess.
func Decode(ctx context.Context, ref, patch []byte) ([]byte, error) {
	if bytes.Equal(patch, emptyZstdFrame()) {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	refPath := filepath.Join(dir, "ref")
	patchPath := filepath.Join(dir, "patch.zst")
	outPath := filepath.Join(dir, "target")

	if err := os.WriteFile(refPath, ref, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write ref: %w", err)
	}
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write patch: %w", err)
	}

	// --long=31 sets the maximum admissible window size (2 GiB). Frames
	// declaring smaller windows allocate only what they need; this cap
	// only governs the upper bound on decoder memory.
	//nolint:gosec // G204: every argv path is mktempd above; no user input.
	cmd := exec.CommandContext(ctx, "zstd",
		"-d", "--long=31",
		"--patch-from="+refPath,
		patchPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: decode: %w\n%s", err, out)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read target: %w", err)
	}
	return data, nil
}
