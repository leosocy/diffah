//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

// TestUnbundleCLI_PartialModeSkipsB2 proves that without --strict, an image
// with an incomplete baseline (B2 — missing reuse layer) is skipped at
// preflight and other images still apply. Exit 0; final summary reports
// "applied 1/2 images" and names the skipped svc.
func TestUnbundleCLI_PartialModeSkipsB2(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	bundlePath, deltaSvcAStripBaseline, deltaSvcBStripBaseline :=
		buildTwoImageBundleWithB2(t, bin, root, tmp)

	// svc-b's baseline is the stripped one (missing a reuse layer);
	// svc-a's baseline is the unmodified v1 fixture.
	baselinesPath := filepath.Join(tmp, "baselines.json")
	writeJSONFile(t, baselinesPath, map[string]any{
		"baselines": map[string]string{
			"svc-a": "oci-archive:" + deltaSvcAStripBaseline,
			"svc-b": "oci-archive:" + deltaSvcBStripBaseline,
		},
	})

	svcAOut := filepath.Join(tmp, "svc-a.tar")
	svcBOut := filepath.Join(tmp, "svc-b.tar")
	outputsPath := filepath.Join(tmp, "outputs.json")
	writeJSONFile(t, outputsPath, map[string]any{
		"outputs": map[string]string{
			"svc-a": "oci-archive:" + svcAOut,
			"svc-b": "oci-archive:" + svcBOut,
		},
	})

	_, stderr, exit := runDiffahBin(t, bin, "unbundle", bundlePath, baselinesPath, outputsPath)

	require.Equalf(t, 0, exit, "partial mode: exit = %d, want 0; stderr=%s", exit, stderr)
	require.Containsf(t, stderr, "applied 1/2",
		"stderr should report 'applied 1/2'; got:\n%s", stderr)
	require.Containsf(t, stderr, "svc-b",
		"stderr should mention svc-b skipped; got:\n%s", stderr)

	info, err := os.Stat(svcAOut)
	require.NoError(t, err, "svc-a output should exist")
	require.Greater(t, info.Size(), int64(0), "svc-a output should be non-empty")
	if _, err := os.Stat(svcBOut); err == nil {
		t.Errorf("svc-b output should not exist (was skipped); but stat succeeded")
	}
}

// TestUnbundleCLI_StrictAbortsAfterFullScan proves --strict + a B2 image
// scans every image, then aborts (exit 4). The summary on stderr lists the
// failing image so the operator sees the complete picture rather than
// learning about issues one --strict run at a time.
func TestUnbundleCLI_StrictAbortsAfterFullScan(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	bundlePath, baselineSvcAPath, baselineSvcBStrippedPath :=
		buildTwoImageBundleWithB2(t, bin, root, tmp)

	baselinesPath := filepath.Join(tmp, "baselines.json")
	writeJSONFile(t, baselinesPath, map[string]any{
		"baselines": map[string]string{
			"svc-a": "oci-archive:" + baselineSvcAPath,
			"svc-b": "oci-archive:" + baselineSvcBStrippedPath,
		},
	})

	outputsPath := filepath.Join(tmp, "outputs.json")
	writeJSONFile(t, outputsPath, map[string]any{
		"outputs": map[string]string{
			"svc-a": "oci-archive:" + filepath.Join(tmp, "svc-a.tar"),
			"svc-b": "oci-archive:" + filepath.Join(tmp, "svc-b.tar"),
		},
	})

	_, stderr, exit := runDiffahBin(t, bin, "unbundle", "--strict",
		bundlePath, baselinesPath, outputsPath)

	require.Equalf(t, 4, exit, "strict mode: exit = %d, want 4; stderr=%s", exit, stderr)
	require.Containsf(t, stderr, "svc-b",
		"strict-mode summary should list svc-b's failure; stderr=%s", stderr)
	if _, err := os.Stat(filepath.Join(tmp, "svc-a.tar")); err == nil {
		t.Errorf("svc-a output must not be written in strict mode (full scan, then abort)")
	}
}

// buildTwoImageBundleWithB2 produces a 2-image v1->v2 bundle plus two
// baseline archives, the second of which has had its first reuse layer
// stripped to simulate a B2 condition. Returns (bundlePath, svcABaseline,
// svcBBaselineStripped). All artifacts live under tmp.
//
// The implementation runs the project's own `diffah bundle` against a
// synthesized 2-image bundle spec built on the existing v1/v2 OCI fixtures
// (the same ones used by apply_resilience_integration_test.go). This keeps
// the test resilient to upstream sidecar shape changes — whatever the
// official bundler emits, the test consumes.
func buildTwoImageBundleWithB2(t *testing.T, bin, root, tmp string) (
	bundlePath, svcABaseline, svcBBaselineStripped string,
) {
	t.Helper()
	v1 := "oci-archive:" + filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	v2 := "oci-archive:" + filepath.Join(root, "testdata/fixtures/v2_oci.tar")

	// svc-b is listed first so the importer attempts it before svc-a; this
	// matters because the importer's baselineBlobCache is keyed by digest
	// and shared across images in a single Import call. If svc-a (with a
	// complete baseline) ran first, its successful fetch of the shared
	// reuse-layer digest would populate the cache and silently satisfy
	// svc-b's later fetch from a stripped baseline — masking the very B2
	// condition the test exists to exercise.
	bundleSpec := map[string]any{
		"pairs": []map[string]string{
			{"name": "svc-b", "baseline": v1, "target": v2},
			{"name": "svc-a", "baseline": v1, "target": v2},
		},
	}
	specPath := filepath.Join(tmp, "bundle-spec.json")
	writeJSONFile(t, specPath, bundleSpec)

	bundlePath = filepath.Join(tmp, "bundle.tar")
	cmd := exec.Command(bin, "bundle", specPath, bundlePath)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "bundle build failed: %s", string(out))

	// svc-a uses the stock v1 baseline.
	svcABaseline = filepath.Join(root, "testdata/fixtures/v1_oci.tar")

	// svc-b's baseline has its first baseline-only-reuse layer stripped.
	// "Baseline-only-reuse" means: the layer is in the svc-b target manifest
	// but the diff did not ship it (so apply needs it from baseline) — that
	// is exactly the B2 condition.
	sc := readSidecarFromArchive(t, bundlePath)
	reuseLayer := firstBaselineOnlyReuseLayerForImage(t, root, sc, "svc-b")

	svcBBaselineStripped = filepath.Join(tmp, "v1_baseline_no_reuse_for_svc_b.tar")
	stripLayerFromOCIArchive(t, svcABaseline, svcBBaselineStripped, reuseLayer)
	return
}

// firstBaselineOnlyReuseLayerForImage is the multi-image variant of
// firstBaselineOnlyReuseLayer in apply_resilience_integration_test.go: it
// looks up the named image in the multi-image sidecar and returns the
// first layer that the target manifest references but the delta did not
// ship.
func firstBaselineOnlyReuseLayerForImage(
	t *testing.T, root string, sc *diff.Sidecar, imageName string,
) digest.Digest {
	t.Helper()
	var targetDigest digest.Digest
	for _, img := range sc.Images {
		if img.Name == imageName {
			targetDigest = img.Target.ManifestDigest
			break
		}
	}
	require.NotEmptyf(t, targetDigest, "image %q not found in sidecar", imageName)

	v2Path := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	manifest := readManifestFromOCIArchive(t, v2Path, targetDigest)

	for _, layer := range manifest.Layers {
		if _, shipped := sc.Blobs[layer.Digest]; !shipped {
			return layer.Digest
		}
	}
	t.Fatalf("svc %q target manifest has no baseline-only-reuse layer", imageName)
	return ""
}

// writeJSONFile serializes v as JSON and writes it to path with 0o600.
func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}
