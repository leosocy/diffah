//go:build integration

package cmd_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// noCfgDir holds the temp dir created to hold a non-existent config file,
// cleaned up after m.Run() completes.
var noCfgDir string

var (
	binaryPath string
	binaryDir  string
	buildOnce  sync.Once
	buildErr   error
	buildTags  = "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper"
)

func TestMain(m *testing.M) {
	// Phase 5.2: ensure no real ~/.diffah/config.yaml leaks into the
	// integration suite. The binary reads $DIFFAH_CONFIG at startup;
	// pointing it at an absent file makes every invocation use defaults.
	if os.Getenv("DIFFAH_CONFIG") == "" {
		dir, _ := os.MkdirTemp("", "diffah-integration-no-config-")
		os.Setenv("DIFFAH_CONFIG", filepath.Join(dir, "absent.yaml"))
		noCfgDir = dir
	}
	code := m.Run()
	if binaryDir != "" {
		_ = os.RemoveAll(binaryDir)
	}
	if noCfgDir != "" {
		_ = os.RemoveAll(noCfgDir)
	}
	os.Exit(code)
}

// integrationBinary returns the path to a diffah binary compiled once per
// `go test` invocation. The build happens lazily on first call.
func integrationBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(buildIntegrationBinary)
	if buildErr != nil {
		t.Fatalf("build diffah: %v", buildErr)
	}
	return binaryPath
}

func buildIntegrationBinary() {
	root, err := discoverRepoRoot()
	if err != nil {
		buildErr = err
		return
	}
	dir, err := os.MkdirTemp("", "diffah-integration-")
	if err != nil {
		buildErr = err
		return
	}
	binaryDir = dir
	binaryPath = filepath.Join(dir, "diffah")
	cmd := exec.Command("go", "build", "-tags", buildTags, "-o", binaryPath, ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("build failed: %v: %s", err, string(out))
	}
}

func discoverRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("go.mod not found above %s", cwd)
}

// findRepoRoot mirrors discoverRepoRoot but with testing.T fatality.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := discoverRepoRoot()
	require.NoError(t, err)
	return root
}

func runDiffahBin(t *testing.T, bin string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return outBuf.String(), errBuf.String(), ee.ExitCode()
		}
		t.Fatalf("run diffah: %v", err)
	}
	return outBuf.String(), errBuf.String(), 0
}
