package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWorkdir_TearsDownOnSubdirFailure(t *testing.T) {
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "bundle.tar")

	// Force the default workdir path so we know where to plant the conflict.
	wantBase := filepath.Join(outputDir, ".diffah-tmp")
	if err := os.MkdirAll(wantBase, 0o700); err != nil {
		t.Fatalf("seed workdir base: %v", err)
	}

	// Pre-create a workdir at a known path and plant a regular file at
	// .../baselines so MkdirAll(.../baselines, 0o700) fails.
	wd := filepath.Join(wantBase, "fixed-suffix")
	if err := os.MkdirAll(wd, 0o700); err != nil {
		t.Fatalf("seed wd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wd, "baselines"), []byte("blocker"), 0o600); err != nil {
		t.Fatalf("plant blocker: %v", err)
	}

	// Pass the planted wd via flag so resolveWorkdir returns it deterministically.
	got, cleanup, err := ensureWorkdir(wd, outputPath)
	if err == nil {
		cleanup()
		t.Fatal("expected ensureWorkdir to fail when subdir mkdir is blocked")
	}
	if got != "" {
		t.Fatalf("expected empty path on error, got %q", got)
	}
	// The blocker file we planted is fine to leave; we only assert that
	// ensureWorkdir does not LEAVE BEHIND any files it created (e.g., the
	// other two subdirs that mkdir'd successfully before the failure).
	for _, sub := range []string{"targets", "blobs"} {
		if _, err := os.Stat(filepath.Join(wd, sub)); !os.IsNotExist(err) {
			t.Fatalf("expected %s subdir cleaned up, stat err = %v", sub, err)
		}
	}
}

func TestResolveWorkdir_FlagBeatsEnvBeatsDefault(t *testing.T) {
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "bundle.tar")

	t.Setenv("DIFFAH_WORKDIR", filepath.Join(outputDir, "from-env"))

	// 1. Flag wins
	got, err := resolveWorkdir("from-flag", outputPath)
	if err != nil {
		t.Fatalf("resolveWorkdir(flag): %v", err)
	}
	if got != "from-flag" {
		t.Fatalf("flag should win: got %q", got)
	}

	// 2. Env wins when flag empty
	got, err = resolveWorkdir("", outputPath)
	if err != nil {
		t.Fatalf("resolveWorkdir(env): %v", err)
	}
	if got != filepath.Join(outputDir, "from-env") {
		t.Fatalf("env should win: got %q", got)
	}

	// 3. Default = <outputDir>/.diffah-tmp/<random>
	t.Setenv("DIFFAH_WORKDIR", "")
	got, err = resolveWorkdir("", outputPath)
	if err != nil {
		t.Fatalf("resolveWorkdir(default): %v", err)
	}
	wantPrefix := filepath.Join(outputDir, ".diffah-tmp") + string(os.PathSeparator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("default should be under %q, got %q", wantPrefix, got)
	}
}

func TestEnsureWorkdir_CreatesAllSubdirsAndCleansUp(t *testing.T) {
	outputDir := t.TempDir()
	wd, cleanup, err := ensureWorkdir("", filepath.Join(outputDir, "bundle.tar"))
	if err != nil {
		t.Fatalf("ensureWorkdir: %v", err)
	}
	for _, sub := range []string{"baselines", "targets", "blobs"} {
		if _, err := os.Stat(filepath.Join(wd, sub)); err != nil {
			t.Fatalf("expected %s subdir: %v", sub, err)
		}
	}
	cleanup()
	cleanup() // idempotent — second call must be safe
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatalf("expected workdir to be removed, stat err = %v", err)
	}
}
