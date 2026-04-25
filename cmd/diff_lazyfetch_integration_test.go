//go:build integration

package cmd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// TestDiffCLI_LazyFetch_BaselineBlobsBoundedByCandidates pushes the
// v1/v2 fixtures to an in-process registry, runs `diffah diff
// --candidates=3`, and asserts that the per-baseline-blob fetch count
// does not blow up with K. Specifically: each baseline layer digest is
// fetched at most (1 + numShipped) times — once for fingerprinting,
// plus at most numShipped times if it is selected as a patch reference
// across shipped target layers. The K candidate fan-out picks at most
// min(K, numBaseline) distinct baselines per shipped layer, so the per-
// digest count is independent of K.
//
// PR-2 only covers within-pair caching (the sync.Once on the Planner's
// baseline fingerprint cache); cross-pair caching is the property the
// fpCache wired in PR-3 will enforce. This test stays within a single
// pair.
func TestDiffCLI_LazyFetch_BaselineBlobsBoundedByCandidates(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedV1V2(t, srv, root)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	// Snapshot baseline blob-fetch counts AFTER seeding so we measure
	// only the diff-time fetches. The seeding pipeline issues HEAD
	// existence probes that BlobHits would otherwise count.
	preDiffHits := countRepoHits(srv, "fixtures/v1")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		registryDockerURL(t, srv, "fixtures/v1"),
		registryDockerURL(t, srv, "fixtures/v2"),
		deltaPath,
		"--tls-verify=false",
		"--candidates=3",
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)

	info, err := os.Stat(deltaPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))

	// v1 baseline has 2 layers (shared base + version.txt) plus 1
	// config blob; v2 ships 1 differing layer (version.txt). With
	// --candidates=3:
	//   * ensureBaselineFP fetches each baseline layer once (×2 = 2 GETs).
	//   * PlanShippedTopK reads up to min(3, 2) = 2 baseline blobs as
	//     candidate patch references for the 1 shipped layer (×2 GETs).
	// → expected diff-time baseline-blob GETs ≈ 4 (plus possibly a
	// small number of config/empty-blob accesses by the imageio path).
	//
	// upperBound=6 leaves slack for transport-internal fetches while
	// flagging the "K multiplies fingerprint cost" regression: a
	// buggy implementation that re-fingerprints per candidate would
	// produce 2 × K (= 6) FP fetches alone + ref fetches → ≥ 8 total.
	const upperBound = 6
	diffHits := countRepoHits(srv, "fixtures/v1") - preDiffHits
	require.LessOrEqual(t, diffHits, upperBound,
		"baseline-side blob requests during diff (%d) exceed upper bound %d — "+
			"--candidates=K should not multiply baseline fingerprint fetches by K",
		diffHits, upperBound)
}

// countRepoHits returns the number of BlobHits recorded for the given
// repo so far. Useful for taking before/after snapshots around an
// operation whose blob traffic we want to measure in isolation.
func countRepoHits(srv *registrytest.Server, repo string) int {
	hits := srv.BlobHits()
	n := 0
	for _, h := range hits {
		if h.Repo == repo {
			n++
		}
	}
	return n
}
