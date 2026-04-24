//go:build integration

package cmd_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInspectCommand_WithFixtures builds a real delta via `diffah diff`
// and runs `diffah inspect` against it, asserting the new surfaces:
// intra-layer patches required, zstd available, and the per-image section.
func TestInspectCommand_WithFixtures(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	bundlePath := filepath.Join(t.TempDir(), "bundle.tar")

	diffCmd := exec.Command(bin,
		"diff",
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		bundlePath,
	)
	diffCmd.Dir = root
	diffOut, err := diffCmd.CombinedOutput()
	require.NoError(t, err, "diff output: %s", diffOut)

	inspectCmd := exec.Command(bin, "inspect", bundlePath)
	inspectCmd.Dir = root
	out, err := inspectCmd.CombinedOutput()
	require.NoError(t, err, "inspect output: %s", out)

	s := string(out)
	require.Contains(t, s, "archive: ")
	require.Contains(t, s, "images: 1")
	require.Contains(t, s, "intra-layer patches required:")
	require.Contains(t, s, "zstd available:")
	require.Contains(t, s, "--- image: default ---")
}
