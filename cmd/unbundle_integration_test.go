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

func TestUnbundleCommand_BundleRoundTrip(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	bundleSpec := map[string]any{
		"pairs": []map[string]string{{
			"name":     "app",
			"baseline": "oci-archive:" + filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"target":   "oci-archive:" + filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		}},
	}
	specPath := filepath.Join(tmp, "bundle.json")
	raw, err := json.Marshal(bundleSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	bundleOut := filepath.Join(tmp, "bundle.tar")
	cmd := exec.Command(bin, "bundle", specPath, bundleOut)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	baselineSpec := map[string]any{
		"baselines": map[string]string{
			"app": "oci-archive:" + filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		},
	}
	baselinePath := filepath.Join(tmp, "baselines.json")
	raw, err = json.Marshal(baselineSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(baselinePath, raw, 0o600))

	// Build outputs.json: map each image to an oci-archive destination.
	restoredArchive := filepath.Join(tmp, "app.tar")
	outputsSpec := map[string]any{
		"outputs": map[string]string{
			"app": "oci-archive:" + restoredArchive,
		},
	}
	outputsPath := filepath.Join(tmp, "outputs.json")
	raw, err = json.Marshal(outputsSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outputsPath, raw, 0o600))

	cmd = exec.Command(bin, "unbundle", bundleOut, baselinePath, outputsPath)
	cmd.Dir = root
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "wrote 1 images per "+outputsPath)

	info, err := os.Stat(restoredArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}
