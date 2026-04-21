// Package zstdpatch implements zstd --patch-from style byte-level deltas
// by shelling out to the zstd CLI (>= 1.5).
//
// Encode(ref, target) produces a zstd frame that decodes to target when
// seeded with ref via --patch-from. The output is a standard zstd frame.
//
// The package keeps its surface tiny: the exporter and importer never need
// to know about window sizes, compression levels, or encoder state.
//
// Runtime dependency: zstd >= 1.5 must be on $PATH.
package zstdpatch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// emptyZstdFrame returns a canonical zstd frame that decodes to zero bytes.
// Generated once via klauspost/compress so it is guaranteed standards-compliant.
//
// Short-circuiting empty payloads avoids a known assertion failure
// (FIO_highbit64: v != 0) in the zstd CLI < 1.5.x when asked to encode
// an empty file.
var emptyZstdFrame = sync.OnceValue(func() []byte {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		// zstd.NewWriter(nil) has no failure path in current klauspost;
		// panic here makes the invariant explicit if that ever changes.
		panic(fmt.Sprintf("zstdpatch: klauspost NewWriter: %v", err))
	}
	out := enc.EncodeAll(nil, nil)
	_ = enc.Close()
	return out
})

// Encode produces a zstd frame using --patch-from=ref that decodes to target.
// An empty target returns a precomputed empty frame to avoid invoking the CLI
// on a degenerate case that crashes older zstd builds.
func Encode(ref, target []byte) ([]byte, error) {
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
	cmd := exec.Command("zstd",
		"-3", "--long=27",
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

// EncodeFull compresses target as a standalone zstd frame (no reference).
func EncodeFull(target []byte) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	targetPath := filepath.Join(dir, "target")
	outPath := filepath.Join(dir, "target.zst")

	if err := os.WriteFile(targetPath, target, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write target: %w", err)
	}

	cmd := exec.Command("zstd",
		"-3", "--long=27",
		targetPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: encode full: %w\n%s", err, out)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read output: %w", err)
	}
	return data, nil
}

// Decode reads a zstd frame produced by Encode and returns the original
// target bytes. ref must be byte-identical to the ref used at encode time;
// otherwise the decoder may return an error or silently-different bytes.
// Callers are expected to verify the decoded bytes against the content
// digest recorded in the sidecar.
func Decode(ref, patch []byte) ([]byte, error) {
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

	//nolint:gosec // G204: every argv path is mktempd above; no user input.
	cmd := exec.Command("zstd",
		"-d", "--long=27",
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

// DecodeFull reads a standalone zstd frame (no reference).
func DecodeFull(data []byte) ([]byte, error) {
	if bytes.Equal(data, emptyZstdFrame()) {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	inPath := filepath.Join(dir, "input.zst")
	outPath := filepath.Join(dir, "output")

	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write input: %w", err)
	}

	cmd := exec.Command("zstd",
		"-d", "--long=27",
		inPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: decode full: %w\n%s", err, out)
	}

	result, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read output: %w", err)
	}
	return result, nil
}
