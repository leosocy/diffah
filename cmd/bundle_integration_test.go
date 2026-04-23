//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundleCommand_WithSpec(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	spec := map[string]any{
		"pairs": []map[string]string{{
			"name":     "app",
			"baseline": filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"target":   filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		}},
	}
	raw, _ := json.MarshalIndent(spec, "", "  ")
	specPath := filepath.Join(tmp, "bundle.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	out := filepath.Join(tmp, "bundle.tar")
	cmd := exec.Command(bin, "bundle", specPath, out)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}
