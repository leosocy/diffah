//go:build integration

package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// findRepoRoot already exists in cmd/export_integration_test.go.

func TestImportCommand_RoundTrip(t *testing.T) {
	root := findRepoRoot(t)

	// Produce a delta via the export command.
	delta := filepath.Join(t.TempDir(), "delta.tar")
	exp := exec.Command("go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"export",
		"--target", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"--output", delta,
	)
	exp.Dir = root
	out, err := exp.CombinedOutput()
	require.NoError(t, err, "export output: %s", out)

	// Reconstruct via the import command.
	restored := filepath.Join(t.TempDir(), "v2_restored.tar")
	imp := exec.Command("go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"import",
		"--delta", delta,
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"--output", restored,
	)
	imp.Dir = root
	out, err = imp.CombinedOutput()
	require.NoError(t, err, "import output: %s", out)

	fi, err := os.Stat(restored)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))
}

func TestImportCommand_DryRun_Reachable(t *testing.T) {
	root := findRepoRoot(t)

	delta := filepath.Join(t.TempDir(), "delta.tar")
	exp := exec.Command("go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"export",
		"--target", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"--output", delta,
	)
	exp.Dir = root
	out, err := exp.CombinedOutput()
	require.NoError(t, err, "export output: %s", out)

	restored := filepath.Join(t.TempDir(), "v2_restored.tar")
	imp := exec.Command("go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"import",
		"--delta", delta,
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"--output", restored,
		"--dry-run",
	)
	imp.Dir = root
	out, err = imp.CombinedOutput()
	require.NoError(t, err, "import output: %s", out)
	require.Contains(t, string(out), "all reachable: true")

	_, err = os.Stat(restored)
	require.True(t, os.IsNotExist(err))
}

func TestImportCommand_DryRun_Missing(t *testing.T) {
	root := findRepoRoot(t)

	delta := filepath.Join(t.TempDir(), "delta.tar")
	exp := exec.Command("go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"export",
		"--target", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"--output", delta,
	)
	exp.Dir = root
	out, err := exp.CombinedOutput()
	require.NoError(t, err, "export output: %s", out)

	imp := exec.Command("go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"import",
		"--delta", delta,
		"--baseline", "oci-archive:"+filepath.Join(root, "testdata/fixtures/unrelated_oci.tar"),
		"--output", filepath.Join(t.TempDir(), "x.tar"),
		"--dry-run",
	)
	imp.Dir = root
	out, _ = imp.CombinedOutput()
	require.True(t,
		strings.Contains(string(out), "missing in baseline") ||
			strings.Contains(string(out), "baseline missing required blobs"),
		"expected missing-blob diagnostic in output: %s", out)
}
