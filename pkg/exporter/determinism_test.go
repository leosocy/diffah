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

// testCreatedAt is the pinned timestamp used by determinism tests. All runs of
// the same test must use the same timestamp or the archive will differ.
var testCreatedAt = time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

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
				Platform:    "linux/amd64",
				OutputPath:  out,
				ToolVersion: "test",
				CreatedAt:   testCreatedAt,
				Workers:     w,
				// Production-tuned Phase 4 defaults — pin determinism on the
				// path operators actually hit, not on a milder mid-tier
				// configuration. Level 22 + window-log=auto + 3 candidates
				// is the same combination cmd/encoding_flags.go installs.
				Candidates:    3,
				ZstdLevel:     22,
				ZstdWindowLog: 0,
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

// TestExport_OutputIsByteIdenticalAcrossMemoryBudgets verifies that enabling
// the admission controller (MemoryBudget=8GiB) produces a byte-identical
// archive to running without it (MemoryBudget=0). Admission only changes WHEN
// encodes run, never WHAT they produce; this test pins that invariant.
//
// Both runs use a generous budget (0 = unlimited; 8GiB = well above any test
// fixture's RSS) so neither should serialize unnecessarily.
func TestExport_OutputIsByteIdenticalAcrossMemoryBudgets(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the zstd CLI; not for -short")
	}

	basePairs := []exporter.Pair{
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
	}

	budgets := map[string]int64{
		"budget=0":    0,       // admission disabled
		"budget=8GiB": 8 << 30, // generous limit; all layers fit
	}
	digests := make(map[string]string, len(budgets))

	for label, budget := range budgets {
		t.Run(label, func(t *testing.T) {
			tmp := t.TempDir()
			out := filepath.Join(tmp, "bundle.tar")
			opts := exporter.Options{
				Pairs:         basePairs,
				Platform:      "linux/amd64",
				OutputPath:    out,
				ToolVersion:   "test",
				CreatedAt:     testCreatedAt,
				Workers:       4,
				Candidates:    3,
				ZstdLevel:     22,
				ZstdWindowLog: 0,
				MemoryBudget:  budget,
			}
			require.NoError(t, exporter.Export(context.Background(), opts))
			data, err := os.ReadFile(out)
			require.NoError(t, err)
			require.NotEmpty(t, data)
			h := sha256.Sum256(data)
			digests[label] = hex.EncodeToString(h[:])
		})
	}

	require.Equal(t, digests["budget=0"], digests["budget=8GiB"],
		"archive sha256 must be identical regardless of admission budget: %v", digests)
}
