package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatalf("expected workdir to be removed, stat err = %v", err)
	}
}
