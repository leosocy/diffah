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
	raw, err := json.MarshalIndent(spec, "", "  ")
	require.NoError(t, err)
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

// TestBundleCommand_MultiPair exercises the feature's differentiator over
// 'diff': combining multiple images into one bundle archive. The same fixture
// pair is reused twice under distinct names to avoid needing additional
// testdata.
func TestBundleCommand_MultiPair(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	v1 := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	v2 := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	spec := map[string]any{
		"pairs": []map[string]string{
			{"name": "svc-a", "baseline": v1, "target": v2},
			{"name": "svc-b", "baseline": v1, "target": v2},
		},
	}
	raw, err := json.MarshalIndent(spec, "", "  ")
	require.NoError(t, err)
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
