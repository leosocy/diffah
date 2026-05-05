package exporter

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/leosocy/diffah/internal/workdir"
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
// See spec §4.2. Delegates to internal/workdir.Resolve which is shared
// with the importer side.
func resolveWorkdir(flag, outputPath string) (string, error) {
	return workdir.Resolve(flag, outputPath), nil
}

// ensureWorkdir creates the workdir and its three subdirs (baselines, targets,
// blobs). Returns the resolved workdir path and a cleanup callback that
// best-effort-removes everything under it. Cleanup is idempotent and safe to
// invoke from a defer. On any failure after the workdir root is created, the
// partial directory is torn down before the error returns so the caller never
// owes the user an orphan path.
//
// Root creation + cleanup are delegated to internal/workdir.Ensure; the three
// exporter-specific subdirs are mkdir'd here so the importer (which only
// needs a flat baselines/ subdir of its own choosing) is not forced to
// pay for them.
func ensureWorkdir(flag, outputPath string) (string, func(), error) {
	wd, baseCleanup, err := workdir.Ensure(flag, outputPath)
	if err != nil {
		return "", func() {}, err
	}
	// Wrap baseCleanup so RemoveAll failures surface as a warn log —
	// exporter callers historically depend on visibility of cleanup
	// failures (the shared internal helper silently ignores them).
	cleanup := func() {
		if rmErr := os.RemoveAll(wd); rmErr != nil && !os.IsNotExist(rmErr) {
			log().Warn("workdir cleanup failed", "path", wd, "err", rmErr)
		}
		baseCleanup()
	}
	for _, sub := range []string{"baselines", "targets", "blobs"} {
		if err := os.MkdirAll(filepath.Join(wd, sub), 0o700); err != nil {
			cleanup() // tear down the partial workdir before returning
			return "", func() {}, fmt.Errorf("mkdir %s/%s: %w", wd, sub, err)
		}
	}
	return wd, cleanup, nil
}
