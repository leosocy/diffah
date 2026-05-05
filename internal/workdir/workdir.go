// Package workdir provides shared per-Export/per-Import disk spool
// lifecycle utilities. Resolution precedence (high → low):
//
//  1. explicit workdir string from caller
//  2. DIFFAH_WORKDIR environment variable
//  3. <dir(hint)>/.diffah-tmp/<random> when hint is non-empty
//  4. os.TempDir()/diffah-tmp-<random>
package workdir

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const envVar = "DIFFAH_WORKDIR"

// Resolve returns the workdir path that Ensure would create, without
// creating it. Public for callers that need to communicate the path
// before any I/O happens (e.g., diagnostics, --dry-run).
func Resolve(workdir, hint string) string {
	if workdir != "" {
		return workdir
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	if hint != "" {
		return filepath.Join(filepath.Dir(hint), ".diffah-tmp", randSuffix())
	}
	return filepath.Join(os.TempDir(), "diffah-tmp-"+randSuffix())
}

// Ensure resolves the workdir and creates it. The returned cleanup
// closure removes the workdir tree and is safe to call multiple times
// (idempotent).
func Ensure(workdir, hint string) (string, func(), error) {
	path := Resolve(workdir, hint)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("create workdir %s: %w", path, err)
	}
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		_ = os.RemoveAll(path)
	}
	return path, cleanup, nil
}

func randSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
