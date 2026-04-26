//go:build integration

package cmd_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// TestApplyCLI_PreflightManifestFetchBounded asserts that pre-flight does
// not regress the baseline manifest GET count: the entire apply pipeline
// (pre-flight + composeImage + invariant verify) reads each baseline
// manifest at most twice. Pre-flight reads it once for layer-set
// classification; copy.Image's own manifest-resolution path may issue
// additional reads (auth probe, content-type negotiation), so the
// budget is 2 — anything higher means pre-flight is multi-fetching.
//
// The test is single-image so the budget applies directly to one repo.
func TestApplyCLI_PreflightManifestFetchBounded(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)

	// Seed v1 as the baseline.
	v1Path := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	seedOCIIntoRegistry(t, srv, "service-x/v1", v1Path, nil)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	// Apply with the registry-backed baseline, output to local oci-archive.
	outputPath := filepath.Join(tmp, "out.tar")
	_, stderr, exit := runDiffahBin(t, bin, "apply",
		"--tls-verify=false",
		deltaPath,
		registryDockerURL(t, srv, "service-x/v1"),
		"oci-archive:"+outputPath,
	)
	require.Equalf(t, 0, exit, "apply failed: %s", stderr)

	// Count manifest GETs against the seeded baseline repo. Both tag
	// resolution and digest pulls match the manifest path regex; we
	// budget for ≤ 2 across pre-flight + apply.
	hits := srv.ManifestHits()
	baselineGETs := 0
	for _, h := range hits {
		if h.Repo == "service-x/v1" && h.Method == "GET" {
			baselineGETs++
		}
	}
	require.LessOrEqualf(t, baselineGETs, 2,
		"baseline manifest GETs = %d, want <= 2 (pre-flight + apply may share); hits=%v",
		baselineGETs, hits)
}
