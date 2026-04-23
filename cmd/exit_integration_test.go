//go:build integration

package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func buildDiffah(t *testing.T) string {
	t.Helper()
	root := findRepoRoot(t)
	bin := filepath.Join(t.TempDir(), "diffah")
	cmd := exec.Command("go", "build",
		"-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper",
		"-o", bin, ".")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))
	return bin
}

func runDiffahBin(t *testing.T, bin string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("run diffah: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func TestExit_UserError_MissingRequiredFlag(t *testing.T) {
	bin := buildDiffah(t)
	_, stderr, exit := runDiffahBin(t, bin, "export")
	require.Equal(t, 2, exit, "expected exit 2 (user) for missing required flag; stderr=%q", stderr)
	require.True(t, strings.Contains(stderr, "user:") || strings.Contains(stderr, "required"),
		"expected user-category or 'required' in stderr; got %q", stderr)
}

func TestExit_UserError_UnknownSubcommand(t *testing.T) {
	bin := buildDiffah(t)
	_, stderr, exit := runDiffahBin(t, bin, "no-such-subcommand")
	require.Equal(t, 2, exit, "expected exit 2 (user) for unknown subcommand; stderr=%q", stderr)
	require.Contains(t, stderr, "diffah:")
}

func TestExit_EnvError_MissingFile(t *testing.T) {
	bin := buildDiffah(t)
	_, _, exit := runDiffahBin(t, bin, "inspect", filepath.Join(os.TempDir(), "nonexistent_diffah_test.tar"))
	require.Equal(t, 3, exit, "expected exit 3 (environment) for missing file")
}
