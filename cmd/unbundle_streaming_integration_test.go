//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUnbundle_PoolPartialVsStrict_SingleImage exercises the PR5
// admission-pool path on the unbundle command in both --strict and
// partial modes against a single-image bundle whose baseline has been
// mutilated (B2: a layer the target manifest references but the delta
// did not ship has been stripped from the baseline).
//
// Behavior pinned:
//   - Default mode: writes a non-zero report exit code (CategoryContent
//     → exit 4) AND surfaces the B2 hint phrase. Single-image bundles
//     have no siblings to capture, so this scope-restricted check
//     proves the pool routes per-image errors through the report path
//     unchanged from the serial loop's behavior.
//   - --strict adds nothing observable on a single-image bundle; the
//     same exit/stderr is expected. (Strict's added value is sibling
//     cancellation, which only matters for multi-image bundles.)
//
// SCOPE NOTE: The canonical fixtures only support single-image
// bundles via the `unbundle` spec. With a single image there are no
// siblings whose baselines could leak through the shared
// BaselineSpool's cross-image dedup, so --workers=4 is safe here.
// Multi-image scenarios with mismatched per-image baselines are
// covered by TestUnbundleCLI_PartialModeRecordsApplyTimeB2 and
// TestUnbundleCLI_StrictAbortsOnFirstApplyFailure (which pin
// --workers=1 to preserve serial ordering — see those tests'
// comments for the spool-dedup rationale).
func TestUnbundle_PoolPartialVsStrict_SingleImage(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	// 1. Build a single-image bundle (v1 → v2).
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
	{
		_, stderr, exit := runDiffahBin(t, bin, "bundle", specPath, bundleOut)
		require.Equal(t, 0, exit, "bundle: stderr=%q", stderr)
	}

	// 2. Make a mutilated baseline that strips a B2 layer (referenced by
	//    the target manifest, not shipped in the delta — must come from
	//    the baseline at apply time).
	deltaSidecar := readSidecarFromArchive(t, bundleOut)
	reuseLayer := firstBaselineOnlyReuseLayer(t, root, deltaSidecar)
	srcBaseline := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	badBaseline := filepath.Join(tmp, "bad_baseline.tar")
	stripLayerFromOCIArchive(t, srcBaseline, badBaseline, reuseLayer)

	baselineSpec := map[string]any{
		"baselines": map[string]string{
			"app": "oci-archive:" + badBaseline,
		},
	}
	baselinePath := filepath.Join(tmp, "baselines.json")
	raw, err = json.Marshal(baselineSpec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(baselinePath, raw, 0o600))

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

	// 3. Default (partial) mode with --workers=4: pool routes the per-image
	//    error through the report path. CategoryContent → exit 4 + B2 hint.
	{
		_, stderr, exit := runDiffahBin(t, bin,
			"unbundle",
			"--workers", "4",
			bundleOut, baselinePath, outputsPath,
		)
		require.Equal(t, 4, exit,
			"expected exit 4 (content) for B2; stderr=%q", stderr)
		require.Contains(t, stderr, "re-run diff with a wider baseline",
			"expected B2 hint phrase; stderr=%q", stderr)
	}

	// 4. --strict adds no new observable on a single-image bundle but must
	//    not regress: same exit code, same hint.
	{
		// Use a fresh restored path so step 3's output cannot pollute
		// detection if the strict path were to short-circuit before
		// touching the dest.
		restoredArchiveStrict := filepath.Join(tmp, "app-strict.tar")
		outputsStrict := map[string]any{
			"outputs": map[string]string{"app": "oci-archive:" + restoredArchiveStrict},
		}
		outputsPathStrict := filepath.Join(tmp, "outputs-strict.json")
		raw, err := json.Marshal(outputsStrict)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(outputsPathStrict, raw, 0o600))

		_, stderr, exit := runDiffahBin(t, bin,
			"unbundle",
			"--workers", "4",
			"--strict",
			bundleOut, baselinePath, outputsPathStrict,
		)
		require.Equal(t, 4, exit,
			"expected exit 4 in strict mode; stderr=%q", stderr)
		require.Contains(t, stderr, "re-run diff with a wider baseline",
			"expected B2 hint phrase in strict mode; stderr=%q", stderr)
	}
}
