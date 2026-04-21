//go:build integration

package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// findRepoRoot climbs up from the current working dir until it finds a
// go.mod. cmd-level integration tests need to run `go run .` from the
// repo root so that the binary includes all packages.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	t.Fatal("could not locate repo root")
	return ""
}

func TestExportCommand_WithFixtures(t *testing.T) {
	root := findRepoRoot(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(
		"go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"export",
		"--target", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"--output", out,
	)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "go run output: %s", string(output))

	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestExportCommand_DryRun(t *testing.T) {
	root := findRepoRoot(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(
		"go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"export",
		"--target", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"--output", out,
		"--dry-run",
	)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "go run output: %s", string(output))

	// Output file must NOT exist.
	_, err = os.Stat(out)
	require.True(t, os.IsNotExist(err))
	require.Contains(t, string(output), "delta would ship")
}
