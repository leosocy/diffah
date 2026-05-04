package workdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve_ExplicitWins(t *testing.T) {
	t.Setenv(envVar, "/env/path")
	got := Resolve("/explicit", "/some/hint")
	if got != "/explicit" {
		t.Fatalf("got %q want /explicit", got)
	}
}

func TestResolve_EnvBeatsHint(t *testing.T) {
	t.Setenv(envVar, "/env/path")
	got := Resolve("", "/some/hint/file")
	if got != "/env/path" {
		t.Fatalf("got %q want /env/path", got)
	}
}

func TestResolve_HintBeatsTemp(t *testing.T) {
	t.Setenv(envVar, "")
	got := Resolve("", "/parent/file.tar")
	if !strings.HasPrefix(got, "/parent/.diffah-tmp/") {
		t.Fatalf("got %q want under /parent/.diffah-tmp/", got)
	}
}

func TestResolve_TempFallback(t *testing.T) {
	t.Setenv(envVar, "")
	got := Resolve("", "")
	if !strings.HasPrefix(got, filepath.Join(os.TempDir(), "diffah-tmp-")) {
		t.Fatalf("got %q want under tempdir", got)
	}
}

func TestEnsure_CreatesAndCleansUp(t *testing.T) {
	t.Setenv(envVar, "")
	dir := t.TempDir()
	path, cleanup, err := Ensure("", filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("created path missing: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should have removed %s, stat err=%v", path, err)
	}
	cleanup() // idempotent
}
