package exporter_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/exporter"
)

// TestExport_Phase4DefaultsDoNotRegressPhase3Size is the smoke-level
// quality regression: with the existing v1/v2/v3 OCI fixtures, the
// Phase-4 production-tuned defaults must produce an archive at most as
// large as the Phase-3 defaults. This is a guard against accidental
// inversion (e.g., flipping a flag default the wrong way) — not a
// promise of "X% smaller," because the fixtures are too small to give
// long-mode windows or level=22 the room they need to dominate.
//
// Spec §2 Goal #1 ("smaller deltas") is properly verified by the
// GB-scale benchmark gated behind DIFFAH_BIG_TEST=1 (deferred to a
// follow-up PR that also adds the registrytest synthesizing helpers).
func TestExport_Phase4DefaultsDoNotRegressPhase3Size(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes zstd CLI several times; not for -short")
	}

	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	pairs := []exporter.Pair{
		{
			Name:        "alpha",
			BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
		},
		{
			Name:        "beta",
			BaselineRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
			TargetRef:   "oci-archive:../../testdata/fixtures/v3_oci.tar",
		},
	}

	run := func(name string, workers, candidates, level, windowLog int) int64 {
		out := filepath.Join(t.TempDir(), name+".tar")
		require.NoError(t, exporter.Export(context.Background(), exporter.Options{
			Pairs:         pairs,
			Platform:      "linux/amd64",
			OutputPath:    out,
			ToolVersion:   "test",
			CreatedAt:     createdAt,
			Workers:       workers,
			Candidates:    candidates,
			ZstdLevel:     level,
			ZstdWindowLog: windowLog,
		}))
		fi, err := os.Stat(out)
		require.NoError(t, err)
		return fi.Size()
	}

	phase3 := run("phase3", 1, 1, 3, 27)
	phase4 := run("phase4", 8, 3, 22, 0) // 0 = auto

	t.Logf("phase3=%d phase4=%d ratio=%.3f", phase3, phase4,
		float64(phase4)/float64(phase3))
	require.LessOrEqual(t, phase4, phase3,
		"Phase-4 archive (%d) regressed against Phase-3 (%d) — likely a default flipped the wrong way",
		phase4, phase3)
}
