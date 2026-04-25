//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

func TestUnbundleCLI_MixedDestinations(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)

	v1Path := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	seedOCIIntoRegistry(t, srv, "svc-a/v1", v1Path, nil)
	seedOCIIntoRegistry(t, srv, "svc-b/v1", v1Path, nil)

	tmp := t.TempDir()

	// Build a bundle from the local v1/v2 fixture pair for both services.
	v1Ref := "oci-archive:" + v1Path
	v2Ref := "oci-archive:" + filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	bundleSpec := map[string]any{
		"pairs": []map[string]string{
			{
				"name":     "svc-a",
				"baseline": v1Ref,
				"target":   v2Ref,
			},
			{
				"name":     "svc-b",
				"baseline": v1Ref,
				"target":   v2Ref,
			},
		},
	}
	specPath := filepath.Join(tmp, "bundle.json")
	raw, err := json.MarshalIndent(bundleSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	bundlePath := filepath.Join(tmp, "bundle.tar")
	_, stderr, exit := runDiffahBin(t, bin, "bundle", specPath, bundlePath)
	require.Equal(t, 0, exit, "bundle failed: %s", stderr)

	// Baselines spec: pull both from the in-process registry.
	baselinesSpec := map[string]any{
		"baselines": map[string]string{
			"svc-a": registryDockerURL(t, srv, "svc-a/v1"),
			"svc-b": registryDockerURL(t, srv, "svc-b/v1"),
		},
	}
	baselinesPath := filepath.Join(tmp, "baselines.json")
	raw, err = json.MarshalIndent(baselinesSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(baselinesPath, raw, 0o600))

	// Outputs spec: svc-a goes to the registry, svc-b goes to a local archive.
	svcBArchive := filepath.Join(tmp, "svc-b.tar")
	outputsSpec := map[string]any{
		"outputs": map[string]string{
			"svc-a": registryDockerURL(t, srv, "svc-a/v2"),
			"svc-b": "oci-archive:" + svcBArchive,
		},
	}
	outputsPath := filepath.Join(tmp, "outputs.json")
	raw, err = json.MarshalIndent(outputsSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outputsPath, raw, 0o600))

	_, stderr, exit = runDiffahBin(t, bin,
		"unbundle", bundlePath, baselinesPath, outputsPath,
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "unbundle failed: %s", stderr)

	// Verify registry target: svc-a/v2 must be present and pullable.
	img, err := crane.Pull(registryHost(t, srv)+"/svc-a/v2:latest", crane.Insecure)
	require.NoError(t, err)
	d, err := img.Digest()
	require.NoError(t, err)
	require.NotEmpty(t, d.String())

	// Verify filesystem target: svc-b.tar must exist and be non-empty.
	info, err := os.Stat(svcBArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}
