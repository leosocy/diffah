package exporter

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// resolveWorkdir picks the spool root with precedence:
//  1. flag (--workdir DIR)
//  2. DIFFAH_WORKDIR env
//  3. <dir(outputPath)>/.diffah-tmp/<random16hex>
//
// When outputPath is empty (e.g. DryRun API callers without an output
// file), the default base is os.TempDir() — filepath.Dir("") would
// yield "." and place spool dirs under CWD, which fails in read-only
// working directories.
//
// See spec §4.2.
func resolveWorkdir(flag, outputPath string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv("DIFFAH_WORKDIR"); env != "" {
		return env, nil
	}
	suffix, err := randomSuffix()
	if err != nil {
		return "", fmt.Errorf("generate workdir suffix: %w", err)
	}
	base := filepath.Dir(outputPath)
	if outputPath == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, ".diffah-tmp", suffix), nil
}

// ensureWorkdir creates the workdir and its three subdirs (baselines, targets,
// blobs). Returns the resolved workdir path and a cleanup callback that
// best-effort-removes everything under it. Cleanup is idempotent and safe to
// invoke from a defer. On any failure after the workdir root is created, the
// partial directory is torn down before the error returns so the caller never
// owes the user an orphan path.
func ensureWorkdir(flag, outputPath string) (string, func(), error) {
	wd, err := resolveWorkdir(flag, outputPath)
	if err != nil {
		return "", func() {}, err
	}
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("mkdir workdir %s: %w", wd, err)
	}
	cleanup := func() {
		if err := os.RemoveAll(wd); err != nil {
			log().Warn("workdir cleanup failed", "path", wd, "err", err)
		}
	}
	for _, sub := range []string{"baselines", "targets", "blobs"} {
		if err := os.MkdirAll(filepath.Join(wd, sub), 0o700); err != nil {
			cleanup() // tear down the partial workdir before returning
			return "", func() {}, fmt.Errorf("mkdir %s/%s: %w", wd, sub, err)
		}
	}
	return wd, cleanup, nil
}

func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
