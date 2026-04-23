//go:build integration

package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyCommand_RoundTrip(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")
	restored := filepath.Join(tmp, "restored.tar")

	{
		cmd := exec.Command(bin,
			"diff",
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
			delta,
		)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	{
		cmd := exec.Command(bin,
			"apply",
			delta,
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			restored,
		)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, "stderr: %s", stderr.String())
		require.Contains(t, string(out), "wrote "+restored)
	}

	info, err := os.Stat(restored)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}
