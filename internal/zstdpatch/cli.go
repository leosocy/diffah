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
//
// Deprecated: use EncodeStream. Retained for the importer hot path until
// the importer streaming spec migrates it. See docs/superpowers/specs/
// 2026-05-02-export-streaming-io-design.md §5.1.
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
	if _, err := EncodeStream(ctx, refPath, targetPath, outPath, opts); err != nil {
		return nil, err
	}
	return os.ReadFile(outPath)
}

// Decode reads a zstd frame produced by Encode and returns the original
// target bytes.
//
// Deprecated: use DecodeStream. Retained for the importer hot path until
// the importer streaming spec migrates it.
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
	if _, err := DecodeStream(ctx, refPath, patchPath, outPath); err != nil {
		return nil, err
	}
	return os.ReadFile(outPath)
}
