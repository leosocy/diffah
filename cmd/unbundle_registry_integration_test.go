//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/opencontainers/go-digest"
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

// TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage builds
// a two-pair bundle whose pairs share identical baseline content
// (same fixture pushed to two distinct registry repos, app-a/v1 and
// app-b/v1). After diffah unbundle, every distinct baseline blob
// digest must appear in BlobHits exactly once total across both
// repos — proving the per-Import baselineBlobCache deduplicates
// fetches across images. Without the cache this assertion fails
// because each bundleImageSource opens an independent ImageSource
// and re-fetches every required blob.
func TestUnbundleCLI_BaselineBlobsFetchedExactlyOnce_MultiImage(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)

	v1 := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	v2 := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	seedOCIIntoRegistry(t, srv, "app-a/v1", v1, nil)
	seedOCIIntoRegistry(t, srv, "app-b/v1", v1, nil)

	tmp := t.TempDir()

	bundleSpec := map[string]any{
		"pairs": []map[string]string{
			{"name": "app-a", "baseline": "oci-archive:" + v1, "target": "oci-archive:" + v2},
			{"name": "app-b", "baseline": "oci-archive:" + v1, "target": "oci-archive:" + v2},
		},
	}
	bundleSpecPath := filepath.Join(tmp, "bundle.json")
	raw, err := json.Marshal(bundleSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(bundleSpecPath, raw, 0o600))

	bundleOut := filepath.Join(tmp, "bundle.tar")
	_, stderr, exit := runDiffahBin(t, bin, "bundle", bundleSpecPath, bundleOut)
	require.Equal(t, 0, exit, "bundle failed: %s", stderr)

	baselineSpec := map[string]any{
		"baselines": map[string]string{
			"app-a": registryDockerURL(t, srv, "app-a/v1"),
			"app-b": registryDockerURL(t, srv, "app-b/v1"),
		},
	}
	baselineSpecPath := filepath.Join(tmp, "baselines.json")
	raw, err = json.Marshal(baselineSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(baselineSpecPath, raw, 0o600))

	outputsSpec := map[string]any{
		"outputs": map[string]string{
			"app-a": "oci-archive:" + filepath.Join(tmp, "restored-a.tar"),
			"app-b": "oci-archive:" + filepath.Join(tmp, "restored-b.tar"),
		},
	}
	outputsPath := filepath.Join(tmp, "outputs.json")
	raw, err = json.Marshal(outputsSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outputsPath, raw, 0o600))

	before := len(srv.BlobHits())

	_, stderr, exit = runDiffahBin(t, bin,
		"unbundle", bundleOut, baselineSpecPath, outputsPath,
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "unbundle failed: %s", stderr)

	totalPerDigest := make(map[digest.Digest]int)
	all := srv.BlobHits()
	require.Greater(t, len(all), before, "registry must have observed at least one blob fetch")
	for _, h := range all[before:] {
		switch h.Repo {
		case "app-a/v1", "app-b/v1":
			totalPerDigest[h.Digest]++
		}
	}
	require.NotEmptyf(t, totalPerDigest,
		"expected at least one baseline-blob fetch across app-a/v1 and app-b/v1 — fixture must exercise the baseline-fetch path")
	for d, n := range totalPerDigest {
		t.Logf("baseline blob %s total cross-repo hits=%d", d, n)
		require.Equalf(t, 1, n,
			"baseline blob %s fetched %d times across app-a/v1 + app-b/v1; want exactly 1 — dedup regression", d, n)
	}

	for _, name := range []string{"restored-a.tar", "restored-b.tar"} {
		info, err := os.Stat(filepath.Join(tmp, name))
		require.NoError(t, err, "missing output %s", name)
		require.Greater(t, info.Size(), int64(0))
	}
}
