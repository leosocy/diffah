// Package zstdpatch — CLI-backed patch-from encode/decode.
//
// These functions shell out to `zstd ≥ 1.5`. EncodeFull / DecodeFull live
// in fullgo.go and do NOT require the CLI.
package zstdpatch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

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

// Decode reads a zstd frame produced by Encode and returns the original
// target bytes. ref must be byte-identical to the ref used at encode time.
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
