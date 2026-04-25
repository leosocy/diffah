package exporter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/exporter"
)

// TestExport_OutputIsByteIdenticalAcrossWorkerCounts is the load-bearing
// determinism guarantee for PR-3 (parallel encode + fpCache): driving the
// same Export across {1, 2, 4, 8, 16} workers must produce sha256-equal
// archives. Two pairs share the v1 baseline so the cross-pair fpCache
// path (singleflight collapse + cached bytes reuse) is exercised, not
// just the per-pair encode pool.
//
// If this regresses, the most likely causes — in order — are:
//   - non-stable iteration order in PickTopK / PlanShippedTopK tie-break
//   - blobPool first-write-wins becoming first-finisher-wins
//   - assembleSidecar ordering depending on map iteration
//
// See encodeShipped's contract comment in encode.go for the invariants
// this test pins down.
func TestExport_OutputIsByteIdenticalAcrossWorkerCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the zstd CLI per layer per worker count; not for -short")
	}

	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	workerCounts := []int{1, 2, 4, 8, 16}
	digests := make(map[int]string, len(workerCounts))

	for _, w := range workerCounts {
		t.Run(fmt.Sprintf("workers=%d", w), func(t *testing.T) {
			tmp := t.TempDir()
			out := filepath.Join(tmp, "delta.tar")
			opts := exporter.Options{
				Pairs: []exporter.Pair{
					{
						Name:        "alpha",
						BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
						TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
					},
					{
						Name:        "beta",
						BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
						TargetRef:   "oci-archive:../../testdata/fixtures/v3_oci.tar",
					},
				},
				Platform:      "linux/amd64",
				OutputPath:    out,
				ToolVersion:   "test",
				CreatedAt:     createdAt,
				Workers:       w,
				Candidates:    3,
				ZstdLevel:     12,
				ZstdWindowLog: 0, // 0 = historical default (27); writer not under test here
			}
			require.NoError(t, exporter.Export(context.Background(), opts))

			data, err := os.ReadFile(out)
			require.NoError(t, err)
			require.NotEmpty(t, data)
			h := sha256.Sum256(data)
			digests[w] = hex.EncodeToString(h[:])
		})
	}

	// Compare every digest against workers=1 (the strict-serial reference).
	require.NotEmpty(t, digests[1], "serial reference run was skipped")
	for _, w := range workerCounts {
		require.Equal(t, digests[1], digests[w],
			"archive sha256 differs across worker counts: %v", digests)
	}
}
