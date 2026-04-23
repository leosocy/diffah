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
			"baseline": filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"target":   filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
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
			"app": filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		},
	}
	baselinePath := filepath.Join(tmp, "baselines.json")
	raw, err = json.Marshal(baselineSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(baselinePath, raw, 0o600))

	restored := filepath.Join(tmp, "restored")
	require.NoError(t, os.MkdirAll(restored, 0o755))
	cmd = exec.Command(bin, "unbundle", bundleOut, baselinePath, restored)
	cmd.Dir = root
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	entries, err := os.ReadDir(restored)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "expected at least one reconstructed image")
}
