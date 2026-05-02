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
	return filepath.Join(filepath.Dir(outputPath), ".diffah-tmp", suffix), nil
}

// ensureWorkdir creates the workdir and its three subdirs (baselines, targets,
// blobs). Returns the resolved workdir path and a cleanup callback that
// best-effort-removes everything under it. Cleanup is idempotent and safe to
// invoke from a defer.
func ensureWorkdir(flag, outputPath string) (string, func(), error) {
	wd, err := resolveWorkdir(flag, outputPath)
	if err != nil {
		return "", func() {}, err
	}
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("mkdir workdir %s: %w", wd, err)
	}
	for _, sub := range []string{"baselines", "targets", "blobs"} {
		if err := os.MkdirAll(filepath.Join(wd, sub), 0o700); err != nil {
			return "", func() {}, fmt.Errorf("mkdir %s/%s: %w", wd, sub, err)
		}
	}
	cleanup := func() { _ = os.RemoveAll(wd) }
	return wd, cleanup, nil
}

func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
