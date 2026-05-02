// Package zstdpatch — streaming (path-based) variants.
//
// These functions take filesystem paths instead of []byte. They exist
// because callers in the exporter streaming pipeline already have their
// data on disk (in the per-Export workdir spool); the legacy []byte API
// would force them to re-read into memory, defeating the bounded-RAM
// guarantee documented in
// docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §5.1.
package zstdpatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// EncodeStream produces a zstd patch from refPath against targetPath, written
// to outPath. Returns the encoded byte count of outPath. ctx cancellation
// kills the zstd subprocess. Bit-equivalent to Encode(refBytes, targetBytes)
// when the file contents are byte-identical.
//
// Empty target files produce the precomputed empty zstd frame at outPath.
func EncodeStream(ctx context.Context, refPath, targetPath, outPath string, opts EncodeOpts) (int64, error) {
	tInfo, err := os.Stat(targetPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat target: %w", err)
	}
	if tInfo.Size() == 0 {
		empty := emptyZstdFrame()
		if err := os.WriteFile(outPath, empty, 0o600); err != nil {
			return 0, fmt.Errorf("zstdpatch: write empty frame: %w", err)
		}
		return int64(len(empty)), nil
	}
	//nolint:gosec // G204: refPath/targetPath/outPath come from caller-controlled spool dirs, not user input.
	cmd := exec.CommandContext(ctx, "zstd",
		opts.levelArg(), opts.windowArg(),
		"--patch-from="+refPath,
		targetPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(outPath) // best effort; partial output cannot be trusted
		return 0, fmt.Errorf("zstdpatch: encode: %w\n%s", err, out)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat patch: %w", err)
	}
	return info.Size(), nil
}

// DecodeStream reverses EncodeStream. Added now (even though importer
// streaming is out of scope of this PR series) to avoid a second
// package-surface churn when the importer migration spec lands.
func DecodeStream(ctx context.Context, refPath, patchPath, outPath string) (int64, error) {
	pInfo, err := os.Stat(patchPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat patch: %w", err)
	}
	patchBytes, err := os.ReadFile(patchPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: read patch: %w", err)
	}
	if pInfo.Size() == int64(len(emptyZstdFrame())) && bytesEqual(patchBytes, emptyZstdFrame()) {
		// Empty-frame contract: produce a zero-byte target file.
		if err := os.WriteFile(outPath, nil, 0o600); err != nil {
			return 0, fmt.Errorf("zstdpatch: write empty target: %w", err)
		}
		return 0, nil
	}
	//nolint:gosec // G204: paths are caller-controlled spool dirs.
	cmd := exec.CommandContext(ctx, "zstd",
		"-d", "--long=31",
		"--patch-from="+refPath,
		patchPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(outPath)
		return 0, fmt.Errorf("zstdpatch: decode: %w\n%s", err, out)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat decoded: %w", err)
	}
	return info.Size(), nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
