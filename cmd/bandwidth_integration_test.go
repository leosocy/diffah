//go:build integration

package cmd_test

import (
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// maxBaselineBlobHitsPerDigest is a loose upper bound on registry
// round-trips per unique baseline blob during a single 'diffah diff'
// run. It exists to catch runaway regressions — a future bug that
// accidentally introduces a per-layer fresh ImageSource (each
// layer reopened against the registry with fresh HEAD+GET probes)
// would drive this number into the tens. The exact today-value is
// closer to 3-4 (HEAD + retry-path HEAD + GET) and fluctuates
// between runs depending on the containers-image stream path;
// tightening this invariant below "loose upper bound" requires a
// Phase-4 caching refactor.
const maxBaselineBlobHitsPerDigest = 10

// TestDiffCLI_BandwidthBaselineBlobsAreFetchedBounded exercises a
// diff over a docker:// baseline and asserts:
//  1. the run succeeds;
//  2. the baseline repo was actually contacted (otherwise the test is
//     trivially green);
//  3. no single baseline blob digest exceeds
//     maxBaselineBlobHitsPerDigest registry hits.
//
// Scope narrowed to the **baseline** repo. The target's config blob
// is legitimately fetched multiple times (planPair inlines it into
// the sidecar; encodeShipped re-reads to emit the delta archive);
// fixing that is a Phase-4 scale-robustness concern.
//
// Observed counts are logged via t.Log for future optimization work.
func TestDiffCLI_BandwidthBaselineBlobsAreFetchedBounded(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "bandwidth/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)
	seedOCIIntoRegistry(t, srv, "bandwidth/v2",
		filepath.Join(root, "testdata/fixtures/v2_oci.tar"), nil)

	tmp := t.TempDir()
	out := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		"--tls-verify=false",
		registryDockerURL(t, srv, "bandwidth/v1"),
		registryDockerURL(t, srv, "bandwidth/v2"),
		out,
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)

	perBaselineDigest := make(map[digest.Digest]int)
	totalBaselineHits := 0
	for _, h := range srv.BlobHits() {
		if h.Repo != "bandwidth/v1" {
			continue
		}
		perBaselineDigest[h.Digest]++
		totalBaselineHits++
	}
	for d, count := range perBaselineDigest {
		t.Logf("baseline blob %s hits=%d", d, count)
		require.LessOrEqualf(t, count, maxBaselineBlobHitsPerDigest,
			"baseline blob %s fetched %d times; want ≤ %d — likely regression in ImageSource reuse", d, count, maxBaselineBlobHitsPerDigest)
	}
	require.NotZerof(t, totalBaselineHits,
		"expected at least one baseline blob hit; got zero — is the run actually reading the baseline from the registry?")
}
