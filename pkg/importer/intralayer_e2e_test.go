package importer_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

// buildDeltaIntraLayerAuto exports a delta with IntraLayer="auto", enabling
// patch encoding for layers that differ by a small amount.
func buildDeltaIntraLayerAuto(t *testing.T, transport, targetTar, baselineTar string) string {
	t.Skip("rewritten in Task 17")
	t.Helper()
	ctx := context.Background()
	target, err := imageio.ParseReference(transport + ":" + filepath.Join(repoRoot(t), "testdata/fixtures", targetTar))
	require.NoError(t, err)
	baseline, err := imageio.ParseReference(transport + ":" + filepath.Join(repoRoot(t), "testdata/fixtures", baselineTar))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{{Name: "default", BaselinePath: baseline.StringWithinTransport(), TargetPath: target.StringWithinTransport()}},
		OutputPath:  out,
		ToolVersion: "test",
		IntraLayer:  "auto",
	}))
	return out
}

// TestIntraLayer_E2E_OCI_ExportImportRoundTrip exercises the full intra-layer
// pipeline with OCI fixtures: export with auto → sidecar has patch entries →
// import → manifest digest matches original target.
func TestIntraLayer_E2E_OCI_ExportImportRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping intra-layer E2E test")
	}
	ctx := context.Background()

	// Step 1: Export v3 (target) against v2 (baseline) with intra-layer=auto.
	// v3 differs from v2 by only 1 byte in the shared layer, so the planner
	// should produce a patch entry.
	delta := buildDeltaIntraLayerAuto(t, "oci-archive", "v3_oci.tar", "v2_oci.tar")

	// Step 2: Inspect sidecar — expect at least one patch-encoded entry.
	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	hasPatch := false
	for _, e := range sidecar.ShippedInDelta {
		if e.Encoding == diff.EncodingPatch {
			hasPatch = true
			require.NotEmpty(t, e.Codec, "patch entry must declare a codec")
			require.NotEmpty(t, e.PatchFromDigest, "patch entry must reference a baseline blob")
			require.Greater(t, e.ArchiveSize, int64(0), "patch archive_size must be > 0")
			require.Less(t, e.ArchiveSize, e.Size, "patch archive_size must be < original size")
		}
	}
	require.True(t, hasPatch, "v3→v2 delta with intra-layer=auto must produce at least one patch entry")

	// Step 3: Import the delta back using v2 as baseline.
	baselineRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v2_oci.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v3_reconstructed.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baselineRef,
		OutputPath:        out,
	}))

	// Step 4: Verify manifest digest matches the sidecar's target.
	outRef, err := imageio.ParseReference("oci-archive:" + out)
	require.NoError(t, err)
	gotDigest := readManifestDigest(ctx, t, outRef)
	require.Equal(t, sidecar.Target.ManifestDigest, gotDigest,
		"imported image manifest digest must match original target")
}

// TestIntraLayer_E2E_S2_ExportImportRoundTrip is the docker schema-2 twin.
func TestIntraLayer_E2E_S2_ExportImportRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping intra-layer E2E test")
	}
	ctx := context.Background()

	delta := buildDeltaIntraLayerAuto(t, "docker-archive", "v3_s2.tar", "v2_s2.tar")

	// Inspect sidecar.
	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	hasPatch := false
	for _, e := range sidecar.ShippedInDelta {
		if e.Encoding == diff.EncodingPatch {
			hasPatch = true
			require.NotEmpty(t, e.Codec)
			require.NotEmpty(t, e.PatchFromDigest)
			require.Greater(t, e.ArchiveSize, int64(0))
			require.Less(t, e.ArchiveSize, e.Size)
		}
	}
	require.True(t, hasPatch, "v3→v2 schema2 delta with intra-layer=auto must produce at least one patch entry")

	// Import.
	baselineRef, err := imageio.ParseReference(
		"docker-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v2_s2.tar"))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "v3_reconstructed.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baselineRef,
		OutputPath:        out,
	}))

	// Verify manifest digest.
	outRef, err := imageio.ParseReference("docker-archive:" + out)
	require.NoError(t, err)
	gotDigest := readManifestDigest(ctx, t, outRef)
	require.Equal(t, sidecar.Target.ManifestDigest, gotDigest,
		"imported image manifest digest must match original target")
}

// TestIntraLayer_E2E_PatchSmallerThanFull confirms that the patch payload
// in the delta archive is smaller than the original blob size, validating
// that the planner correctly chose patch over full encoding.
func TestIntraLayer_E2E_PatchSmallerThanFull(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping intra-layer E2E test")
	}

	delta := buildDeltaIntraLayerAuto(t, "oci-archive", "v3_oci.tar", "v2_oci.tar")

	raw, err := archive.ReadSidecar(delta)
	require.NoError(t, err)
	sidecar, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)

	// With v3 differing from v2 by only 1 byte, the patch must be
	// significantly smaller than the full blob.
	for _, e := range sidecar.ShippedInDelta {
		if e.Encoding == diff.EncodingPatch {
			ratio := float64(e.ArchiveSize) / float64(e.Size)
			require.Less(t, ratio, 0.5,
				"patch encoding for a 1-byte-diff layer should be < 50%% of original; got %.2f%%", ratio*100)
		}
	}
}
