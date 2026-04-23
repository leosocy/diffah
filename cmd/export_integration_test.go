//go:build integration

package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExportCommand_WithFixtures(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(bin,
		"export",
		"--pair", "app="+filepath.Join(root, "testdata/fixtures/v1_oci.tar")+","+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		out,
	)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "diffah output: %s", string(output))

	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestExportCommand_DryRun(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(bin,
		"export",
		"--pair", "app="+filepath.Join(root, "testdata/fixtures/v1_oci.tar")+","+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		"--dry-run",
		out,
	)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "diffah output: %s", string(output))

	_, err = os.Stat(out)
	require.True(t, os.IsNotExist(err))
	require.Contains(t, string(output), "blobs")
}

func TestExport_RejectsUnknownIntraLayerValue(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)

	cmd := exec.Command(bin,
		"export",
		"--pair", "a="+filepath.Join(root, "testdata/fixtures/v1_oci.tar")+","+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		"--intra-layer", "aggressive",
		filepath.Join(t.TempDir(), "out.tar"),
	)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	t.Logf("output: %s", out)
	require.Error(t, err)
	require.Contains(t, string(out), "aggressive")
	require.Contains(t, string(out), "--intra-layer")
}
