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

func TestDiffCommand_WithFixtures(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(bin,
		"diff",
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		out,
	)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", stderr.String())

	require.Contains(t, string(output), "wrote "+out)
	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestDiffCommand_DryRun(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	out := filepath.Join(t.TempDir(), "delta.tar")

	cmd := exec.Command(bin,
		"diff",
		"--dry-run",
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		out,
	)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	_, err = os.Stat(out)
	require.True(t, os.IsNotExist(err), "dry-run should not write the archive")
	require.True(t, strings.Contains(string(output), "would ship"),
		"stdout: %s", string(output))
}
