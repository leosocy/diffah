//go:build integration

package cmd_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInspectCommand_WithFixtures builds a real bundle via `diffah export`
// and runs `diffah inspect` against it, asserting the new surfaces:
// intra-layer patches required, zstd available, and the per-image section.
func TestInspectCommand_WithFixtures(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	bundlePath := filepath.Join(t.TempDir(), "bundle.tar")

	exportCmd := exec.Command(bin,
		"export",
		"--pair", "app="+filepath.Join(root, "testdata/fixtures/v1_oci.tar")+","+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		bundlePath,
	)
	exportCmd.Dir = root
	exportOut, err := exportCmd.CombinedOutput()
	require.NoError(t, err, "export output: %s", exportOut)

	inspectCmd := exec.Command(bin, "inspect", bundlePath)
	inspectCmd.Dir = root
	out, err := inspectCmd.CombinedOutput()
	require.NoError(t, err, "inspect output: %s", out)

	s := string(out)
	require.Contains(t, s, "archive: ")
	require.Contains(t, s, "images: 1")
	require.Contains(t, s, "intra-layer patches required:")
	require.Contains(t, s, "zstd available:")
	require.Contains(t, s, "--- image: app ---")
}
